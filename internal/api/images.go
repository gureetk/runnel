// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

// Images finds where an image's file of one width is kept.
type Images interface {
	Path(key, kind string, width int) string
}

// validKey is a key as images have them: 32 lower-case hex characters.
func validKey(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// handleImage is GET /img/{key}/{w} (docs/04-api.md, section 6): the
// image at the width asked for, or the nearest stored one above it, or
// the widest. It takes no token: keys are 128 bits that cannot be
// guessed, and what they name is artwork. A changed image has a new key,
// so the answer can be cached forever.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	width, err := strconv.Atoi(r.PathValue("w"))
	if s.images == nil || !validKey(key) || err != nil || width <= 0 {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such image")
		return
	}
	kind, widths, err := s.db.ImageByKey(r.Context(), key)
	if errors.Is(err, db.ErrNotFound) || (err == nil && len(widths) == 0) {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such image")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	slices.Sort(widths)
	pick := widths[len(widths)-1]
	for _, stored := range widths {
		if stored >= width {
			pick = stored
			break
		}
	}
	f, err := os.Open(s.images.Path(key, kind, pick))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "the image's file is not there; the next scan makes it again")
		return
	}
	defer f.Close()
	if kind == "logo" {
		w.Header().Set("Content-Type", "image/png")
	} else {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "", time.Time{}, f)
}
