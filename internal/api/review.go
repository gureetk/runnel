// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta"
	"github.com/gureetk/runnel/internal/meta/tmdb"
)

// Matches searches the provider for an item's candidates
// (docs/06-library-and-metadata.md, section 6.2).
type Matches interface {
	Search(ctx context.Context, typ, title string, year int, age time.Duration) ([]meta.Candidate, error)
}

// candidatesAge is how old a search's answer may be for the dialog: as
// the matcher's, so that the dialog shows what the matcher saw.
const candidatesAge = meta.Every

// posterSize is the size of TMDB's poster that a candidate links to.
const posterSize = "w154"

type reviewItemBody struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"`
	LibraryID   int64           `json:"library_id"`
	Library     string          `json:"library"`
	Title       string          `json:"title"`
	Year        *int            `json:"year"`
	Path        string          `json:"path"`
	State       string          `json:"state"`
	Confidence  *float64        `json:"confidence"`
	Why         json.RawMessage `json:"why"`
	SeriesID    int64           `json:"series_id,omitempty"`
	SeriesTitle string          `json:"series_title,omitempty"`
	Season      *int            `json:"season,omitempty"`
}

type reviewCursor struct {
	L int64  `json:"l"`
	S string `json:"s"`
	I int64  `json:"i"`
}

// why is what the review list says of why an item is on it: the note
// without what only the matcher reads, or the reason a note cannot give.
func why(it db.ReviewItem) json.RawMessage {
	if it.Type == "episode" {
		return json.RawMessage(`{"reason":"unnumbered"}`)
	}
	var n map[string]any
	if it.Note == "" || json.Unmarshal([]byte(it.Note), &n) != nil {
		return json.RawMessage(`{"reason":"not_searched"}`)
	}
	delete(n, "title")
	delete(n, "year")
	delete(n, "at")
	b, _ := json.Marshal(n)
	return b
}

// handleReview is GET /admin/review (docs/04-api.md, section 8.4).
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	unmatched, review := true, true
	switch q.Get("state") {
	case "":
	case "unmatched":
		review = false
	case "review":
		unmatched = false
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "state is unmatched or review")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "limit is 1 to 200")
			return
		}
		limit = n
	}
	var after *db.ReviewAfter
	if c := q.Get("cursor"); c != "" {
		var rc reviewCursor
		if !decodeCursor(c, &rc) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
			return
		}
		after = &db.ReviewAfter{Library: rc.L, Sort: rc.S, ID: rc.I}
	}
	items, err := s.db.Review(r.Context(), unmatched, review, after, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	nu, nr, err := s.db.ReviewCounts(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		c := encodeCursor(reviewCursor{L: last.LibraryID, S: last.SortTitle, I: last.ID})
		next = &c
	}
	body := struct {
		Items  []reviewItemBody `json:"items"`
		Counts map[string]int   `json:"counts"`
		Next   *string          `json:"next"`
	}{Items: []reviewItemBody{}, Counts: map[string]int{"unmatched": nu, "review": nr}, Next: next}
	for _, it := range items {
		b := reviewItemBody{ID: it.ID, Type: it.Type, LibraryID: it.LibraryID, Library: it.Library, Title: it.Title,
			Path: it.Path, State: it.State, Why: why(it), SeriesID: it.SeriesID, SeriesTitle: it.SeriesTitle}
		if it.Year > 0 {
			b.Year = &it.Year
		}
		if it.Confidence > 0 && it.Type != "episode" {
			b.Confidence = &it.Confidence
		}
		if it.Type == "episode" {
			b.Season = &it.Season
		}
		body.Items = append(body.Items, b)
	}
	writeJSON(w, http.StatusOK, body)
}

type candidateBody struct {
	Provider      string  `json:"provider"`
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Year          *int    `json:"year"`
	Poster        *string `json:"poster"`
	Score         float64 `json:"score"`
}

// handleCandidates is GET /admin/items/{id}/candidates (docs/04-api.md,
// section 8.4): the provider searched for a film or a series, by its own
// title and year or by q and year, its answers scored as the matcher
// scores them. It waits for the provider.
func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
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
	if c.Type != "movie" && c.Type != "series" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "candidates are searched for a film or a series")
		return
	}
	q := r.URL.Query()
	if p := q.Get("provider"); p != "" && p != "tmdb" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "provider is tmdb")
		return
	}
	title, year := c.Title, c.Year
	if v := q.Get("q"); v != "" {
		title = v
		if len(title) > 200 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "q is at most 200 characters")
			return
		}
	}
	if q.Has("year") {
		year = 0
		if v := q.Get("year"); v != "" {
			if year, err = strconv.Atoi(v); err != nil || year < 1000 || year > 9999 {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "year is four digits, or empty for none")
				return
			}
		}
	}
	if !s.hasKey(w, r) {
		return
	}
	if s.matches == nil {
		s.fail(w, r, http.StatusBadGateway, "upstream", "this server has no metadata provider")
		return
	}
	cands, err := s.matches.Search(r.Context(), c.Type, title, year, candidatesAge)
	if err != nil {
		s.providerFailed(w, r, err)
		return
	}
	body := struct {
		Query      string          `json:"query"`
		Year       *int            `json:"year"`
		Candidates []candidateBody `json:"candidates"`
	}{Query: title, Candidates: []candidateBody{}}
	if year > 0 {
		body.Year = &year
	}
	for _, k := range cands {
		b := candidateBody{Provider: k.Provider, ID: k.ID, Title: k.Title, OriginalTitle: k.OriginalTitle, Score: k.Score}
		if k.Year > 0 {
			b.Year = &k.Year
		}
		if k.PosterPath != "" {
			u := tmdb.ImageBase + posterSize + k.PosterPath
			b.Poster = &u
		}
		body.Candidates = append(body.Candidates, b)
	}
	writeJSON(w, http.StatusOK, body)
}

// providerFailed answers a request that waited on the provider and did
// not get an answer it can use.
func (s *Server) providerFailed(w http.ResponseWriter, r *http.Request, err error) {
	var busy *tmdb.Busy
	var up *tmdb.UpstreamError
	switch {
	case errors.Is(err, tmdb.ErrNoKey):
		s.fail(w, r, http.StatusConflict, "no_key", "no metadata key is set: set one in the settings")
	case errors.Is(err, tmdb.ErrBadKey):
		s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB refuses the metadata key: set another in the settings")
	case errors.As(err, &busy):
		s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB has asked the server to wait; try again after "+busy.Until.UTC().Format(time.RFC3339))
	case errors.As(err, &up):
		s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB cannot be reached: "+up.Msg)
	case errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil:
		s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB did not answer in time")
	case r.Context().Err() == nil:
		s.internal(w, r, err)
	}
}

type matchRequest struct {
	Provider      string `json:"provider"`
	Value         string `json:"value"`
	SeasonOffset  int    `json:"season_offset"`
	EpisodeOffset int    `json:"episode_offset"`
	Numbering     string `json:"numbering"`
}

// handleMatch is PUT /admin/items/{id}/match (docs/04-api.md, section
// 8.4): a film or a series matched by hand to a TMDB id, or left
// unmatched with provider none, kept by its folder; an episode without a
// number left as it is with none. A match is fetched; leaving one
// unmatched builds the library again, so that it is titled from its
// names.
func (s *Server) handleMatch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "item")
	if !ok {
		return
	}
	if r.URL.Query().Get("dry_run") != "" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "the preview of a series' match is not built yet")
		return
	}
	var req matchRequest
	if !s.decode(w, r, &req) {
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
	switch req.Provider {
	case "none":
		req.Value = ""
	case "tmdb":
		if n, err := strconv.ParseUint(req.Value, 10, 32); err != nil || n == 0 || strconv.FormatUint(n, 10) != req.Value {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "value is TMDB's id, a whole number")
			return
		}
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "provider is tmdb or none")
		return
	}
	switch req.Numbering {
	case "", "seasons", "absolute":
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "numbering is seasons or absolute")
		return
	}
	if (req.SeasonOffset != 0 || req.EpisodeOffset != 0 || req.Numbering != "") && (c.Type != "series" || req.Provider == "none") {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "offsets and numbering are a series' match's")
		return
	}
	if req.SeasonOffset < -100 || req.SeasonOffset > 100 || req.EpisodeOffset < -10000 || req.EpisodeOffset > 10000 {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "an offset is out of range")
		return
	}
	err = s.db.SetOverride(r.Context(), id, db.Override{Provider: req.Provider, Value: req.Value, SeasonOffset: req.SeasonOffset,
		EpisodeOffset: req.EpisodeOffset, Numbering: req.Numbering}, s.now().UnixMilli())
	s.matchDone(w, r, id, c.LibraryID, err, req.Provider == "none", req.Provider == "tmdb")
}

// handleForgetMatch is DELETE /admin/items/{id}/match (docs/04-api.md,
// section 8.4): the match is forgotten, the item is unmatched, titled
// from its names again, and searched for.
func (s *Server) handleForgetMatch(w http.ResponseWriter, r *http.Request) {
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
	s.matchDone(w, r, id, c.LibraryID, s.db.ForgetMatch(r.Context(), id), true, true)
}

// matchDone answers a change of match with the item's match. With
// rebuild set the library is scanned and built again, so that what lost
// its provider's titles is titled from its names; with fetch set the item
// is fetched, or searched for, again.
func (s *Server) matchDone(w http.ResponseWriter, r *http.Request, id, libraryID int64, err error, rebuild, fetch bool) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such item")
		return
	case errors.Is(err, db.ErrNotMatchable):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a film or a series is matched; an episode without a number is only left as it is")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if rebuild && s.scans != nil {
		s.scans.Request(libraryID)
	}
	if fetch && s.fetches != nil {
		if err := s.fetches.Refresh(r.Context(), id, libraryID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	m, err := s.db.MatchOf(r.Context(), id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	body := matchBody{State: m.State}
	if m.Confidence != 0 {
		body.Confidence = &m.Confidence
	}
	writeJSON(w, http.StatusOK, struct {
		Match       matchBody         `json:"match"`
		ExternalIDs map[string]string `json:"external_ids"`
	}{body, m.ExternalIDs})
}
