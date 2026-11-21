// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/probe"
)

// containerOfExtension stands in for the probed container of a file not
// probed yet.
var containerOfExtension = map[string]string{
	".mkv": "matroska", ".webm": "webm", ".mp4": "mp4", ".m4v": "mp4", ".mov": "mov", ".avi": "avi",
	".ts": "mpegts", ".m2ts": "mpegts", ".wmv": "asf", ".mpg": "mpeg", ".mpeg": "mpeg",
}

// handleRaw is GET /files/{id}/raw: the file as it is on disk, by byte
// range, copied from disk to socket by the kernel (docs/04-api.md,
// section 7.2; docs/05-playback.md, section 3). It has no write deadline:
// a paused player may keep it open for hours.
//
// A playback starts with this request: it holds the playback gate from
// its arrival until firstByteHold after its first byte, and takes over
// from its token's prepare of the same file (docs/02-architecture.md,
// section 5).
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
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
	s.handOver(callerOf(r.Context()).tokenID, id)
	f, err := s.db.RawFileOf(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	case errors.Is(err, db.ErrOutsideRoot):
		s.log.Warn("a stored path leaves its root; it is not served", "file", id)
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	// A missing file, or one on a root that is away, is answered without
	// touching the disk.
	if !f.Available {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file is missing or its disk is away")
		return
	}
	file, err := os.Open(f.Path)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file cannot be opened")
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file cannot be read")
		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	container := f.Container
	if container == "" {
		container = containerOfExtension[strings.ToLower(filepath.Ext(f.Path))]
	}
	w.Header().Set("Content-Type", probe.MediaType(container))
	if f.Fingerprint != "" {
		w.Header().Set("ETag", `"`+f.Fingerprint+`"`)
	}
	gw := &gateWriter{ResponseWriter: w, first: func() {
		sending = true
		time.AfterFunc(s.firstByteHold, release)
	}}
	http.ServeContent(gw, r, "", fi.ModTime(), file)
}

// trackTypes are the media types the files beside a video are served with.
var trackTypes = map[string]string{
	".srt": "application/x-subrip", ".ass": "text/x-ssa", ".ssa": "text/x-ssa", ".vtt": "text/vtt",
	".sub": "application/octet-stream", ".sup": "application/octet-stream",
	".mka": "audio/x-matroska", ".ac3": "audio/ac3", ".flac": "audio/flac", ".opus": "audio/ogg",
	".dts": "audio/vnd.dts", ".mp3": "audio/mpeg", ".aac": "audio/aac",
}

// handleExternal is GET /files/{id}/external/{external_id}: a subtitle or
// audio file beside the video, by byte range, as the video is served
// (docs/04-api.md, section 7.2).
func (s *Server) handleExternal(w http.ResponseWriter, r *http.Request) {
	id, err1 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	track, err2 := strconv.ParseInt(r.PathValue("external_id"), 10, 64)
	if err1 != nil || err2 != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file or track")
		return
	}
	t, err := s.db.TrackFileOf(r.Context(), id, track)
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file or track")
		return
	case errors.Is(err, db.ErrOutsideRoot):
		s.log.Warn("a stored path leaves its root; it is not served", "file", id, "track", track)
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file or track")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if !t.Available {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the file is missing or its disk is away")
		return
	}
	file, err := os.Open(t.Path)
	if err != nil {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the track cannot be opened")
		return
	}
	defer file.Close()
	fi, err := file.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		s.fail(w, r, http.StatusServiceUnavailable, "unavailable", "the track cannot be read")
		return
	}
	// An audio track plays along with its video: no write deadline.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", trackTypes[strings.ToLower(filepath.Ext(t.Path))])
	http.ServeContent(w, r, "", fi.ModTime(), file)
}
