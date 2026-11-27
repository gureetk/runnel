// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/gureetk/runnel/internal/play"
)

type personRef struct {
	ID   *int64 `json:"id"`
	Name string `json:"name"`
}

type deviceRef struct {
	Name   string `json:"name"`
	Client string `json:"client"`
}

type itemRef struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type transcodeBodyOfSession struct {
	// State is "warming", "held" or "streaming" (docs/05-playback.md,
	// section 5).
	State   string `json:"state"`
	V       string `json:"v"`
	Depth   int    `json:"depth"`
	Encoder string `json:"encoder"`
	// Memory is the ffmpeg child's resident memory, read every 2 s.
	Memory      int64  `json:"memory"`
	Warmed      bool   `json:"warmed"`
	FirstByteMS *int64 `json:"first_byte_ms"`
	Since       int64  `json:"since"`
}

type sessionBody struct {
	Session     string                  `json:"session"`
	User        *personRef              `json:"user"`
	Device      *deviceRef              `json:"device"`
	Item        *itemRef                `json:"item"`
	FileID      int64                   `json:"file_id"`
	State       *string                 `json:"state"`
	PositionMS  *int64                  `json:"position_ms"`
	Mode        string                  `json:"mode"`
	ReportedAt  *int64                  `json:"reported_at"`
	StartedInMS *int64                  `json:"started_in_ms"`
	Transcode   *transcodeBodyOfSession `json:"transcode"`
	since       int64
	tokenID     int64
	itemID      int64
}

// sessionRows gathers the playbacks in progress: those that report
// progress, the transcodes that run, and the warm-ups that wait. A
// transcode and the reports of its session are one row.
func (s *Server) sessionRows() []*sessionBody {
	var rows []*sessionBody
	byKey := map[sessionKey]*sessionBody{}
	for key, se := range s.sessions.snapshot() {
		if se.last.state == "stopped" {
			continue
		}
		st, pos, at := se.last.state, se.last.positionMS, se.seen
		row := &sessionBody{Session: key.id, FileID: se.last.fileID, State: &st, PositionMS: &pos, Mode: se.last.mode,
			ReportedAt: &at, StartedInMS: se.last.playingMS, since: se.since, tokenID: se.last.tokenID, itemID: se.last.itemID}
		rows = append(rows, row)
		byKey[key] = row
	}
	for _, st := range s.streams.list() {
		t := &transcodeBodyOfSession{State: "streaming", V: st.target.V, Depth: st.target.Depth, Encoder: st.encoder,
			Memory: st.child.RSS(), Warmed: st.warmed, Since: st.started.UnixMilli()}
		if ms := st.firstByteMS.Load(); ms >= 0 {
			t.FirstByteMS = &ms
		}
		row := byKey[st.key]
		if st.key.id == "" || row == nil {
			row = &sessionBody{Session: st.id, FileID: st.fileID, Mode: "transcode", since: st.started.UnixMilli(), tokenID: st.tokenID}
			rows = append(rows, row)
		}
		row.Transcode = t
	}
	s.warm.mu.Lock()
	for token, w := range s.warm.by {
		if w.warm == nil {
			continue
		}
		state := "warming"
		if w.warm.Held() {
			state = "held"
		}
		rows = append(rows, &sessionBody{Session: w.ticket, FileID: w.file, Mode: "transcode", since: w.since, tokenID: token,
			Transcode: &transcodeBodyOfSession{State: state, V: w.target.V, Depth: w.target.Depth, Encoder: w.warm.Child().Encoder,
				Memory: w.warm.Child().RSS(), Since: w.since}})
	}
	s.warm.mu.Unlock()
	// The oldest first, so that rows keep their place as the page refreshes.
	slices.SortFunc(rows, func(a, b *sessionBody) int {
		if c := cmp.Compare(a.since, b.since); c != 0 {
			return c
		}
		return cmp.Compare(a.Session, b.Session)
	})
	return rows
}

// handleSessions is GET /admin/sessions (docs/04-api.md, section 8.7).
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	rows := s.sessionRows()
	for _, row := range rows {
		p, err := s.db.PlayingOf(r.Context(), row.tokenID, row.itemID, row.FileID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if p.User != "" {
			row.User = &personRef{ID: int64OrNil(p.UserID), Name: p.User}
		}
		if p.Device != "" || p.Client != "" {
			row.Device = &deviceRef{Name: p.Device, Client: p.Client}
		}
		if p.ItemID != 0 {
			row.Item = &itemRef{ID: p.ItemID, Title: p.Title}
		}
	}
	if rows == nil {
		rows = []*sessionBody{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": rows})
}

// handleStopSession is DELETE /admin/sessions/{session}: the session's
// transcode, or its warm-up, ends, and ffmpeg with it. A direct play is
// the player's own reading of the file, which the server cannot stop.
func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session")
	var children []*play.Child
	for _, st := range s.streams.list() {
		if st.id == id {
			children = append(children, st.child)
		}
	}
	stopped := len(children) > 0
	for _, c := range children {
		c.Stop()
	}
	s.warm.mu.Lock()
	var warm []*warmup
	for token, wu := range s.warm.by {
		if wu.warm != nil && wu.ticket == id {
			warm = append(warm, wu)
			delete(s.warm.by, token)
		}
	}
	s.warm.mu.Unlock()
	for _, wu := range warm {
		wu.stop()
		stopped = true
	}
	if stopped {
		s.log.Info("an admin stopped a playback", "session", id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for key, se := range s.sessions.snapshot() {
		if key.id == id && se.last.state != "stopped" {
			s.fail(w, r, http.StatusConflict, "conflict", "this session plays the file as it is, which the server cannot stop")
			return
		}
	}
	s.fail(w, r, http.StatusNotFound, "not_found", "no such session")
}
