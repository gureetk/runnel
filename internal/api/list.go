// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"math"
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

type listCursor struct {
	L int64  `json:"l"`
	T string `json:"t"`
	I int64  `json:"i"`
}

// listPage is a page of GET /items?list=1: the signed-in user's list,
// newest first or by title, of films, series or both.
func (s *Server) listPage(w http.ResponseWriter, r *http.Request, limit int, first bool) (cards []db.Card, total int, next func(db.Card) string, ok bool) {
	q := r.URL.Query()
	for _, k := range []string{"library", "parent", "series"} {
		if q.Has(k) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", k+" does not go with list")
			return nil, 0, nil, false
		}
	}
	if q.Get("list") != "1" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", `list is "1"`)
		return nil, 0, nil, false
	}
	typ := q.Get("type")
	if typ != "" && typ != "movie" && typ != "series" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", `with list, type is "movie" or "series"`)
		return nil, 0, nil, false
	}
	byTitle := false
	switch q.Get("sort") {
	case "", "listed":
	case "title":
		byTitle = true
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", `with list, this server sorts by "listed" or "title", so far`)
		return nil, 0, nil, false
	}
	c := listCursor{L: math.MaxInt64, I: math.MaxInt64}
	if byTitle {
		c = listCursor{}
	}
	if !first && !decodeCursor(q.Get("cursor"), &c) {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
		return nil, 0, nil, false
	}
	user := callerOf(r.Context()).user.ID
	var err error
	if first {
		if total, err = s.db.CountListed(r.Context(), user, typ); err != nil {
			s.internal(w, r, err)
			return nil, 0, nil, false
		}
	}
	cards, at, err := s.db.Listed(r.Context(), user, typ, byTitle, db.ListKey{ListedAt: c.L, Title: c.T, ID: c.I}, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return nil, 0, nil, false
	}
	listedAt := map[int64]int64{}
	for i, card := range cards {
		listedAt[card.ID] = at[i]
	}
	next = func(card db.Card) string {
		if byTitle {
			return encodeCursor(listCursor{T: card.SortTitle, I: card.ID})
		}
		return encodeCursor(listCursor{L: listedAt[card.ID], I: card.ID})
	}
	return cards, total, next, true
}
