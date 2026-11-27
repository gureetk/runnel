// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/gureetk/runnel/internal/db"
)

const (
	// writeEvery is how often a playing session's position is written
	// (docs/04-api.md, section 7.6).
	writeEvery = 10_000 // ms
	// sessionIdle is how long a session is remembered after its last
	// report.
	sessionIdle = 30 * 60_000 // ms
	// maxMarked is how many ids one request marks, and maxRestored how many
	// states one Undo writes (docs/04-api.md, section 7.7).
	maxMarked   = 1000
	maxRestored = 2000
)

// stateBody is a user's watch state of a film or an episode
// (docs/04-api.md, section 2.1).
type stateBody struct {
	PositionMS   int64  `json:"position_ms"`
	Played       bool   `json:"played"`
	PlayCount    int    `json:"play_count"`
	LastPlayedAt *int64 `json:"last_played_at"`
}

func stateJSON(s db.State) *stateBody {
	return &stateBody{PositionMS: s.PositionMS, Played: s.Played, PlayCount: s.PlayCount, LastPlayedAt: int64OrNil(s.LastPlayedAt)}
}

// personal is what belongs to the signed-in user on a set of cards: the
// state of films and episodes, the unplayed count of series and seasons,
// and when films and series went on My list.
type personal struct {
	states   map[int64]db.State
	unplayed map[int64]int
	listed   map[int64]int64
}

// personalOf reads the signed-in user's parts of some cards.
func (s *Server) personalOf(ctx context.Context, cards []db.Card) (personal, error) {
	user := callerOf(ctx).user.ID
	var items, series, seasons, listable []int64
	for _, c := range cards {
		switch c.Type {
		case "movie", "episode":
			items = append(items, c.ID)
		case "series":
			series = append(series, c.ID)
		case "season":
			seasons = append(seasons, c.ID)
		}
		if c.Type == "movie" || c.Type == "series" {
			listable = append(listable, c.ID)
		}
	}
	var p personal
	var err error
	if p.states, err = s.db.StatesOf(ctx, user, items); err != nil {
		return personal{}, err
	}
	if p.unplayed, err = s.db.Unplayed(ctx, user, series, seasons); err != nil {
		return personal{}, err
	}
	if p.listed, err = s.db.ListedOf(ctx, user, listable); err != nil {
		return personal{}, err
	}
	return p, nil
}

// state is the signed-in user's state of a film or an episode, also when
// it was never started.
func (p personal) state(id int64) *stateBody {
	if s, ok := p.states[id]; ok {
		return stateJSON(s)
	}
	return stateJSON(db.State{ItemID: id})
}

// sessions remembers, for each playback that reports progress, when its
// position was last written and whether its start was logged.
type sessions struct {
	mu        sync.Mutex
	by        map[sessionKey]*session
	lastSweep int64
}

type sessionKey struct {
	user int64
	id   string
}

type session struct {
	written int64 // when its position was last written; 0 never
	seen    int64 // its last report
	logged  bool  // its start is in play_log
}

// report decides what to do with one report of a session at now: whether
// to write its position, and whether to log its start, which happens once
// for the first report that carries timings or stats.
func (ss *sessions) report(key sessionKey, playing, timed bool, now int64) (write, log bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if now-ss.lastSweep > 60_000 {
		for k, v := range ss.by {
			if now-v.seen > sessionIdle {
				delete(ss.by, k)
			}
		}
		ss.lastSweep = now
	}
	se := ss.by[key]
	if se == nil {
		se = &session{}
		ss.by[key] = se
	}
	se.seen = now
	write = !playing || se.written == 0 || now-se.written >= writeEvery
	if write {
		se.written = now
	}
	log = timed && !se.logged
	if log {
		se.logged = true
	}
	return write, log
}

// end forgets a session that has stopped.
func (ss *sessions) end(key sessionKey) {
	ss.mu.Lock()
	delete(ss.by, key)
	ss.mu.Unlock()
}

type progressBody struct {
	Session    string `json:"session"`
	ItemID     *int64 `json:"item_id"`
	FileID     *int64 `json:"file_id"`
	PositionMS *int64 `json:"position_ms"`
	State      string `json:"state"`
	Mode       string `json:"mode"`
	AudioIdx   *int   `json:"audio_idx"`
	SubIdx     *int   `json:"sub_idx"`
	Timings    *struct {
		Loaded  *int64 `json:"pressed_to_loaded_ms"`
		Playing *int64 `json:"pressed_to_playing_ms"`
	} `json:"timings"`
	Stats *struct {
		Decoder       *string `json:"decoder"`
		Hardware      *bool   `json:"hardware"`
		DroppedFrames *int64  `json:"dropped_frames"`
		Warmed        *bool   `json:"warmed"`
	} `json:"stats"`
}

var progressModes = map[string]bool{"direct": true, "transcode": true, "sidecar": true, "remux": true}

func negative(ns ...*int64) bool {
	for _, n := range ns {
		if n != nil && *n < 0 {
			return true
		}
	}
	return false
}

// handleProgress is POST /progress (docs/04-api.md, section 7.6).
func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	var b progressBody
	if !s.decode(w, r, &b) {
		return
	}
	switch {
	case b.Session == "" || len(b.Session) > 64:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "session is 1 to 64 characters")
		return
	case b.ItemID == nil || b.FileID == nil || b.PositionMS == nil:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "item_id, file_id and position_ms are needed")
		return
	case *b.PositionMS < 0:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "position_ms is not negative")
		return
	case b.State != "playing" && b.State != "paused" && b.State != "stopped":
		s.fail(w, r, http.StatusBadRequest, "bad_request", `state is "playing", "paused" or "stopped"`)
		return
	case !progressModes[b.Mode]:
		s.fail(w, r, http.StatusBadRequest, "bad_request", `mode is "direct", "transcode", "sidecar" or "remux"`)
		return
	case b.Timings != nil && negative(b.Timings.Loaded, b.Timings.Playing),
		b.Stats != nil && negative(b.Stats.DroppedFrames):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "timings and counts are not negative")
		return
	}
	c := callerOf(r.Context())
	now := s.now().UnixMilli()
	key := sessionKey{user: c.user.ID, id: b.Session}
	write, logStart := s.sessions.report(key, b.State == "playing", b.Timings != nil || b.Stats != nil, now)
	if b.State == "stopped" {
		defer s.sessions.end(key)
	}
	if write {
		_, err := s.db.SaveProgress(r.Context(), c.user.ID, db.Progress{ItemID: *b.ItemID, FileID: *b.FileID,
			PositionMS: *b.PositionMS, Stopped: b.State == "stopped", At: now})
		switch {
		case errors.Is(err, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such item")
			return
		case errors.Is(err, db.ErrNoState):
			s.fail(w, r, http.StatusBadRequest, "bad_request", "progress is for films and episodes")
			return
		case errors.Is(err, db.ErrWrongFile):
			s.fail(w, r, http.StatusBadRequest, "bad_request", "file_id is not one of the item's files")
			return
		case err != nil:
			s.internal(w, r, err)
			return
		}
	}
	if logStart {
		start := db.PlayStart{At: now, UserID: c.user.ID, TokenID: c.tokenID, ItemID: *b.ItemID, FileID: *b.FileID, Mode: b.Mode}
		if t := b.Timings; t != nil {
			start.LoadedMS, start.PlayingMS = t.Loaded, t.Playing
		}
		if st := b.Stats; st != nil {
			if st.Decoder != nil {
				start.Decoder = *st.Decoder
			}
			start.Hardware, start.DroppedFrames = st.Hardware, st.DroppedFrames
			start.Warmed = st.Warmed != nil && *st.Warmed
		}
		// The server's side of a transcode's start: its first byte, its
		// encoder, and whether it claimed a warm-up.
		if t := s.streams.of(key); t != nil {
			if ms := t.firstByteMS.Load(); ms >= 0 {
				start.ServerFirstByteMS = &ms
			}
			start.Encoder = t.encoder
			start.Warmed = start.Warmed || t.warmed
		}
		if err := s.db.LogPlay(r.Context(), start); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

type previousEntry struct {
	ItemID int64 `json:"item_id"`
	stateBody
}

// marked answers a request that marked items: 204, or with
// ?return=previous 200 and the states replaced (docs/04-api.md,
// section 7.7).
func (s *Server) marked(w http.ResponseWriter, prev []db.State, wantPrevious bool) {
	if !wantPrevious {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out := make([]previousEntry, 0, len(prev))
	for _, p := range prev {
		out = append(out, previousEntry{ItemID: p.ItemID, stateBody: *stateJSON(p)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"previous": out})
}

// wantsPrevious reads ?return=, or answers 400.
func (s *Server) wantsPrevious(w http.ResponseWriter, r *http.Request) (want, ok bool) {
	switch v := r.URL.Query().Get("return"); v {
	case "":
		return false, true
	case "previous":
		return true, true
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", `return is "previous"`)
		return false, false
	}
}

// handlePlayed is PUT and DELETE /items/{id}/played.
func (s *Server) handlePlayed(played bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want, ok := s.wantsPrevious(w, r)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		_, gerr := s.db.Item(r.Context(), id)
		switch {
		case err != nil || errors.Is(gerr, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such item")
			return
		case gerr != nil:
			s.internal(w, r, gerr)
			return
		}
		prev, err := s.db.MarkPlayed(r.Context(), callerOf(r.Context()).user.ID, []int64{id}, played, s.now().UnixMilli())
		if err != nil {
			s.internal(w, r, err)
			return
		}
		s.marked(w, prev, want)
	}
}

// handlePlayedMany is PUT and DELETE /items/played.
func (s *Server) handlePlayedMany(played bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want, ok := s.wantsPrevious(w, r)
		if !ok {
			return
		}
		var b struct {
			IDs []int64 `json:"ids"`
		}
		if !s.decode(w, r, &b) {
			return
		}
		if len(b.IDs) > maxMarked {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "at most 1000 ids")
			return
		}
		prev, err := s.db.MarkPlayed(r.Context(), callerOf(r.Context()).user.ID, b.IDs, played, s.now().UnixMilli())
		if err != nil {
			s.internal(w, r, err)
			return
		}
		s.marked(w, prev, want)
	}
}

// handleRestore is PUT /items/state: Undo (docs/04-api.md, section 7.7).
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	var b struct {
		States []struct {
			ItemID       *int64 `json:"item_id"`
			PositionMS   int64  `json:"position_ms"`
			Played       bool   `json:"played"`
			PlayCount    int    `json:"play_count"`
			LastPlayedAt *int64 `json:"last_played_at"`
		} `json:"states"`
	}
	if !s.decodeUpTo(w, r, &b, 512<<10) {
		return
	}
	if len(b.States) > maxRestored {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "at most 2000 states")
		return
	}
	states := make([]db.State, 0, len(b.States))
	for _, e := range b.States {
		switch {
		case e.ItemID == nil:
			s.fail(w, r, http.StatusBadRequest, "bad_request", "every state has an item_id")
			return
		case e.PositionMS < 0 || e.PlayCount < 0 || negative(e.LastPlayedAt):
			s.fail(w, r, http.StatusBadRequest, "bad_request", "positions, counts and times are not negative")
			return
		case e.Played && e.PositionMS != 0:
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a played item has no position")
			return
		}
		st := db.State{ItemID: *e.ItemID, PositionMS: e.PositionMS, Played: e.Played, PlayCount: e.PlayCount}
		if e.LastPlayedAt != nil {
			st.LastPlayedAt = *e.LastPlayedAt
		}
		states = append(states, st)
	}
	err := s.db.RestoreStates(r.Context(), callerOf(r.Context()).user.ID, states)
	switch {
	case errors.Is(err, db.ErrNoState):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "watch state is for films and episodes")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// prefsBody is a track choice (docs/04-api.md, section 7.8).
type prefsBody struct {
	AudioLang *string `json:"audio_lang"`
	SubLang   *string `json:"sub_lang"`
	SubMode   *string `json:"sub_mode"`
}

func prefsJSON(p db.Prefs) prefsBody {
	return prefsBody{AudioLang: strOrNil(p.AudioLang), SubLang: strOrNil(p.SubLang), SubMode: strOrNil(p.SubMode)}
}

var subModes = map[string]bool{"off": true, "forced": true, "auto": true, "on": true}

// validLang reports whether a language is one a track can carry: at most
// 16 letters, digits and hyphens.
func validLang(l string) bool {
	if l == "" || len(l) > 16 {
		return false
	}
	for _, c := range l {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// readPrefs reads a track choice from a request, or answers 400.
func (s *Server) readPrefs(w http.ResponseWriter, r *http.Request) (db.Prefs, bool) {
	var b prefsBody
	if !s.decode(w, r, &b) {
		return db.Prefs{}, false
	}
	if b.SubMode == nil || !subModes[*b.SubMode] {
		s.fail(w, r, http.StatusBadRequest, "bad_request", `sub_mode is "off", "forced", "auto" or "on"`)
		return db.Prefs{}, false
	}
	p := db.Prefs{SubMode: *b.SubMode}
	for _, l := range []struct {
		in  *string
		out *string
	}{{b.AudioLang, &p.AudioLang}, {b.SubLang, &p.SubLang}} {
		if l.in == nil {
			continue
		}
		if !validLang(*l.in) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a language is at most 16 letters, digits and hyphens, or null")
			return db.Prefs{}, false
		}
		*l.out = *l.in
	}
	return p, true
}

// handleSeriesPrefs is PUT /series/{id}/prefs, and with reset DELETE.
func (s *Server) handleSeriesPrefs(reset bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var p db.Prefs
		if !reset {
			var ok bool
			if p, ok = s.readPrefs(w, r); !ok {
				return
			}
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		err := db.ErrNotFound
		if perr == nil {
			err = s.db.SetSeriesPrefs(r.Context(), callerOf(r.Context()).user.ID, id, p)
		}
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, db.ErrNotFound):
			s.fail(w, r, http.StatusNotFound, "not_found", "no such series")
		default:
			s.internal(w, r, err)
		}
	}
}

// handleSetMyPrefs is PUT /auth/me/prefs: the user's track defaults.
func (s *Server) handleSetMyPrefs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.readPrefs(w, r)
	if !ok {
		return
	}
	if err := s.db.SetDefaultPrefs(r.Context(), callerOf(r.Context()).user.ID, p); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type seriesPrefsBody struct {
	SeriesID int64  `json:"series_id"`
	Title    string `json:"title"`
	prefsBody
}

// handleMyPrefs is GET /auth/me/prefs: the defaults, and every series with
// a choice of its own.
func (s *Server) handleMyPrefs(w http.ResponseWriter, r *http.Request) {
	defaults, series, err := s.db.AllPrefs(r.Context(), callerOf(r.Context()).user.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]seriesPrefsBody, 0, len(series))
	for _, p := range series {
		out = append(out, seriesPrefsBody{SeriesID: p.SeriesID, Title: p.Title, prefsBody: prefsJSON(p.Prefs)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"defaults": prefsJSON(defaults), "series": out})
}
