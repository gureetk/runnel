// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

// recentDays is how far back Recently added on Home reaches (P7).
const recentDays = 30

// listCursor is where a page of a list continues: its sort and order, so
// that a cursor is not used for another, and the last row's key.
type listCursor struct {
	S string `json:"s"`
	D bool   `json:"d,omitempty"`
	N bool   `json:"n,omitempty"`
	V any    `json:"v"`
	T string `json:"t"`
	I int64  `json:"i"`
}

// listQuery reads the parameters of a list of films and series
// (docs/04-api.md, section 4.3): a library's, a genre's across libraries
// or in one, or what the user has on My list. It answers 400 or 404 and
// reports false when they are not right.
func (s *Server) listQuery(w http.ResponseWriter, r *http.Request) (db.ListQuery, bool) {
	q := r.URL.Query()
	lq := db.ListQuery{UserID: callerOf(r.Context()).user.ID, Genre: q.Get("genre"), Played: -1}
	if q.Has("list") {
		if q.Get("list") != "1" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", `list is "1"`)
			return lq, false
		}
		if q.Has("library") {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "library does not go with list")
			return lq, false
		}
		lq.Listed = true
	}
	var kind string
	if v := q.Get("library"); v != "" || (!lq.Listed && lq.Genre == "") {
		id, perr := strconv.ParseInt(v, 10, 64)
		lib, gerr := s.db.Library(r.Context(), id)
		switch {
		case v == "":
			s.fail(w, r, http.StatusBadRequest, "bad_request", "library, series, parent, ids, genre or list is needed")
			return lq, false
		case perr != nil || errors.Is(gerr, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return lq, false
		case gerr != nil:
			s.internal(w, r, gerr)
			return lq, false
		}
		lq.LibraryID, kind = lib.ID, map[string]string{"movies": "movie", "shows": "series"}[lib.Kind]
	}
	switch t := q.Get("type"); {
	case t == "movie" || t == "series":
		lq.Types = []string{t}
	case t != "":
		s.fail(w, r, http.StatusBadRequest, "bad_request", `at the top of a list, type is "movie" or "series"`)
		return lq, false
	case kind != "":
		lq.Types = []string{kind}
	}
	switch v := q.Get("played"); v {
	case "":
	case "0", "1":
		lq.Played = int(v[0] - '0')
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", `played is "0" or "1"`)
		return lq, false
	}
	lq.Sort = q.Get("sort")
	switch {
	case lq.Sort == "":
		lq.Sort = "title"
		if lq.Listed {
			lq.Sort = "listed"
		}
	case !db.Sorts[lq.Sort] || (lq.Sort == "listed" && !lq.Listed):
		s.fail(w, r, http.StatusBadRequest, "bad_request", `sort is "title", "added", "date" or "rating", and "listed" with list`)
		return lq, false
	}
	switch q.Get("order") {
	case "":
		lq.Desc = lq.Sort != "title"
	case "asc":
	case "desc":
		lq.Desc = true
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", `order is "asc" or "desc"`)
		return lq, false
	}
	return lq, true
}

// list answers a page of a list of films and series, after a cursor or
// from an offset.
func (s *Server) list(w http.ResponseWriter, r *http.Request, limit int) {
	lq, ok := s.listQuery(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if q.Has("cursor") && q.Has("offset") {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "offset takes the place of cursor")
		return
	}
	var after *db.ListKey
	if v := q.Get("cursor"); v != "" {
		var c listCursor
		if !decodeCursor(v, &c) || c.S != lq.Sort || c.D != lq.Desc {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave for this list")
			return
		}
		after = &db.ListKey{Null: c.N, Value: c.V, Title: c.T, ID: c.I}
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "offset is a number of rows")
			return
		}
		offset = n
	}
	withTotal := after == nil
	total := 0
	if withTotal {
		var err error
		if total, err = s.db.CountItems(r.Context(), lq); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	cards, keys, err := s.db.ListItems(r.Context(), lq, after, offset, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	keyOf := map[int64]db.ListKey{}
	for i, c := range cards {
		keyOf[c.ID] = keys[i]
	}
	next := func(c db.Card) string {
		k := keyOf[c.ID]
		return encodeCursor(listCursor{S: lq.Sort, D: lq.Desc, N: k.Null, V: k.Value, T: k.Title, I: k.ID})
	}
	s.page(w, r, cards, limit, next, withTotal, total)
}

// itemsByID answers the cards of some items, in the order asked for: at
// most 200, for refreshing a few cards.
func (s *Server) itemsByID(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "ids" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "ids goes alone")
			return
		}
	}
	var ids []int64
	for _, v := range strings.Split(q.Get("ids"), ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "ids are numbers, separated by commas")
			return
		}
		ids = append(ids, id)
	}
	if len(ids) > 200 {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "at most 200 ids")
		return
	}
	cards, err := s.db.ItemsByID(r.Context(), ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.page(w, r, cards, len(cards), nil, false, 0)
}

// handleLetters is GET /items/letters: where each letter begins in a list
// sorted by title.
func (s *Server) handleLetters(w http.ResponseWriter, r *http.Request) {
	for k := range r.URL.Query() {
		if k != "library" && k != "type" && k != "genre" && k != "played" && k != "list" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameters are library, type, genre, played and list")
			return
		}
	}
	lq, ok := s.listQuery(w, r)
	if !ok {
		return
	}
	lq.Sort, lq.Desc = "title", false
	letters, total, err := s.db.Letters(r.Context(), lq)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type letterBody struct {
		Letter string `json:"letter"`
		Offset int    `json:"offset"`
		Count  int    `json:"count"`
	}
	out := []letterBody{}
	for _, l := range letters {
		out = append(out, letterBody{l.Letter, l.Offset, l.Count})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "letters": out})
}

// handleHome is GET /home: the first screen in one response
// (docs/04-api.md, section 4.2).
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "limit" && k != "library" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameters are limit and library")
			return
		}
	}
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "limit is 1 to 200")
			return
		}
		limit = n
	}
	var library int64
	if v := q.Get("library"); v != "" {
		id, perr := strconv.ParseInt(v, 10, 64)
		_, gerr := s.db.Library(r.Context(), id)
		switch {
		case perr != nil || errors.Is(gerr, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return
		case gerr != nil:
			s.internal(w, r, gerr)
			return
		}
		library = id
	}
	// Recently added begins at the start of a day, so that the ETag can
	// carry it and moves when the day does.
	now := s.now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -recentDays)
	if !s.etag(w, r, "-"+day.Format("20060102")) {
		return
	}
	home, err := s.db.HomeOf(r.Context(), callerOf(r.Context()).user.ID, library, day.UnixMilli(), limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var all []db.Card
	for _, row := range [][]db.Card{home.Continue, home.NextUp, home.Recent, home.List} {
		all = append(all, row...)
	}
	mine, err := s.personalOf(r.Context(), all)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	row := func(cards []db.Card) []cardBody {
		out := []cardBody{}
		for _, c := range cards {
			b := cardJSON(c, mine)
			if n, ok := home.NewEpisodes[c.ID]; ok {
				b.NewEpisodes = &n
			}
			out = append(out, b)
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{"continue": row(home.Continue), "next_up": row(home.NextUp),
		"recent": row(home.Recent), "list": row(home.List)})
}

var searchTypes = map[string]bool{"movie": true, "series": true, "season": true, "episode": true, "collection": true}

// handleSearch is GET /search (docs/04-api.md, section 4.5).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "q" && k != "types" && k != "limit" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameters are q, types and limit")
			return
		}
	}
	text := strings.TrimSpace(q.Get("q"))
	if text == "" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "q is needed")
		return
	}
	types := []string{"movie", "series"}
	if v := q.Get("types"); v != "" {
		types = nil
		for _, t := range strings.Split(v, ",") {
			if !searchTypes[t] {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "types are movie, series, season, episode and collection")
				return
			}
			types = append(types, t)
		}
	}
	limit := 30
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "limit is 1 to 100")
			return
		}
		limit = n
	}
	cards, err := s.db.Search(r.Context(), text, types, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	mine, err := s.personalOf(r.Context(), cards)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []cardBody{}
	for _, c := range cards {
		out = append(out, cardJSON(c, mine))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleGenres is GET /genres (docs/04-api.md, section 4.6).
func (s *Server) handleGenres(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "library" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameter is library")
			return
		}
	}
	var library int64
	if v := q.Get("library"); v != "" {
		id, perr := strconv.ParseInt(v, 10, 64)
		_, gerr := s.db.Library(r.Context(), id)
		switch {
		case perr != nil || errors.Is(gerr, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such library")
			return
		case gerr != nil:
			s.internal(w, r, gerr)
			return
		}
		library = id
	}
	genres, err := s.db.Genres(r.Context(), library)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type genreBody struct {
		Name      string  `json:"name"`
		Items     int     `json:"items"`
		Libraries []int64 `json:"libraries"`
	}
	out := []genreBody{}
	for _, g := range genres {
		out = append(out, genreBody{g.Name, g.Items, g.Libraries})
	}
	writeJSON(w, http.StatusOK, map[string]any{"genres": out})
}
