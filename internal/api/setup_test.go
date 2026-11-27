// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/auth"
)

func TestFirstRun(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	code, err := ts.db.SetupCode(ctx, auth.NewSetupCode)
	if err != nil || len(code) != 8 {
		t.Fatalf("code %q %v", code, err)
	}
	// A second start keeps the code.
	if again, _ := ts.db.SetupCode(ctx, auth.NewSetupCode); again != code {
		t.Fatalf("a restart made %q after %q", again, code)
	}
	var shown struct{ Code string }
	local := func(method, target string, out any) int {
		req := httptest.NewRequest(method, target, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("Authorization", "Bearer "+ts.adminToken)
		rec := httptest.NewRecorder()
		ts.Handler().ServeHTTP(rec, req)
		if out != nil {
			mustDecode(t, rec, out)
		}
		return rec.Code
	}
	if c := local("GET", "/api/v1/admin/setup-code", &shown); c != 200 || shown.Code != code[:4]+"-"+code[4:] {
		t.Fatalf("runnel setup-code: %d %+v", c, shown)
	}

	setup := func(from, typed, name, password string) (int, loginAnswer) {
		t.Helper()
		var a loginAnswer
		rec := ts.call(t, "POST", "/api/v1/setup", "", from, map[string]any{"code": typed, "name": name, "password": password,
			"device": map[string]string{"id": "browser", "name": "Firefox on Linux", "client": "runnel-web/0.1.0"}}, nil)
		if rec.Code == 200 {
			mustDecode(t, rec, &a)
		}
		return rec.Code, a
	}
	// Five wrong codes a minute from one address.
	for i := range 5 {
		if c, _ := setup("192.0.2.5:1", "ABCD-EFGH", "ada", "correct horse"); c != 403 {
			t.Fatalf("wrong code %d: %d", i, c)
		}
	}
	if c, _ := setup("192.0.2.5:1", code, "ada", "correct horse"); c != 429 {
		t.Fatalf("the sixth try: %d", c)
	}
	if c, _ := setup("192.0.2.6:1", code, "ada", "short"); c != 400 {
		t.Fatalf("a short password: %d", c)
	}
	if c, _ := setup("192.0.2.6:1", code, " ada", "correct horse"); c != 400 {
		t.Fatalf("a bad name: %d", c)
	}
	var info struct {
		SetupDone bool `json:"setup_done"`
	}
	if ts.call(t, "GET", "/api/v1/server", "", "", nil, &info); info.SetupDone {
		t.Fatal("setup done before the first person")
	}
	// In small letters with the hyphen, as a person might type it.
	c, a := setup("192.0.2.6:1", strings.ToLower(code[:4]+"-"+code[4:]), "ada", "correct horse")
	if c != 200 || a.User.Name != "ada" || !a.User.IsAdmin || a.Token == "" {
		t.Fatalf("setup: %d %+v", c, a)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/me", a.Token, "", nil, nil); rec.Code != 200 {
		t.Fatalf("the first person's token: %d", rec.Code)
	}
	if ts.call(t, "GET", "/api/v1/server", "", "", nil, &info); !info.SetupDone {
		t.Fatal("setup not done after the first person")
	}
	if c, _ := setup("192.0.2.7:1", code, "eve", "correct horse"); c != 409 {
		t.Fatalf("a second setup: %d", c)
	}
	if c := local("GET", "/api/v1/admin/setup-code", nil); c != 409 {
		t.Fatalf("the code after setup: %d", c)
	}
	if c := ts.call(t, "GET", "/api/v1/admin/setup-code", a.Token, "", nil, nil).Code; c != 403 {
		t.Fatalf("an admin's token for the code: %d", c)
	}
}

func TestFirstPersonByTheShellDropsTheCode(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	code, _ := ts.db.SetupCode(ctx, auth.NewSetupCode)
	ts.addUser(t, "ada", "correct horse", false)
	if rec := ts.call(t, "POST", "/api/v1/setup", "", "", map[string]any{"code": code, "name": "eve", "password": "correct horse",
		"device": map[string]string{"id": "x"}}, nil); rec.Code != 409 {
		t.Fatalf("setup after runnel user add: %d", rec.Code)
	}
	if left, _, _ := ts.db.Setting(ctx, "setup_code"); left != "" {
		t.Fatalf("the code is still there: %q", left)
	}
}

func TestServerName(t *testing.T) {
	ts := newTestServer(t)
	ada := ts.signIn(t, "ada", true, "phone")
	bo := ts.signIn(t, "bo", false, "laptop")
	name := func() string {
		var info struct{ Name string }
		ts.call(t, "GET", "/api/v1/server", "", "", nil, &info)
		return info.Name
	}
	var settings struct {
		ServerName settingBody `json:"server_name"`
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/settings", ada, "", nil, &settings); rec.Code != 200 || settings.ServerName.Value != "Test" || settings.ServerName.Locked {
		t.Fatalf("settings %d %+v", rec.Code, settings)
	}
	if rec := ts.call(t, "PATCH", "/api/v1/admin/settings", ada, "", map[string]any{"server_name": "Upstairs"}, &settings); rec.Code != 200 ||
		settings.ServerName.Value != "Upstairs" || name() != "Upstairs" {
		t.Fatalf("rename: %d %+v %q", rec.Code, settings, name())
	}
	if rec := ts.call(t, "PATCH", "/api/v1/admin/settings", ada, "", map[string]any{"server_name": nil}, nil); rec.Code != 200 || name() != "Test" {
		t.Fatalf("back to the host name: %d %q", rec.Code, name())
	}
	for _, c := range []struct {
		token string
		body  map[string]any
		want  int
	}{
		{bo, map[string]any{"server_name": "Mine"}, 403},
		{ada, map[string]any{"server_name": ""}, 400},
		{ada, map[string]any{"server_name": strings.Repeat("x", 41)}, 400},
		{ada, map[string]any{"tmdb_key": "a key with spaces"}, 400},
		{ada, map[string]any{"colour": "blue"}, 400},
	} {
		if rec := ts.call(t, "PATCH", "/api/v1/admin/settings", c.token, "", c.body, nil); rec.Code != c.want {
			t.Errorf("%v: %d, want %d", c.body, rec.Code, c.want)
		}
	}
	// Set by the environment, the name is locked.
	ts.info.NameFromEnv = true
	if rec := ts.call(t, "PATCH", "/api/v1/admin/settings", ada, "", map[string]any{"server_name": "Other"}, nil); rec.Code != 409 {
		t.Fatalf("a locked name: %d", rec.Code)
	}
	ts.call(t, "GET", "/api/v1/admin/settings", ada, "", nil, &settings)
	if !settings.ServerName.Locked || name() != "Test" {
		t.Fatalf("locked %+v %q", settings, name())
	}
}

func mustDecode(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("%d %q: %v", rec.Code, rec.Body.String(), err)
	}
}
