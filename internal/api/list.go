// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gureetk/runnel/internal/db"
)

// handleListed is PUT /items/{id}/list, and with listed false DELETE: My
// list (docs/04-api.md, section 4.7).
func (s *Server) handleListed(listed bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		err := db.ErrNotFound
		if perr == nil {
			err = s.db.SetListed(r.Context(), callerOf(r.Context()).user.ID, id, listed, s.now().UnixMilli())
		}
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such item")
		case errors.Is(err, db.ErrNotListable):
			s.fail(w, r, http.StatusBadRequest, "bad_request", "only films and series go on My list")
		default:
			s.internal(w, r, err)
		}
	}
}
