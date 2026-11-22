// SPDX-License-Identifier: GPL-3.0-or-later

// Package standin is a Jellyfin server for tests: it answers the calls of
// docs/06-library-and-metadata.md, section 7, as Jellyfin 12.4's published
// API description gives them, from data a test sets, and it refuses a key
// sent the older ways that Jellyfin 12 turns off by default.
package standin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Item is a film, a series or an episode, with each user's play state by
// user id.
type Item struct {
	ID, Name, Type, Path string
	ProviderIDs          map[string]string
	UserData             map[string]UserData
}

// UserData is a user's play state, as Jellyfin's UserItemDataDto has it.
type UserData struct {
	Played         bool
	PlayCount      int
	LastPlayedDate string // RFC 3339, or "" for never
	PositionTicks  int64
}

// User is a Jellyfin user.
type User struct{ ID, Name string }

// Server is a stand-in Jellyfin server. Set its fields before the first
// request; Requests records what it was asked.
type Server struct {
	Key     string
	Version string
	Users   []User
	Items   []Item // in the order SortName gives
	// Fail, when set, answers every request with that status.
	Fail int

	mu       sync.Mutex
	requests []*http.Request
}

// Start serves the stand-in until the test ends.
func (s *Server) Start(t interface{ Cleanup(func()) }) *httptest.Server {
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

// Requests are the requests made so far.
func (s *Server) Requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

var tokenRE = regexp.MustCompile(`^MediaBrowser (?:.*, *)?Token="([^"]*)"`)

// authorized reports whether the request carries the key in the one way
// Jellyfin 12 reads by default: the Authorization header, scheme
// MediaBrowser, or the ApiKey parameter.
func (s *Server) authorized(r *http.Request) bool {
	if m := tokenRE.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		return m[1] == s.Key
	}
	return r.URL.Query().Get("ApiKey") == s.Key && s.Key != ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Clone(r.Context()))
	s.mu.Unlock()
	if s.Fail != 0 {
		http.Error(w, "failing", s.Fail)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "not here", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/System/Info/Public":
		writeJSON(w, map[string]any{"LocalAddress": "http://" + r.Host, "ServerName": "Stand-in", "Version": s.Version,
			"ProductName": "Jellyfin Server", "OperatingSystem": "", "Id": "0123456789abcdef0123456789abcdef", "StartupWizardCompleted": true})
	case "/Users":
		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		out := []map[string]any{}
		for _, u := range s.Users {
			out = append(out, map[string]any{"Name": u.Name, "ServerId": "0123456789abcdef0123456789abcdef", "Id": u.ID,
				"HasPassword": true, "Policy": map[string]any{"IsAdministrator": false}})
		}
		writeJSON(w, out)
	case "/Items":
		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.items(w, r)
	default:
		http.NotFound(w, r)
	}
}

// items answers GET /Items for the parameters the import sends, and
// refuses others it would not answer the same way.
func (s *Server) items(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	types := map[string]bool{}
	for _, t := range strings.Split(q.Get("includeItemTypes"), ",") {
		types[t] = true
	}
	fields := strings.Split(q.Get("fields"), ",")
	if q.Get("recursive") != "true" || len(types) == 0 {
		http.Error(w, "the stand-in lists recursively, by type", http.StatusBadRequest)
		return
	}
	start, _ := strconv.Atoi(q.Get("startIndex"))
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = len(s.Items)
	}
	user := q.Get("userId")
	if user != "" && !slices.ContainsFunc(s.Users, func(u User) bool { return u.ID == user }) {
		http.Error(w, "no such user", http.StatusNotFound)
		return
	}
	var all []Item
	for _, it := range s.Items {
		if types[it.Type] && it.Path != "" {
			all = append(all, it)
		}
	}
	page := []map[string]any{}
	for i := start; i < len(all) && i < start+limit; i++ {
		it := all[i]
		dto := map[string]any{"Name": it.Name, "ServerId": "0123456789abcdef0123456789abcdef", "Id": it.ID, "Type": it.Type,
			"IsFolder": it.Type == "Series", "LocationType": "FileSystem"}
		if slices.Contains(fields, "Path") {
			dto["Path"] = it.Path
		}
		if slices.Contains(fields, "ProviderIds") {
			ids := it.ProviderIDs
			if ids == nil {
				ids = map[string]string{}
			}
			dto["ProviderIds"] = ids
		}
		if user != "" && q.Get("enableUserData") == "true" {
			d := it.UserData[user]
			ud := map[string]any{"PlaybackPositionTicks": d.PositionTicks, "PlayCount": d.PlayCount, "IsFavorite": false,
				"Played": d.Played, "Key": it.ID, "ItemId": it.ID}
			if d.LastPlayedDate != "" {
				ud["LastPlayedDate"] = d.LastPlayedDate
			}
			dto["UserData"] = ud
		}
		page = append(page, dto)
	}
	writeJSON(w, map[string]any{"Items": page, "TotalRecordCount": len(all), "StartIndex": start})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
