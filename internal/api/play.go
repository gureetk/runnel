// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/play"
)

// Gate is the playback gate. A request that starts a playback holds it,
// and the background queue takes no new work while it is held
// (docs/02-architecture.md, section 5).
type Gate interface {
	Hold() (release func())
	// Open reports whether no playback is starting.
	Open() bool
}

type noGate struct{}

func (noGate) Hold() func() { return func() {} }
func (noGate) Open() bool   { return true }

const (
	// firstByteHold is how long after a response's first byte its
	// playback still counts as starting.
	firstByteHold = 10 * time.Second
	// prepareHold is how long a prepare holds the gate when no playback
	// of its file follows: as long as a transcode's ticket lasts
	// (docs/04-api.md, section 7.5).
	prepareHold = 30 * time.Second
	// warmHead and warmTail are what a warm-up for direct play reads
	// (docs/05-playback.md, section 3).
	warmHead = 4 << 20
	warmTail = 1 << 20
)

// warmup is the one warm-up of a token: a direct play's, which reads the
// file's head and tail, or a transcode's, which holds ffmpeg's first
// output for the request that brings its ticket.
type warmup struct {
	file    int64
	cancel  context.CancelFunc
	release func() // the gate's hold
	// A transcode's.
	ticket string
	target play.Target
	warm   *play.Warm
}

// stop ends a warm-up that nothing claimed: a transcode's ffmpeg is
// killed.
func (w *warmup) stop() {
	w.cancel()
	w.release()
	if w.warm != nil {
		w.warm.Stop()
		// Its buffer was megabytes: give them back (docs/02-architecture.md,
		// section 8).
		go debug.FreeOSMemory()
	}
}

type warmups struct {
	mu sync.Mutex
	by map[int64]*warmup // token id -> its warm-up
}

// startWarmup holds the gate from now until the file's playback starts, a
// newer prepare of the same token replaces this warm-up, or prepareHold
// passes, when the warm-up ends. A direct play's reads the file's head and
// tail in the background (path); a transcode's holds warm.
func (s *Server) startWarmup(tokenID, fileID int64, path string, ticket string, target play.Target, warm *play.Warm) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &warmup{file: fileID, cancel: cancel, release: s.gate.Hold(), ticket: ticket, target: target, warm: warm}
	s.warm.mu.Lock()
	old := s.warm.by[tokenID]
	s.warm.by[tokenID] = w
	s.warm.mu.Unlock()
	if old != nil {
		old.stop()
	}
	go func() {
		t := time.NewTimer(s.prepareHold)
		defer t.Stop()
		if warm == nil {
			readHeadAndTail(ctx, path)
		}
		select {
		case <-ctx.Done():
			return // handed over or replaced, by whoever did that
		case <-t.C:
		}
		s.warm.mu.Lock()
		mine := s.warm.by[tokenID] == w
		if mine {
			delete(s.warm.by, tokenID)
		}
		s.warm.mu.Unlock()
		if mine {
			w.stop()
		}
	}()
}

// handOver ends a token's warm-up of a file whose playback has started,
// which holds the gate itself from here. A transcode's warm-up that the
// playback did not claim is of no more use, and goes too.
func (s *Server) handOver(tokenID, fileID int64) {
	s.warm.mu.Lock()
	w := s.warm.by[tokenID]
	if w == nil || w.file != fileID {
		s.warm.mu.Unlock()
		return
	}
	delete(s.warm.by, tokenID)
	s.warm.mu.Unlock()
	w.stop()
}

// claim hands a transcode its token's warm-up when the ticket, the file
// and the target all match, or returns nil (docs/04-api.md, section 7.5).
// The playback holds the gate itself from here.
func (s *Server) claim(tokenID int64, ticket string, fileID int64, target play.Target) *play.Warm {
	s.warm.mu.Lock()
	w := s.warm.by[tokenID]
	if w == nil || w.warm == nil || w.ticket != ticket || w.file != fileID || w.target != target {
		s.warm.mu.Unlock()
		return nil
	}
	delete(s.warm.by, tokenID)
	s.warm.mu.Unlock()
	w.cancel()
	w.release()
	return w.warm
}

// dropTranscodeWarmups ends every transcode's warm-up: one is held at a
// time (docs/05-playback.md, section 4.4), and a transcode that wants a
// slot comes before it. It reports whether it ended any.
func (s *Server) dropTranscodeWarmups() bool {
	s.warm.mu.Lock()
	var drop []*warmup
	for token, w := range s.warm.by {
		if w.warm != nil {
			drop = append(drop, w)
			delete(s.warm.by, token)
		}
	}
	s.warm.mu.Unlock()
	for _, w := range drop {
		w.stop()
	}
	return len(drop) > 0
}

// readHeadAndTail reads the first warmHead and the last warmTail bytes of
// a file and drops them. That brings the header, the attachments and
// usually the index into the page cache, and wakes a disk that sleeps.
func readHeadAndTail(ctx context.Context, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	buf := make([]byte, 64<<10)
	read := func(off, n int64) {
		for n > 0 && ctx.Err() == nil {
			k, err := f.ReadAt(buf[:min(n, int64(len(buf)))], off)
			if err != nil {
				return
			}
			off, n = off+int64(k), n-int64(k)
		}
	}
	read(0, min(warmHead, fi.Size()))
	if tail := max(warmHead, fi.Size()-warmTail); tail < fi.Size() {
		read(tail, fi.Size()-tail)
	}
}

type prepareRequest struct {
	Mode    string `json:"mode"`
	StartMS *int64 `json:"start_ms"`
	// A transcode's parameters, as GET /files/{id}/transcode.mkv takes
	// them (docs/04-api.md, section 7.3).
	V     string          `json:"v"`
	Depth *int            `json:"depth"`
	MaxH  *int            `json:"max_h"`
	Seek  string          `json:"seek"`
	Audio json.RawMessage `json:"audio"`
	Subs  string          `json:"subs"`
	Fonts *int            `json:"fonts"`
}

// query is a transcode prepare's parameters as a transcode request has
// them, so that both are read and given their defaults the same way. A
// message means a parameter that is not of its type.
func (p prepareRequest) query() (url.Values, string) {
	q := url.Values{}
	set := func(k string, n *int) {
		if n != nil {
			q.Set(k, strconv.Itoa(*n))
		}
	}
	q.Set("v", p.V)
	set("depth", p.Depth)
	set("max_h", p.MaxH)
	set("fonts", p.Fonts)
	if p.StartMS != nil {
		q.Set("start_ms", strconv.FormatInt(*p.StartMS, 10))
	}
	for k, v := range map[string]string{"seek": p.Seek, "subs": p.Subs} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if len(p.Audio) > 0 {
		var name string
		var index int
		switch {
		case json.Unmarshal(p.Audio, &name) == nil:
			q.Set("audio", name)
		case json.Unmarshal(p.Audio, &index) == nil:
			q.Set("audio", strconv.Itoa(index))
		default:
			return nil, `audio is "all", "none" or a stream index`
		}
	}
	return q, ""
}

// ticketTTL is how long a transcode's warm-up waits for its request, in
// the answer's expires_in (docs/04-api.md, section 7.5).
const ticketTTL = 30

// newTicket is a warm-up's ticket: p_ and 48 random bits in hex.
func newTicket() string {
	var b [6]byte
	rand.Read(b[:])
	return "p_" + hex.EncodeToString(b[:])
}

// handlePrepare is POST /files/{id}/prepare (docs/04-api.md, section 7.5).
// For direct play it answers at once and reads the file's head and tail in
// the background. For a transcode it starts ffmpeg, holds its first output
// for the request that brings the ticket, and answers once that output
// has come.
func (s *Server) handlePrepare(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	}
	var req prepareRequest
	if !s.decode(w, r, &req) {
		return
	}
	switch req.Mode {
	case "direct":
	case "transcode":
		s.prepareTranscode(w, r, id, req)
		return
	default:
		s.fail(w, r, http.StatusBadRequest, "bad_request", `mode is "direct" or "transcode"`)
		return
	}
	f, err := s.db.RawFileOf(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrOutsideRoot):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if !f.Available {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file is missing or its disk is away")
		return
	}
	s.startWarmup(callerOf(r.Context()).tokenID, id, f.Path, "", play.Target{}, nil)
	w.WriteHeader(http.StatusNoContent)
}

// prepareTranscode warms a transcode (docs/05-playback.md, section 6). The
// token's warm-up before it goes, and so does any other transcode's
// warm-up: one is held at a time.
func (s *Server) prepareTranscode(w http.ResponseWriter, r *http.Request, id int64, req prepareRequest) {
	q, bad := req.query()
	if bad != "" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", bad)
		return
	}
	src, t, ok := s.transcodeRequest(w, r, id, q)
	if !ok {
		return
	}
	// The prepare holds the gate from its arrival; its warm-up holds it
	// from here as a direct play's does.
	hold := s.gate.Hold()
	defer hold()
	token := callerOf(r.Context()).tokenID
	s.dropWarmup(token)
	s.dropTranscodeWarmups()
	// What comes before the first video packet, and 4 MiB of video.
	warm, err := s.transcodes.Warm(r.Context(), src, t, int(src.Attachments)+play.WarmExtra)
	if s.startFailed(w, r, id, t, err) {
		return
	}
	ticket := newTicket()
	s.startWarmup(token, id, "", ticket, t, warm)
	writeJSON(w, http.StatusOK, map[string]any{"ticket": ticket, "expires_in": ticketTTL, "sidecar": map[string]bool{"ready": false}})
}

// dropWarmup ends a token's warm-up, whatever its file.
func (s *Server) dropWarmup(token int64) {
	s.warm.mu.Lock()
	w := s.warm.by[token]
	delete(s.warm.by, token)
	s.warm.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

// gateWriter calls first when the response's first byte goes out. It
// passes ReadFrom through, so that a file still goes from disk to socket
// in the kernel, and Unwrap, for http.ResponseController.
type gateWriter struct {
	http.ResponseWriter
	once  sync.Once
	first func()
}

func (w *gateWriter) Write(p []byte) (int, error) {
	w.once.Do(w.first)
	return w.ResponseWriter.Write(p)
}

func (w *gateWriter) ReadFrom(r io.Reader) (int64, error) {
	w.once.Do(w.first)
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(writerOnly{w.ResponseWriter}, r)
}

func (w *gateWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// handleFile is GET /files/{id}: the file as a detail's files[] has it
// (docs/04-api.md, section 7.2).
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	}
	f, err := s.db.FileDetailOf(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file, or it is not offered for playback")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, fileJSON(f))
}
