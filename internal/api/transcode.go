// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/play"
)

// Transcodes runs the ffmpeg children of transcodes.
type Transcodes interface {
	// Start takes a slot and starts ffmpeg on the first rung of the ladder
	// that produces output (play.Transcoder).
	Start(ctx context.Context, src play.Source, t play.Target) (*play.Child, error)
	// Running is how many transcodes hold a slot now.
	Running() int
}

// busyRetry is the Retry-After of a transcode refused for want of a slot.
const busyRetry = "5"

// transcodeStream is one transcode as it runs, and after, for the
// start-time log.
type transcodeStream struct {
	key     sessionKey // the user and the client's session, which may be empty
	tokenID int64
	fileID  int64
	target  play.Target
	encoder string
	warmed  bool
	started time.Time
	// firstByteMS is the time from the request to its first byte sent;
	// -1 until then.
	firstByteMS atomic.Int64
	ended       atomic.Int64 // unix ms; 0 while it runs
}

// transcodeStreams are the transcodes that run, and the last one of each
// session, kept while its session is (sessionIdle), so that the session's
// first timed report can log the server's side of its start.
type transcodeStreams struct {
	mu      sync.Mutex
	running map[*transcodeStream]bool
	last    map[sessionKey]*transcodeStream
}

func (ss *transcodeStreams) add(st *transcodeStream, now int64) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for k, v := range ss.last {
		if e := v.ended.Load(); e != 0 && now-e > sessionIdle {
			delete(ss.last, k)
		}
	}
	ss.running[st] = true
	if st.key.id != "" {
		ss.last[st.key] = st
	}
}

func (ss *transcodeStreams) end(st *transcodeStream, now int64) {
	st.ended.Store(now)
	ss.mu.Lock()
	delete(ss.running, st)
	ss.mu.Unlock()
}

// of is the last stream of a session, or nil.
func (ss *transcodeStreams) of(key sessionKey) *transcodeStream {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.last[key]
}

// transcodeTarget reads a transcode's parameters (docs/04-api.md, section
// 7.3) and gives them their defaults. A message means a bad parameter.
func transcodeTarget(q url.Values, video play.Source, audio map[int]bool) (play.Target, string) {
	t := play.Target{Audio: play.AllAudio, Subs: true, Fonts: true}
	switch t.V = q.Get("v"); t.V {
	case "hevc", "h264":
	case "":
		return t, "v is needed: hevc or h264"
	default:
		return t, "v is hevc or h264"
	}
	switch q.Get("depth") {
	case "":
		t.Depth = 8
		if t.V == "hevc" && video.Depth >= 10 {
			t.Depth = 10
		}
	case "8":
		t.Depth = 8
	case "10":
		t.Depth = 10
	default:
		return t, "depth is 8 or 10"
	}
	if v := q.Get("max_h"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 || n > 8640 {
			return t, "max_h is a height from 2 to 8640"
		}
		t.MaxH = n &^ 1 // the encoders take an even height
	}
	if v := q.Get("start_ms"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return t, "start_ms is a number of milliseconds, not negative"
		}
		t.StartMS = n
	}
	switch q.Get("seek") {
	case "", "key":
	case "exact":
		t.Exact = true
	default:
		return t, "seek is key or exact"
	}
	switch v := q.Get("audio"); v {
	case "", "all":
	case "none":
		t.Audio = play.NoAudio
	default:
		n, err := strconv.Atoi(v)
		if err != nil || !audio[n] {
			return t, "audio is all, none, or the index of one of the file's audio streams"
		}
		t.Audio = n
	}
	switch q.Get("subs") {
	case "", "all":
	case "none":
		t.Subs = false
	default:
		return t, "subs is all or none"
	}
	switch q.Get("fonts") {
	case "", "1":
	case "0":
		t.Fonts = false
	default:
		return t, "fonts is 1 or 0"
	}
	if len(q.Get("session")) > 64 {
		return t, "session is at most 64 characters"
	}
	return t, ""
}

// unsupported answers 409 for a target this server cannot encode, with
// what it can (docs/04-api.md, section 7.3).
func (s *Server) unsupported(w http.ResponseWriter, message string) {
	video, depth10 := s.lastEncoders().Video()
	var b struct {
		errorBody
		Supported struct {
			Video   []string `json:"video"`
			Depth10 []string `json:"depth10"`
		} `json:"supported"`
	}
	b.Error.Code, b.Error.Message = "unsupported", message
	b.Supported.Video, b.Supported.Depth10 = video, depth10
	writeJSON(w, http.StatusConflict, b)
}

// handleTranscode is GET /files/{id}/transcode.mkv: the file with its
// video encoded again and everything else copied, as ffmpeg writes it
// (docs/04-api.md, section 7.3; docs/05-playback.md, section 4). It has no
// write deadline, and ffmpeg is killed the moment the client goes.
//
// Like a raw request, it holds the playback gate from its arrival until
// firstByteHold after its first byte.
func (s *Server) handleTranscode(w http.ResponseWriter, r *http.Request) {
	arrived := time.Now()
	release := s.gate.Hold()
	var sending bool
	defer func() {
		if !sending {
			release()
		}
	}()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	}
	c := callerOf(r.Context())
	s.handOver(c.tokenID, id)
	f, err := s.db.RawFileOf(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrOutsideRoot):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	d, err := s.db.FileDetailOf(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file, or it is not offered for playback")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	src := play.Source{Path: f.Path}
	audio := map[int]bool{}
	hasVideo := false
	for _, st := range d.Streams {
		switch {
		case st.Kind == "video" && !hasVideo:
			hasVideo = true
			src.Codec, src.Profile, src.Depth, src.Height = st.Codec, st.Profile, st.BitDepth, st.Height
		case st.Kind == "audio":
			audio[st.Index] = true
		}
	}
	q := r.URL.Query()
	t, bad := transcodeTarget(q, src, audio)
	if bad != "" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", bad)
		return
	}
	if !hasVideo {
		s.unsupported(w, "the file has no video to encode")
		return
	}
	if hw, sw := s.lastEncoders().Can(t.V, t.Depth); s.transcodes == nil || !hw && !sw {
		s.unsupported(w, "this server cannot encode "+t.V+" at "+strconv.Itoa(t.Depth)+" bits")
		return
	}
	if !f.Available {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file is missing or its disk is away")
		return
	}

	child, err := s.transcodes.Start(r.Context(), src, t)
	var failed *play.FailedError
	switch {
	case errors.Is(err, play.ErrNoSlot):
		w.Header().Set("Retry-After", busyRetry)
		s.fail(w, r, http.StatusServiceUnavailable, "busy", "every transcode slot is in use")
		return
	case errors.Is(err, play.ErrUnsupported):
		s.unsupported(w, "this server cannot encode "+t.V+" at "+strconv.Itoa(t.Depth)+" bits")
		return
	case errors.Is(err, play.ErrClosed):
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the server is stopping")
		return
	case errors.As(err, &failed):
		s.log.Warn("a transcode could not start", "file", id, "v", t.V, "depth", t.Depth, "err", failed.Message)
		s.fail(w, r, http.StatusInternalServerError, "encode_failed", failed.Message)
		return
	case r.Context().Err() != nil:
		return // the client went before the first output
	case err != nil:
		s.internal(w, r, err)
		return
	}
	defer child.Stop()
	// The client's going ends the stream at once, wherever it is.
	stopWhenGone := context.AfterFunc(r.Context(), child.Stop)
	defer stopWhenGone()

	st := &transcodeStream{key: sessionKey{user: c.user.ID, id: q.Get("session")}, tokenID: c.tokenID, fileID: id,
		target: t, encoder: child.Encoder, started: arrived}
	st.firstByteMS.Store(-1)
	s.streams.add(st, s.now().UnixMilli())
	defer func() { s.streams.end(st, s.now().UnixMilli()) }()

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "video/x-matroska")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Runnel-Encoder", child.Encoder)
	h.Set("X-Runnel-Warmed", "0")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	first := true
	err = child.Relay(w, func() {
		_ = rc.Flush()
		if first {
			first = false
			st.firstByteMS.Store(time.Since(arrived).Milliseconds())
			sending = true
			time.AfterFunc(s.firstByteHold, release)
		}
	})
	s.log.Debug("transcode ended", "file", id, "encoder", child.Encoder, "client_went", err != nil || r.Context().Err() != nil)
}
