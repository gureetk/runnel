// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"runtime/debug"
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
	// Warm starts one as Start does, and holds up to limit bytes of its
	// output for the request that claims it.
	Warm(ctx context.Context, src play.Source, t play.Target, limit int) (*play.Warm, error)
	// Running is how many transcodes hold a slot now, warm-ups too.
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
	q := r.URL.Query()
	src, t, ok := s.transcodeRequest(w, r, id, q)
	if !ok {
		s.handOver(c.tokenID, id)
		return
	}

	// The token's warm-up, when the ticket matches; otherwise one of the
	// same file is of no more use.
	var child *play.Child
	warmed := false
	if tk := q.Get("ticket"); tk != "" {
		if wu := s.claim(c.tokenID, tk, id, t); wu != nil {
			child, warmed = wu.Claim(), true
		}
	}
	s.handOver(c.tokenID, id)
	if child == nil {
		child, err = s.transcodes.Start(r.Context(), src, t)
		// A playback comes before a warm-up: one that holds the slot goes.
		if errors.Is(err, play.ErrNoSlot) && s.dropTranscodeWarmups() {
			child, err = s.transcodes.Start(r.Context(), src, t)
		}
	}
	if s.startFailed(w, r, id, t, err) {
		return
	}
	defer child.Stop()
	// The client's going ends the stream at once, wherever it is.
	stopWhenGone := context.AfterFunc(r.Context(), child.Stop)
	defer stopWhenGone()

	st := &transcodeStream{key: sessionKey{user: c.user.ID, id: q.Get("session")}, tokenID: c.tokenID, fileID: id,
		target: t, encoder: child.Encoder, warmed: warmed, started: arrived}
	st.firstByteMS.Store(-1)
	s.streams.add(st, s.now().UnixMilli())
	defer func() { s.streams.end(st, s.now().UnixMilli()) }()

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "video/x-matroska")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Runnel-Encoder", child.Encoder)
	if warmed {
		h.Set("X-Runnel-Warmed", "1")
	} else {
		h.Set("X-Runnel-Warmed", "0")
	}
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
	if warmed {
		// The warm-up's buffer was megabytes: give them back
		// (docs/02-architecture.md, section 8).
		defer debug.FreeOSMemory()
	}
	s.log.Debug("transcode ended", "file", id, "encoder", child.Encoder, "warmed", warmed,
		"client_went", err != nil || r.Context().Err() != nil)
}

// transcodeRequest finds a transcode's file and reads its parameters,
// for GET /files/{id}/transcode.mkv and a transcode's prepare. It answers
// the request itself, and returns false, when the transcode cannot be.
func (s *Server) transcodeRequest(w http.ResponseWriter, r *http.Request, id int64, q url.Values) (play.Source, play.Target, bool) {
	f, err := s.db.RawFileOf(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrOutsideRoot):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return play.Source{}, play.Target{}, false
	case err != nil:
		s.internal(w, r, err)
		return play.Source{}, play.Target{}, false
	}
	d, err := s.db.FileDetailOf(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file, or it is not offered for playback")
		return play.Source{}, play.Target{}, false
	case err != nil:
		s.internal(w, r, err)
		return play.Source{}, play.Target{}, false
	}
	src := play.Source{Path: f.Path, Attachments: d.AttachmentsBytes}
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
	t, bad := transcodeTarget(q, src, audio)
	switch hw, sw := s.lastEncoders().Can(t.V, t.Depth); {
	case bad != "":
		s.fail(w, r, http.StatusBadRequest, "bad_request", bad)
	case !hasVideo:
		s.unsupported(w, "the file has no video to encode")
	case s.transcodes == nil || !hw && !sw:
		s.unsupported(w, "this server cannot encode "+t.V+" at "+strconv.Itoa(t.Depth)+" bits")
	case !f.Available:
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file is missing or its disk is away")
	default:
		return src, t, true
	}
	return play.Source{}, play.Target{}, false
}

// startFailed answers a transcode, or a warm-up, that could not start, and
// reports whether there was such an error.
func (s *Server) startFailed(w http.ResponseWriter, r *http.Request, id int64, t play.Target, err error) bool {
	var failed *play.FailedError
	switch {
	case err == nil:
		return false
	case errors.Is(err, play.ErrNoSlot):
		w.Header().Set("Retry-After", busyRetry)
		s.fail(w, r, http.StatusServiceUnavailable, "busy", "every transcode slot is in use")
	case errors.Is(err, play.ErrUnsupported):
		s.unsupported(w, "this server cannot encode "+t.V+" at "+strconv.Itoa(t.Depth)+" bits")
	case errors.Is(err, play.ErrClosed):
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the server is stopping")
	case errors.As(err, &failed):
		s.log.Warn("a transcode could not start", "file", id, "v", t.V, "depth", t.Depth, "err", failed.Message)
		s.fail(w, r, http.StatusInternalServerError, "encode_failed", failed.Message)
	case r.Context().Err() != nil:
		// The client went before the first output.
	default:
		s.internal(w, r, err)
	}
	return true
}
