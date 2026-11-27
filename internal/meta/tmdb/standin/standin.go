// SPDX-License-Identifier: GPL-3.0-or-later

// Package standin is TMDB for tests: it answers under /3 from pages a test
// sets, takes the credentials it is given in the two ways TMDB reads them
// (docs/06-library-and-metadata.md, section 6.4), and answers 401, 404 and
// 429 with bodies in the shape of TMDB's error table, as copies of it give
// them. Every title and id in a test is invented.
package standin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// Configuration is an answer to GET /configuration in the shape of TMDB's
// example, with its image sizes.
const Configuration = `{
  "images": {
    "base_url": "http://image.tmdb.org/t/p/",
    "secure_base_url": "https://image.tmdb.org/t/p/",
    "backdrop_sizes": ["w300", "w780", "w1280", "original"],
    "logo_sizes": ["w45", "w92", "w154", "w185", "w300", "w500", "original"],
    "poster_sizes": ["w92", "w154", "w185", "w342", "w500", "w780", "original"],
    "profile_sizes": ["w45", "w185", "h632", "original"],
    "still_sizes": ["w92", "w185", "w300", "original"]
  },
  "change_keys": ["overview", "title"]
}`

// Busy is one answer 429, with its Retry-After header, "" for none.
type Busy struct{ RetryAfter string }

// Server is a stand-in TMDB. Set its fields before the first request, or
// under Lock; Requests records what it was asked.
type Server struct {
	// Token and APIKey are the credentials it takes: Token as a bearer
	// token, APIKey as the api_key parameter.
	Token, APIKey string
	// Pages are the answers by path under /3 and the query without
	// api_key, sorted, as url.Values.Encode gives it: "/movie/101" or
	// "/search/movie?query=a+film&year=2001". Anything else is 404.
	// /configuration is Configuration unless it is set here.
	Pages map[string]string
	// Busy are answers 429 given, one each, before any other answer.
	Busy []Busy
	// Fail, when set, answers every request with that status.
	Fail int

	mu       sync.Mutex
	requests []*http.Request
}

// Start serves the stand-in until the test ends; its URL plus "/3" is the
// client's base.
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

// Set changes a page while the stand-in runs.
func (s *Server) Set(page, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Pages == nil {
		s.Pages = map[string]string{}
	}
	s.Pages[page] = body
}

// failWith answers as TMDB's error table has it: success false, a code and a
// message.
func failWith(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "status_code": code, "status_message": msg})
}

func (s *Server) authorized(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); h != "" {
		token, ok := strings.CutPrefix(h, "Bearer ")
		return ok && s.Token != "" && token == s.Token
	}
	return s.APIKey != "" && r.URL.Query().Get("api_key") == s.APIKey
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Clone(r.Context()))
	fail := s.Fail
	var busy *Busy
	if len(s.Busy) > 0 {
		busy = &s.Busy[0]
		s.Busy = s.Busy[1:]
	}
	s.mu.Unlock()
	if fail != 0 {
		http.Error(w, "failing", fail)
		return
	}
	if busy != nil {
		if busy.RetryAfter != "" {
			w.Header().Set("Retry-After", busy.RetryAfter)
		}
		failWith(w, http.StatusTooManyRequests, 25, "Your request count (41) is over the allowed limit of 40.")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "not here", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		failWith(w, http.StatusUnauthorized, 7, "Invalid API key: You must be granted a valid key.")
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, "/3")
	if !ok {
		failWith(w, http.StatusNotFound, 34, "The resource you requested could not be found.")
		return
	}
	q := r.URL.Query()
	q.Del("api_key")
	page := path
	if len(q) > 0 {
		page += "?" + url.Values(q).Encode()
	}
	s.mu.Lock()
	body, found := s.Pages[page]
	s.mu.Unlock()
	if !found && page == "/configuration" {
		body, found = Configuration, true
	}
	if !found {
		failWith(w, http.StatusNotFound, 34, "The resource you requested could not be found.")
		return
	}
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	_, _ = w.Write([]byte(body))
}
