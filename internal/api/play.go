// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

// Gate is the playback gate. A request that starts a playback holds it,
// and the background queue takes no new work while it is held
// (docs/02-architecture.md, section 5).
type Gate interface {
	Hold() (release func())
}

type noGate struct{}

func (noGate) Hold() func() { return func() {} }

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

// warmup is the one warm-up of a token.
type warmup struct {
	file    int64
	cancel  context.CancelFunc
	release func()
}

func (w *warmup) stop() {
	w.cancel()
	w.release()
}

type warmups struct {
	mu sync.Mutex
	by map[int64]*warmup // token id -> its warm-up
}

// startWarmup reads the head and the tail of a file in the background,
// and holds the gate from now until the file's playback starts, a newer
// prepare of the same token replaces this one, or prepareHold passes.
func (s *Server) startWarmup(tokenID, fileID int64, path string) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &warmup{file: fileID, cancel: cancel, release: s.gate.Hold()}
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
		readHeadAndTail(ctx, path)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		s.warm.mu.Lock()
		if s.warm.by[tokenID] == w {
			delete(s.warm.by, tokenID)
		}
		s.warm.mu.Unlock()
		w.stop()
	}()
}

// handOver ends a token's warm-up of a file whose playback has started,
// which holds the gate itself from here.
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
}

// handlePrepare is POST /files/{id}/prepare (docs/04-api.md, section 7.5).
// For direct play it answers at once and reads the file's head and tail in
// the background. This server encodes nothing yet, so a transcode cannot
// be prepared.
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
		s.fail(w, r, http.StatusConflict, "unsupported", "this server cannot transcode yet")
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
	s.startWarmup(callerOf(r.Context()).tokenID, id, f.Path)
	w.WriteHeader(http.StatusNoContent)
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
