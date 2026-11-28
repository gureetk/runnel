// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gureetk/runnel/internal/db"
)

// personBody is a person (docs/04-api.md, section 2.4).
type personBody struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Titles     int    `json:"titles"`
}

// handlePerson is GET /people/{id} (docs/04-api.md, section 4.8).
func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p, gerr := s.db.PersonOf(r.Context(), id)
	switch {
	case err != nil || errors.Is(gerr, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such person")
		return
	case gerr != nil:
		s.internal(w, r, gerr)
		return
	}
	writeJSON(w, http.StatusOK, personBody{ID: p.ID, Name: p.Name, Department: p.Department, Titles: p.Titles})
}
