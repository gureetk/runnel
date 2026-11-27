// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"net/http"
	"os"
	"strconv"

	"github.com/gureetk/runnel/internal/db"
)

type playlogCursor struct {
	I int64 `json:"i"`
}

// handlePlaylog is GET /admin/playlog: the start-time log, newest first
// (docs/04-api.md, section 8.7).
func (s *Server) handlePlaylog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "limit" && k != "cursor" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameters are limit and cursor")
			return
		}
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > db.PlayLogKeep {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	var c playlogCursor
	if v := q.Get("cursor"); v != "" && (!decodeCursor(v, &c) || c.I <= 0) {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "the cursor is not one this server gave")
		return
	}
	rows, err := s.db.PlayLog(r.Context(), c.I, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type userRef struct {
		ID   *int64 `json:"id"` // null once the person is removed
		Name string `json:"name"`
	}
	type rowBody struct {
		ID                int64    `json:"id"`
		At                int64    `json:"at"`
		User              *userRef `json:"user"`
		Device            *string  `json:"device"`
		Client            *string  `json:"client"`
		ItemID            *int64   `json:"item_id"`
		Title             *string  `json:"title"`
		Mode              string   `json:"mode"`
		Warmed            bool     `json:"warmed"`
		ServerFirstByteMS *int64   `json:"server_first_byte_ms"`
		ClientLoadedMS    *int64   `json:"client_loaded_ms"`
		ClientPlayingMS   *int64   `json:"client_playing_ms"`
		Decoder           *string  `json:"decoder"`
		Hardware          *bool    `json:"hardware"`
		DroppedFrames     *int64   `json:"dropped_frames"`
		Encoder           *string  `json:"encoder"`
	}
	body := struct {
		Rows []rowBody `json:"rows"`
		Next *string   `json:"next"`
	}{Rows: []rowBody{}}
	if len(rows) > limit {
		n := encodeCursor(playlogCursor{I: rows[limit-1].ID})
		body.Next = &n
		rows = rows[:limit]
	}
	for _, p := range rows {
		b := rowBody{ID: p.ID, At: p.At, Device: strOrNil(p.Device), Client: strOrNil(p.Client), ItemID: int64OrNil(p.ItemID),
			Title: strOrNil(p.Title), Mode: p.Mode, Warmed: p.Warmed, ServerFirstByteMS: p.ServerFirstByteMS,
			ClientLoadedMS: p.LoadedMS, ClientPlayingMS: p.PlayingMS, Decoder: strOrNil(p.Decoder), Hardware: p.Hardware,
			DroppedFrames: p.DroppedFrames, Encoder: strOrNil(p.Encoder)}
		if p.UserID != 0 || p.User != "" {
			b.User = &userRef{ID: int64OrNil(p.UserID), Name: p.User}
		}
		body.Rows = append(body.Rows, b)
	}
	writeJSON(w, http.StatusOK, body)
}

// residentBytes is the process's resident memory, from /proc/self/statm,
// or 0 where that cannot be read.
func residentBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	f := bytes.Fields(b)
	if len(f) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(string(f[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}

// handleStats is GET /admin/stats (docs/04-api.md, section 8.7). The
// limits and slots of transcoding arrive with stage 3, and are left out.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.db.StatsOf(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    s.info.Version,
		"started_at": s.startedAt.UnixMilli(),
		"memory":     map[string]int64{"rss": residentBytes()},
		"database":   map[string]int64{"size": st.Size, "seq": st.Seq},
		"counts": map[string]int{"libraries": st.Libraries, "movies": st.Movies, "series": st.Series, "episodes": st.Episodes,
			"files": st.Files, "users": st.Users},
	})
}
