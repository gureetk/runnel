// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/gureetk/runnel/internal/meta/jellyfin"
)

// Imports is the Jellyfin import (docs/06-library-and-metadata.md, section
// 7).
type Imports interface {
	// Start checks a request and runs it in the background.
	Start(ctx context.Context, req jellyfin.Request) error
	// Report is the queued, running or last import's report.
	Report(ctx context.Context, full bool) (jellyfin.Report, error)
}

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

// handleStartImport is POST /admin/import/jellyfin: the import, or its dry
// run, which is what it does unless dry_run is false. It answers 202 once
// Jellyfin and the people named are checked.
func (s *Server) handleStartImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string             `json:"url"`
		APIKey string             `json:"api_key"`
		Users  map[string]string  `json:"users"`
		Paths  []jellyfin.PathMap `json:"paths"`
		DryRun *bool              `json:"dry_run"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if s.imports == nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "this server runs no import")
		return
	}
	dry := req.DryRun == nil || *req.DryRun
	err := s.imports.Start(r.Context(), jellyfin.Request{URL: req.URL, APIKey: req.APIKey, Users: req.Users, Paths: req.Paths, DryRun: dry})
	var bad *jellyfin.RequestError
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, struct{}{})
	case errors.As(err, &bad):
		s.fail(w, r, http.StatusBadRequest, "bad_request", bad.Msg)
	case errors.Is(err, jellyfin.ErrRunning):
		s.fail(w, r, http.StatusConflict, "conflict", "an import is running")
	default:
		s.jellyfinFailed(w, r, err)
	}
}

// handleImportReport is GET /admin/import/jellyfin: the state and the
// report of the running or the last import, with every row that did not
// join for full=1.
func (s *Server) handleImportReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "full" || (q.Get("full") != "1" && q.Get("full") != "0") {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameter is full, 1 or 0")
			return
		}
	}
	if s.imports == nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "no import has run")
		return
	}
	rep, err := s.imports.Report(r.Context(), q.Get("full") == "1")
	switch {
	case errors.Is(err, jellyfin.ErrNoReport):
		s.fail(w, r, http.StatusNotFound, "not_found", "no import has run")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, rep)
	}
}
