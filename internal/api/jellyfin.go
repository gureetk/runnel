// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/gureetk/runnel/internal/meta/jellyfin"
)

// jellyfinClient makes a client for a Jellyfin server from a request's
// url and api_key, or answers 400 and returns nil.
func (s *Server) jellyfinClient(w http.ResponseWriter, r *http.Request, url, key string) *jellyfin.Client {
	c, err := jellyfin.New(url, key, s.jellyfinHTTP)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), "jellyfin: "))
		return nil
	}
	return c
}

// jellyfinFailed answers an error of a Jellyfin call: 400 bad_key for a key
// Jellyfin refused, 502 upstream for a server that cannot be reached or
// answered with something the server does not read (docs/04-api.md,
// section 8.5).
func (s *Server) jellyfinFailed(w http.ResponseWriter, r *http.Request, err error) {
	var up *jellyfin.UpstreamError
	switch {
	case errors.Is(err, jellyfin.ErrBadKey):
		s.fail(w, r, http.StatusBadRequest, "bad_key", "Jellyfin refused the key")
	case errors.As(err, &up):
		s.fail(w, r, http.StatusBadGateway, "upstream", "Jellyfin: "+strings.TrimPrefix(up.Err.Error(), "jellyfin: "))
	default:
		s.internal(w, r, err)
	}
}

// handleJellyfinUsers is POST /admin/import/jellyfin/users: Jellyfin's
// version and its users by name, for the form that maps them to people
// here.
func (s *Server) handleJellyfinUsers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	c := s.jellyfinClient(w, r, req.URL, req.APIKey)
	if c == nil {
		return
	}
	version, err := c.Version(r.Context())
	if err != nil {
		s.jellyfinFailed(w, r, err)
		return
	}
	users, err := c.Users(r.Context())
	if err != nil {
		s.jellyfinFailed(w, r, err)
		return
	}
	slices.SortFunc(users, func(a, b jellyfin.User) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	type userBody struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := []userBody{}
	for _, u := range users {
		out = append(out, userBody{u.ID, u.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version, "users": out})
}
