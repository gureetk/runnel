// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gureetk/runnel/internal/meta/tmdb"
)

// Metadata is the provider's client, as the settings use it.
type Metadata interface {
	// Check asks the provider whether it takes a key: nil, tmdb.ErrBadKey,
	// or an error for a provider that could not be asked.
	Check(ctx context.Context, key string) error
	// Forget is told that the key changed.
	Forget()
}

// keyCheckTime bounds a key's check, inside the JSON routes' deadline.
const keyCheckTime = 10 * time.Second

// keyBody is the metadata key's setting, as GET /admin/settings gives it
// (docs/04-api.md, section 8.1). The key itself is never in it.
type keyBody struct {
	Set       bool    `json:"set"`
	Hint      *string `json:"hint"`
	OK        *bool   `json:"ok"`
	CheckedAt *int64  `json:"checked_at"`
	Locked    bool    `json:"locked"`
}

// hintLength is how many of a key's last characters the hint shows, of a
// key of at least hintFrom characters.
const (
	hintLength = 4
	hintFrom   = 12
)

func (s *Server) keySetting(ctx context.Context) (keyBody, error) {
	stored, status, err := s.db.MetadataKey(ctx)
	if err != nil {
		return keyBody{}, err
	}
	key := stored
	if s.envKey != "" {
		key = s.envKey
	}
	b := keyBody{Set: key != "", Locked: s.envKey != ""}
	if !b.Set {
		return b, nil
	}
	if len(key) >= hintFrom {
		h := key[len(key)-hintLength:]
		b.Hint = &h
	}
	b.OK, b.CheckedAt = status.OK, status.CheckedAt
	return b, nil
}

// checkKey asks the provider whether it takes a new key, and answers the
// request when it does not, or cannot be asked.
func (s *Server) checkKey(w http.ResponseWriter, r *http.Request, key string) bool {
	if !tmdb.ValidKey(key) {
		s.fail(w, r, http.StatusBadRequest, "bad_key", "a key is TMDB's API read access token or its API key: 1 to 1000 printable characters, without spaces")
		return false
	}
	if s.metadata == nil {
		s.fail(w, r, http.StatusBadGateway, "upstream", "this server has no metadata provider")
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), keyCheckTime)
	defer cancel()
	err := s.metadata.Check(ctx, key)
	var busy *tmdb.Busy
	switch {
	case err == nil:
		return true
	case errors.Is(err, tmdb.ErrBadKey):
		s.fail(w, r, http.StatusBadRequest, "bad_key", "TMDB refuses the key")
	case errors.As(err, &busy):
		s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB has asked the server to wait; try again after "+busy.Until.UTC().Format(time.RFC3339))
	case errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil:
		s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB did not answer in time")
	default:
		var up *tmdb.UpstreamError
		if errors.As(err, &up) {
			s.fail(w, r, http.StatusBadGateway, "upstream", "TMDB cannot be reached: "+up.Msg)
		} else if r.Context().Err() == nil {
			s.internal(w, r, err)
		}
	}
	return false
}
