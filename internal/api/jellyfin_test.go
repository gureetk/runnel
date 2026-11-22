// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gureetk/runnel/internal/meta/jellyfin/standin"
)

func TestJellyfinUsers(t *testing.T) {
	ts := newTestServer(t)
	jf := &standin.Server{Key: "0123456789abcdef0123456789abcdef", Version: "12.4.0",
		Users: []standin.User{{ID: "5b4d20b7e5df4f2a8a2d3e4f5a6b7c8d", Name: "noor"}, {ID: "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c", Name: "Ada"}}}
	url := jf.Start(t).URL

	var body struct {
		Version string `json:"version"`
		Users   []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin/users", ts.adminToken, local,
		map[string]string{"url": url, "api_key": jf.Key}, &body); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if body.Version != "12.4.0" || len(body.Users) != 2 || body.Users[0].Name != "Ada" || body.Users[0].ID != "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c" {
		t.Fatalf("%+v", body)
	}

	gone := httptest.NewServer(nil)
	gone.Close()
	var e apiError
	for _, c := range []struct {
		body   map[string]string
		status int
		code   string
	}{
		{map[string]string{"url": url, "api_key": "another-key"}, 400, "bad_key"},
		{map[string]string{"url": gone.URL, "api_key": jf.Key}, 502, "upstream"},
		{map[string]string{"url": "jellyfin.lan", "api_key": jf.Key}, 400, "bad_request"},
		{map[string]string{"url": url, "api_key": "a,b"}, 400, "bad_request"},
		{map[string]string{"url": url}, 400, "bad_request"},
	} {
		e = apiError{}
		if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin/users", ts.adminToken, local, c.body, &e); rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}
	ts.addUser(t, "ada", "correct horse", true)
	noor := ts.signIn(t, "noor", false, "tv")
	if rec := ts.call(t, "POST", "/api/v1/admin/import/jellyfin/users", noor, "", map[string]string{"url": url, "api_key": jf.Key}, nil); rec.Code != 403 {
		t.Fatalf("someone who is no admin: %d", rec.Code)
	}
}
