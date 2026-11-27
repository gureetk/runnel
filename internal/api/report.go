// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/parse"
)

type reportLibrary struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Files      int            `json:"files"`
	Counts     map[string]int `json:"counts"`
	Noted      int            `json:"noted"`
	Unreadable int            `json:"unreadable"`
}

type reportFile struct {
	FileID     int64  `json:"file_id"`
	LibraryID  int64  `json:"library_id"`
	Path       string `json:"path"`
	Class      string `json:"class"`
	Title      string `json:"title"`
	Year       *int   `json:"year"`
	Season     *int   `json:"season,omitempty"`
	Episode    *int   `json:"episode,omitempty"`
	EpisodeEnd *int   `json:"episode_end,omitempty"`
	Note       string `json:"note,omitempty"`
}

type reportUnreadable struct {
	FileID    int64  `json:"file_id"`
	LibraryID int64  `json:"library_id"`
	Path      string `json:"path"`
	Error     string `json:"error"`
}

func intOrNil(n int, ok bool) *int {
	if !ok {
		return nil
	}
	return &n
}

// handleParseReport is GET /admin/parse-report: how every file's name
// parsed, counted by class, with the files that need a look, and the files
// ffprobe could not read. Parameters: library; class, a class or all; and
// noted=1 for the noted files only: those outside none that have a note.
// Left out, the files listed are those in guess and none, and the noted.
func (s *Server) handleParseReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	libs, err := s.db.Libraries(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v := q.Get("library"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "library is a library's id")
			return
		}
		lib, err := s.db.Library(r.Context(), id)
		if errors.Is(err, db.ErrNotFound) {
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return
		} else if err != nil {
			s.internal(w, r, err)
			return
		}
		libs = []model.Library{lib}
	}
	// By default the files that need a look: guess, unnumbered and none,
	// and the files of the other classes that have a note.
	listed, withNote := []string{parse.Guess, parse.Unnumbered, parse.None}, true
	switch c := q.Get("class"); {
	case c == "all":
		listed = parse.Classes
	case slices.Contains(parse.Classes, c):
		listed, withNote = []string{c}, false
	case c != "":
		s.fail(w, r, http.StatusBadRequest, "bad_request", "class is explicit, anchored, dash, guess, none or all")
		return
	}
	notedOnly := false
	switch q.Get("noted") {
	case "", "0":
	case "1":
		notedOnly = true
		if q.Get("class") == "" {
			listed = parse.Classes
		}
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "noted is 1, for the files with a note only")
		return
	}

	rows, err := library.Report(r.Context(), s.db, libs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	body := struct {
		Libraries  []reportLibrary    `json:"libraries"`
		Files      []reportFile       `json:"files"`
		Unreadable []reportUnreadable `json:"unreadable"`
	}{Libraries: []reportLibrary{}, Files: []reportFile{}, Unreadable: []reportUnreadable{}}
	at := map[int64]int{}
	for i, l := range libs {
		counts := map[string]int{}
		for _, c := range parse.Classes {
			counts[c] = 0
		}
		body.Libraries = append(body.Libraries, reportLibrary{ID: l.ID, Name: l.Name, Kind: l.Kind, Counts: counts})
		at[l.ID] = i
		bad, err := s.db.UnreadableFiles(r.Context(), l.ID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		body.Libraries[i].Unreadable = len(bad)
		for _, u := range bad {
			body.Unreadable = append(body.Unreadable, reportUnreadable{FileID: u.ID, LibraryID: l.ID, Path: u.RelPath, Error: u.Error})
		}
	}
	for _, row := range rows {
		lib := &body.Libraries[at[row.LibraryID]]
		lib.Files++
		lib.Counts[row.Class]++
		// A note on a file in none says why it makes no item. A noted file
		// is one that makes an item and still has something to look at.
		// A file in none or unnumbered is listed by its class; its reason
		// does not make it noted.
		noted := row.Note != "" && row.Class != parse.None && row.Class != parse.Unnumbered
		if noted {
			lib.Noted++
		}
		switch {
		case notedOnly && !noted:
			continue
		case !slices.Contains(listed, row.Class) && !(withNote && noted):
			continue
		}
		f := reportFile{FileID: row.FileID, LibraryID: row.LibraryID, Path: row.Path, Class: row.Class,
			Title: row.Title, Year: intOrNil(row.Year, row.Year != 0), Note: row.Note}
		if row.IsEpisode {
			f.Season, f.Episode = intOrNil(row.Season, true), intOrNil(row.Episode, row.Class != parse.Unnumbered)
			f.EpisodeEnd = intOrNil(row.EpisodeEnd, row.EpisodeEnd != 0)
		}
		body.Files = append(body.Files, f)
	}
	writeJSON(w, http.StatusOK, body)
}
