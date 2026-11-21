// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
)

// Scans is what the API asks of the scanner.
type Scans interface {
	// Request asks for a scan of a library, or of all with 0. It does
	// not wait.
	Request(libraryID int64)
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
	case req.Kind != "movies" && req.Kind != "shows":
		s.fail(w, r, http.StatusBadRequest, "bad_request", `kind is "movies" or "shows"`)
		return
	case req.Profile != "default" && req.Profile != "anime":
		s.fail(w, r, http.StatusBadRequest, "bad_request", `profile is "default" or "anime"`)
		return
	case len(req.Roots) == 0 || len(req.Roots) > 32:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a library has 1 to 32 folders")
		return
	}
	for _, p := range req.Roots {
		if !filepath.IsAbs(p) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a folder is an absolute path: "+p)
			return
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "not a folder this server can open: "+p)
			return
		}
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
