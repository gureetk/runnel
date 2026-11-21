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
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such file")
		return
	}
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
	http.ServeContent(w, r, "", fi.ModTime(), file)
}
