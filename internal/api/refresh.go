// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gureetk/runnel/internal/db"
)

// Fetches queues the fetching of metadata (docs/06-library-and-metadata.md,
// section 6.3).
type Fetches interface {
	// Queue queues the fetch of every film and series of a library, or of
	// every library with 0, that is past its age; again queues what failed
	// too.
	Queue(ctx context.Context, libraryID int64, again bool) (int, error)
	// Refresh queues one item's fetch, which asks the provider again.
	Refresh(ctx context.Context, itemID, libraryID int64) error
	// KeySet is told that a new metadata key was set: what waits for one
	// is fetched.
	KeySet(ctx context.Context)
}

// hasKey reports whether a metadata key is set, the environment's or one
// an admin set, and answers 409 no_key when none is.
func (s *Server) hasKey(w http.ResponseWriter, r *http.Request) bool {
	k, err := s.keySetting(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if !k.Set || s.fetches == nil {
		s.fail(w, r, http.StatusConflict, "no_key", "no metadata key is set: set one in the settings")
		return false
	}
	return true
}

// handleRefreshItem is POST /admin/items/{id}/refresh (docs/04-api.md,
// section 8.4): a film or a series is fetched again, asking the provider
// whatever the age of what it has, with every season it holds; a season or
// an episode refreshes its series.
func (s *Server) handleRefreshItem(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "item")
	if !ok {
		return
	}
	c, err := s.db.Item(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such item")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	switch c.Type {
	case "movie", "series":
	case "season", "episode":
		id = c.SeriesID
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a film, a series, a season or an episode is refreshed")
		return
	}
	if !s.hasKey(w, r) {
		return
	}
	if err := s.fetches.Refresh(r.Context(), id, c.LibraryID); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct{}{})
}

// handleRefreshLibrary is POST /admin/libraries/{id}/refresh
// (docs/04-api.md, section 8.2): every film and series of the library
// whose metadata is past its age is fetched again, and so is what failed.
func (s *Server) handleRefreshLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "library")
	if !ok {
		return
	}
	if _, err := s.db.Library(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	if !s.hasKey(w, r) {
		return
	}
	if _, err := s.fetches.Queue(r.Context(), id, true); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct{}{})
}
