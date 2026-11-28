// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// laterStages are the endpoints of docs/04-api.md that later stages build
// (docs/01-roadmap.md, section 4, exit criterion 1), with the stage, and
// the one call of Jellyfin's that the document names.
var laterStages = map[string]string{
	"GET /admin/update":                          "5: a newer release",
	"GET /files/{id}/video.mp4":                  "7: playing in a browser",
	"GET /files/{id}/audio.mp4":                  "7: playing in a browser",
	"GET /files/{id}/subs/{idx}.ass":             "7: playing in a browser",
	"GET /files/{id}/external/{external_id}.ass": "7: playing in a browser",
	"GET /files/{id}/attachments/{idx}":          "7: playing in a browser",
	"GET /files/{id}/sidecar.mkv":                "10: the sidecar",
	"GET /System/Info/Public":                    "Jellyfin's, not this server's",
}

// TestTheRoutesAreTheDocumentsEndpoints: every endpoint that docs/04-api.md
// names, in its sections or its examples, has a route, unless a later stage
// builds it, and every route is one that the document names.
func TestTheRoutesAreTheDocumentsEndpoints(t *testing.T) {
	doc, err := os.ReadFile("../../docs/04-api.md")
	if err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t)
	named := map[string]bool{} // the routes that the document's endpoints reach
	seen := map[string]bool{}
	wildcard := regexp.MustCompile(`\{[^}/]+\}`)
	for _, m := range regexp.MustCompile("`((?:GET|POST|PUT|PATCH|DELETE) /[^`\\s?]*)").FindAllStringSubmatch(string(doc), -1) {
		endpoint := m[1]
		if seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		if _, ok := laterStages[endpoint]; ok {
			continue
		}
		method, path, _ := strings.Cut(endpoint, " ")
		if path != healthPath {
			path = Prefix + path
		}
		req := httptest.NewRequest(method, wildcard.ReplaceAllString(path, "1"), nil)
		_, pattern := ts.mux.Handler(req)
		if pattern == "" {
			t.Errorf("%s: in docs/04-api.md, with no route and no later stage", endpoint)
			continue
		}
		named[pattern] = true
	}
	for endpoint := range laterStages {
		if !seen[endpoint] {
			t.Errorf("%s: listed for a later stage, and not in docs/04-api.md any more", endpoint)
		}
	}
	var unnamed []string
	for _, pattern := range ts.patterns {
		if !named[pattern] {
			unnamed = append(unnamed, pattern)
		}
	}
	sort.Strings(unnamed)
	for _, pattern := range unnamed {
		t.Errorf("%s: a route that docs/04-api.md does not name", pattern)
	}
	if len(named) < 60 {
		t.Fatalf("only %d routes reached: is the document still read?", len(named))
	}
}
