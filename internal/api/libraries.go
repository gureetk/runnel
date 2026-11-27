// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
)

// Scans is what the API asks of the scanner.
type Scans interface {
	// Request asks for a scan of a library, or of all with 0. It does
	// not wait.
	Request(libraryID int64)
	// Walking reports whether a scan is going through a library's
	// folders now, or building its items from them.
	Walking(libraryID int64) bool
	// Forget is told of folders that were removed: they are no longer
	// watched.
	Forget(rootIDs []int64)
}

type rootBody struct {
	ID        int64  `json:"id"`
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

type adminLibraryBody struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Kind    string     `json:"kind"`
	Profile string     `json:"profile"`
	Roots   []rootBody `json:"roots"`
}

func adminLibraryJSON(l model.Library) adminLibraryBody {
	b := adminLibraryBody{ID: l.ID, Name: l.Name, Kind: l.Kind, Profile: l.Profile, Roots: []rootBody{}}
	for _, r := range l.Roots {
		b.Roots = append(b.Roots, rootBody{ID: r.ID, Path: r.Path, Available: r.Available})
	}
	return b
}

type createLibraryRequest struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Profile string   `json:"profile"`
	Roots   []string `json:"roots"`
}

// handleCreateLibrary is POST /admin/libraries (docs/04-api.md, section
// 8.2). The new library's first scan starts at once.
func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var req createLibraryRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Profile == "" {
		req.Profile = "default"
	}
	switch {
	case !validName(req.Name):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a name has 1 to 40 characters, no control characters, and no space at either end")
		return
	case req.Kind != "movies" && req.Kind != "shows" && req.Kind != "mixed":
		s.fail(w, r, http.StatusBadRequest, "bad_request", `kind is "movies", "shows" or "mixed"`)
		return
	case req.Profile != "default" && req.Profile != "anime":
		s.fail(w, r, http.StatusBadRequest, "bad_request", `profile is "default" or "anime"`)
		return
	case len(req.Roots) == 0 || len(req.Roots) > 32:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a library has 1 to 32 folders")
		return
	}
	if !s.validFolders(w, r, req.Roots) {
		return
	}

	lib, err := s.db.CreateLibrary(r.Context(), req.Name, req.Kind, req.Profile, req.Roots, s.now().UnixMilli())
	switch {
	case errors.Is(err, db.ErrNameTaken):
		s.fail(w, r, http.StatusConflict, "conflict", "a library with that name exists")
		return
	case errors.Is(err, db.ErrRootOverlap):
		s.fail(w, r, http.StatusConflict, "conflict", "a folder is, contains or lies inside one that a library already scans")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if s.scans != nil {
		s.scans.Request(lib.ID)
	}
	writeJSON(w, http.StatusCreated, adminLibraryJSON(lib))
}

// handleScan is POST /admin/scan: {"library_id": 2}, or {} for every
// library (docs/04-api.md, section 8.2).
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LibraryID *int64 `json:"library_id"`
	}
	if r.ContentLength != 0 && !s.decode(w, r, &req) {
		return
	}
	id := int64(0)
	if req.LibraryID != nil {
		id = *req.LibraryID
		if _, err := s.db.Library(r.Context(), id); errors.Is(err, db.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return
		} else if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if s.scans != nil {
		s.scans.Request(id)
	}
	writeJSON(w, http.StatusAccepted, struct{}{})
}

// validFolders answers 400 and reports false unless every path is an
// absolute path to a folder that the server can open.
func (s *Server) validFolders(w http.ResponseWriter, r *http.Request, paths []string) bool {
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a folder is an absolute path: "+p)
			return false
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "not a folder this server can open: "+p)
			return false
		}
	}
	return true
}

type scanStateBody struct {
	State string `json:"state"`
	Done  *int   `json:"done,omitempty"`
	Total *int   `json:"total,omitempty"`
}

type lastScanBody struct {
	At      int64   `json:"at"`
	MS      int64   `json:"ms"`
	Added   int     `json:"added"`
	Changed int     `json:"changed"`
	Removed int     `json:"removed"`
	Error   *string `json:"error"`
}

type adminRootBody struct {
	ID        int64         `json:"id"`
	Path      string        `json:"path"`
	Available bool          `json:"available"`
	LastScan  *lastScanBody `json:"last_scan"`
}

type libraryStateBody struct {
	ID      int64           `json:"id"`
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Profile string          `json:"profile"`
	Items   int             `json:"items"`
	Scan    scanStateBody   `json:"scan"`
	Roots   []adminRootBody `json:"roots"`
}

// handleAdminLibraries is GET /admin/libraries: the libraries with their
// folders, what a scan does now, and each folder's last scan
// (docs/04-api.md, section 8.2).
func (s *Server) handleAdminLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.db.AdminLibraries(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	work, err := s.db.WorkOf(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	probing := map[int64]db.Work{}
	for _, wk := range work {
		if wk.Kind == "probe" && wk.Queued+wk.Running > 0 {
			probing[wk.LibraryID] = wk
		}
	}
	body := struct {
		Libraries []libraryStateBody `json:"libraries"`
	}{Libraries: []libraryStateBody{}}
	for _, l := range libs {
		b := libraryStateBody{ID: l.ID, Name: l.Name, Kind: l.Kind, Profile: l.Profile, Items: l.Items,
			Scan: scanStateBody{State: "idle"}, Roots: []adminRootBody{}}
		failed := false
		for _, root := range l.Roots {
			rb := adminRootBody{ID: root.ID, Path: root.Path, Available: root.Available}
			if ls := root.LastScan; ls != nil {
				rb.LastScan = &lastScanBody{At: ls.At, MS: ls.MS, Added: ls.Added, Changed: ls.Changed, Removed: ls.Removed,
					Error: strOrNil(ls.Error)}
				failed = failed || ls.Error != ""
			}
			b.Roots = append(b.Roots, rb)
		}
		wk, isProbing := probing[l.ID]
		switch {
		case s.scans != nil && s.scans.Walking(l.ID):
			b.Scan.State = "walking"
		case isProbing:
			b.Scan = scanStateBody{State: "probing", Done: &wk.Done, Total: &wk.Total}
		case failed:
			b.Scan.State = "failed"
		}
		body.Libraries = append(body.Libraries, b)
	}
	writeJSON(w, http.StatusOK, body)
}

// handleChangeLibrary is PATCH /admin/libraries/{id}: a new name or
// profile, a films or a shows library made mixed, folders added and
// folders removed, all at once or none of it (docs/04-api.md, section
// 8.2). A library added to, or made mixed, is scanned at once.
func (s *Server) handleChangeLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "library")
	if !ok {
		return
	}
	var req map[string]json.RawMessage
	if !s.decode(w, r, &req) {
		return
	}
	var c db.LibraryChange
	field := func(k string, v any) bool {
		if err := json.Unmarshal(req[k], v); err != nil {
			s.fail(w, r, http.StatusBadRequest, "bad_request", k+" is not what this call takes")
			return false
		}
		return true
	}
	for k := range req {
		switch k {
		case "name":
			if !field(k, &c.Name) {
				return
			}
			if c.Name == nil || !validName(*c.Name) {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "a name has 1 to 40 characters, no control characters, and no space at either end")
				return
			}
		case "profile":
			if !field(k, &c.Profile) {
				return
			}
			if c.Profile == nil || (*c.Profile != "default" && *c.Profile != "anime") {
				s.fail(w, r, http.StatusBadRequest, "bad_request", `profile is "default" or "anime"`)
				return
			}
		case "add_roots":
			if !field(k, &c.AddRoots) || !s.validFolders(w, r, c.AddRoots) {
				return
			}
		case "remove_roots":
			if !field(k, &c.RemoveRoots) {
				return
			}
		case "kind":
			if !field(k, &c.Kind) {
				return
			}
			if c.Kind == nil {
				s.fail(w, r, http.StatusBadRequest, "bad_request", `kind is "mixed", for a films or a shows library`)
				return
			}
		default:
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a library's changes are name, kind, profile, add_roots and remove_roots")
			return
		}
	}
	var before model.Library
	if c.Kind != nil {
		var err error
		if before, err = s.db.Library(r.Context(), id); errors.Is(err, db.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return
		} else if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	lib, err := s.db.ChangeLibrary(r.Context(), id, c, s.now().UnixMilli())
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
		return
	case errors.Is(err, db.ErrNameTaken):
		s.fail(w, r, http.StatusConflict, "conflict", "a library with that name exists")
		return
	case errors.Is(err, db.ErrKindChange):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a films or a shows library can become mixed, and no other change of kind is allowed")
		return
	case errors.Is(err, db.ErrRootOverlap):
		s.fail(w, r, http.StatusConflict, "conflict", "a folder is, contains or lies inside one that a library already scans")
		return
	case errors.Is(err, db.ErrNoSuchRoot):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a folder to remove is not one of this library's")
		return
	case errors.Is(err, db.ErrRootCount):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a library has 1 to 32 folders")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if s.scans != nil && len(c.RemoveRoots) > 0 {
		s.scans.Forget(c.RemoveRoots)
	}
	// Folders added are scanned, and a library of a new kind is read again
	// by it: a scan builds its items.
	if s.scans != nil && (len(c.AddRoots) > 0 || (before.Kind != "" && lib.Kind != before.Kind)) {
		s.scans.Request(id)
	}
	writeJSON(w, http.StatusOK, adminLibraryJSON(lib))
}

// handleDeleteLibrary is DELETE /admin/libraries/{id}: the library goes,
// with its items and everyone's watch state for them.
func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "library")
	if !ok {
		return
	}
	roots, err := s.db.DeleteLibrary(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	if s.scans != nil && len(roots) > 0 {
		s.scans.Forget(roots)
	}
	w.WriteHeader(http.StatusNoContent)
}

// fsMaxFolders is the most folders GET /admin/fs lists.
const fsMaxFolders = 500

type fsFolderBody struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Entries  *int   `json:"entries"`
	Readable bool   `json:"readable"`
}

// handleFS is GET /admin/fs: the folders inside a folder on the server,
// by name, for choosing a library's folders (docs/04-api.md, section
// 8.2). It reads the folder and each folder in it, and nothing deeper.
func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "path" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameter is path")
			return
		}
	}
	p := q.Get("path")
	if p == "" {
		p = "/"
	}
	if !filepath.IsAbs(p) {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "path is an absolute path")
		return
	}
	p = filepath.Clean(p)
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		s.fail(w, r, http.StatusNotFound, "not_found", "not a folder on the server")
		return
	}
	names, err := folderNames(p)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "the server cannot read this folder")
		return
	}
	slices.Sort(names)
	body := struct {
		Path      string         `json:"path"`
		Parent    *string        `json:"parent"`
		Truncated bool           `json:"truncated"`
		Folders   []fsFolderBody `json:"folders"`
	}{Path: p, Folders: []fsFolderBody{}}
	if p != "/" {
		parent := filepath.Dir(p)
		body.Parent = &parent
	}
	if len(names) > fsMaxFolders {
		names, body.Truncated = names[:fsMaxFolders], true
	}
	for _, name := range names {
		child := filepath.Join(p, name)
		f := fsFolderBody{Name: name, Path: child}
		if n, err := countEntries(child); err == nil {
			f.Entries, f.Readable = &n, true
		}
		body.Folders = append(body.Folders, f)
	}
	writeJSON(w, http.StatusOK, body)
}

// folderNames lists the folders in dir, and the links to folders, leaving
// out names that begin with a dot. It reads the folder in batches, so a
// large one is never held whole.
func folderNames(dir string) ([]string, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	for {
		batch, err := f.ReadDir(256)
		for _, e := range batch {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			switch {
			case e.IsDir():
				names = append(names, name)
			case e.Type()&os.ModeSymlink != 0:
				if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.IsDir() {
					names = append(names, name)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// countEntries counts what is directly inside a folder, in batches.
func countEntries(dir string) (int, error) {
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	for {
		batch, err := f.Readdirnames(256)
		n += len(batch)
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
	}
}
