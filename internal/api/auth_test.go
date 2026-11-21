// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/model"
)

// call sends one request with an optional bearer token from a given
// address, and decodes a JSON answer into out when out is not nil.
func (ts *testServer) call(t *testing.T, method, target, token, from string, body any, out any) *httptest.ResponseRecorder {
	t.Helper()
	var payload io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		payload = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, target, payload)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if from != "" {
		req.RemoteAddr = from
	}
	rec := httptest.NewRecorder()
	ts.Handler().ServeHTTP(rec, req)
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %d %q: %v", method, target, rec.Code, rec.Body.String(), err)
		}
	}
	return rec
}

// addUser makes a user straight in the database.
func (ts *testServer) addUser(t *testing.T, name, password string, admin bool) model.User {
	t.Helper()
	var pw *model.Password
	if password != "" {
		p, err := auth.HashPassword(password)
		if err != nil {
			t.Fatal(err)
		}
		pw = &p
	}
	u, err := ts.db.CreateUser(context.Background(), name, pw, admin, ts.now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type loginAnswer struct {
	Token string `json:"token"`
	User  struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		IsAdmin bool   `json:"is_admin"`
	} `json:"user"`
}

func login(name, password, device string) map[string]any {
	return map[string]any{"name": name, "password": password,
		"device": map[string]string{"id": device, "name": "Test device", "client": "runnel-test/0"}}
}

// withProbeRoutes adds two routes that say who called them: one that
// needs a token in the header, and one that also takes ?t= like a media
// route.
func (ts *testServer) withProbeRoutes() {
	who := func(w http.ResponseWriter, r *http.Request) {
		c := callerOf(r.Context())
		writeJSON(w, 200, map[string]any{"user": c.user.Name, "local": c.local})
	}
	ts.mux.Handle("GET /api/v1/test/json", ts.json(ts.signedIn(who)))
	ts.mux.Handle("GET /api/v1/test/media", ts.media(who))
	ts.mux.Handle("GET /api/v1/test/admin", ts.json(ts.admin(who)))
}

func TestLogin(t *testing.T) {
	ts := newTestServer(t)
	ts.withProbeRoutes()
	ada := ts.addUser(t, "ada", "correct horse", true)

	var a loginAnswer
	rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login("Ada", "correct horse", "dev-1"), &a)
	if rec.Code != 200 || !auth.LooksLikeToken(a.Token) || a.User.ID != ada.ID || a.User.Name != "ada" || !a.User.IsAdmin {
		t.Fatalf("login: %d %+v", rec.Code, a)
	}

	var who map[string]any
	if rec := ts.call(t, "GET", "/api/v1/test/json", a.Token, "", nil, &who); rec.Code != 200 || who["user"] != "ada" {
		t.Fatalf("with the token: %d %v", rec.Code, who)
	}

	// Signing in again from the same device replaces its token.
	var b loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "dev-1"), &b)
	if rec := ts.call(t, "GET", "/api/v1/test/json", a.Token, "", nil, nil); rec.Code != 401 {
		t.Fatalf("the replaced token still works: %d", rec.Code)
	}
	// Another device keeps its own.
	var c loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "dev-2"), &c)
	for _, tok := range []string{b.Token, c.Token} {
		if rec := ts.call(t, "GET", "/api/v1/test/json", tok, "", nil, nil); rec.Code != 200 {
			t.Fatalf("a current token was refused: %d", rec.Code)
		}
	}
}

func TestLoginRefusals(t *testing.T) {
	ts := newTestServer(t)
	ts.addUser(t, "ada", "correct horse", true)
	ts.addUser(t, "noor", "", false) // signs in by pairing only

	var e apiError
	for _, c := range []struct {
		body   any
		status int
		code   string
	}{
		{login("ada", "wrong horse!", "d"), 401, "wrong_login"},
		{login("nobody", "correct horse", "d"), 401, "wrong_login"},
		{login("noor", "correct horse", "d"), 401, "wrong_login"},
		{login("ada", "correct horse", ""), 400, "bad_request"},
		{"not an object", 400, "bad_request"},
	} {
		rec := ts.call(t, "POST", "/api/v1/auth/login", "", "198.51.100.7:4000", c.body, &e)
		if rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}
}

func TestLoginLimit(t *testing.T) {
	ts := newTestServer(t)
	ts.addUser(t, "ada", "correct horse", true)
	from := "203.0.113.9:5000"
	for i := 0; i < 5; i++ {
		if rec := ts.call(t, "POST", "/api/v1/auth/login", "", from, login("ada", "wrong horse!", "d"), nil); rec.Code != 401 {
			t.Fatalf("try %d: %d", i+1, rec.Code)
		}
	}
	// The sixth try is refused before the password is looked at, even
	// when it is right.
	var e apiError
	rec := ts.call(t, "POST", "/api/v1/auth/login", "", from, login("ada", "correct horse", "d"), &e)
	if rec.Code != 429 || e.Error.Code != "too_many" || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("sixth try: %d %+v Retry-After %q", rec.Code, e, rec.Header().Get("Retry-After"))
	}
	// Another address is not affected.
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "203.0.113.10:5000", login("ada", "correct horse", "d"), nil); rec.Code != 200 {
		t.Fatalf("another address: %d", rec.Code)
	}
	ts.advance(time.Minute)
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", from, login("ada", "correct horse", "d"), nil); rec.Code != 200 {
		t.Fatalf("a minute later: %d", rec.Code)
	}
}

func TestTokenInQuery(t *testing.T) {
	ts := newTestServer(t)
	ts.withProbeRoutes()
	ts.addUser(t, "ada", "correct horse", true)
	var a loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "d"), &a)

	if rec := ts.call(t, "GET", "/api/v1/test/media?t="+a.Token, "", "", nil, nil); rec.Code != 200 {
		t.Fatalf("a media route with ?t=: %d", rec.Code)
	}
	if rec := ts.call(t, "GET", "/api/v1/test/json?t="+a.Token, "", "", nil, nil); rec.Code != 401 {
		t.Fatalf("a JSON route took ?t=: %d", rec.Code)
	}
	var e apiError
	for _, tok := range []string{"", "rn_tooshort", strings.Repeat("x", 46)} {
		if rec := ts.call(t, "GET", "/api/v1/test/json", tok, "", nil, &e); rec.Code != 401 || e.Error.Code != "unauthorized" {
			t.Errorf("token %q: %d %+v", tok, rec.Code, e)
		}
	}
}

func TestAdminRoutes(t *testing.T) {
	ts := newTestServer(t)
	ts.withProbeRoutes()
	ts.addUser(t, "ada", "correct horse", true)
	ts.addUser(t, "noor", "another horse", false)
	var ada, noor loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "d"), &ada)
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("noor", "another horse", "d"), &noor)

	var e apiError
	for _, c := range []struct {
		name, token, from string
		status            int
		code              string
	}{
		{"no token", "", "", 401, "unauthorized"},
		{"not an admin", noor.Token, "", 403, "forbidden"},
		{"an admin", ada.Token, "", 200, ""},
		{"local token over loopback", ts.adminToken, "127.0.0.1:40000", 200, ""},
		{"local token over IPv6 loopback", ts.adminToken, "[::1]:40000", 200, ""},
		{"local token from elsewhere", ts.adminToken, "192.0.2.50:40000", 401, "unauthorized"},
	} {
		e = apiError{}
		rec := ts.call(t, "GET", "/api/v1/test/admin", c.token, c.from, nil, &e)
		if rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%s: %d %+v", c.name, rec.Code, e)
		}
	}
	var who map[string]any
	ts.call(t, "GET", "/api/v1/test/admin", ts.adminToken, "127.0.0.1:40000", nil, &who)
	if who["local"] != true || who["user"] != "" {
		t.Fatalf("the local admin token is not a user: %v", who)
	}
}

func TestLocalTokenFromTheServersOwnAddress(t *testing.T) {
	ts := newTestServer(t)
	ts.withProbeRoutes()
	req := httptest.NewRequest("GET", "/api/v1/test/admin", nil)
	req.Header.Set("Authorization", "Bearer "+ts.adminToken)
	// A server listening on one address of its own, reached from itself
	// through that address.
	req.RemoteAddr = "192.0.2.5:41000"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("192.0.2.5"), Port: 8610}))
	rec := httptest.NewRecorder()
	ts.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateUser(t *testing.T) {
	ts := newTestServer(t)
	local := "127.0.0.1:40000"

	var u userBody
	rec := ts.call(t, "POST", "/api/v1/admin/users", ts.adminToken, local,
		map[string]any{"name": "ada", "password": "correct horse"}, &u)
	if rec.Code != 201 || u.ID == 0 || u.Name != "ada" || !u.IsAdmin {
		t.Fatalf("first user: %d %+v (the first user is always an admin)", rec.Code, u)
	}
	rec = ts.call(t, "POST", "/api/v1/admin/users", ts.adminToken, local,
		map[string]any{"name": "noor", "password": "another horse", "is_admin": false}, &u)
	if rec.Code != 201 || u.IsAdmin {
		t.Fatalf("second user: %d %+v", rec.Code, u)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login("noor", "another horse", "d"), nil); rec.Code != 200 {
		t.Fatalf("the new user cannot sign in: %d", rec.Code)
	}

	var e apiError
	for _, c := range []struct {
		body   map[string]any
		status int
		code   string
	}{
		{map[string]any{"name": "NOOR", "password": "another horse"}, 409, "conflict"},
		{map[string]any{"name": "sam", "password": "short"}, 400, "weak_password"},
		{map[string]any{"name": "sam", "password": ""}, 400, "weak_password"},
		{map[string]any{"name": " sam", "password": "long enough!"}, 400, "bad_request"},
		{map[string]any{"name": "", "password": "long enough!"}, 400, "bad_request"},
		{map[string]any{"name": strings.Repeat("s", 41), "password": "long enough!"}, 400, "bad_request"},
		{map[string]any{"name": "s\tam", "password": "long enough!"}, 400, "bad_request"},
	} {
		e = apiError{}
		rec := ts.call(t, "POST", "/api/v1/admin/users", ts.adminToken, local, c.body, &e)
		if rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%v: %d %+v, want %d %s", c.body, rec.Code, e, c.status, c.code)
		}
	}

	// Without a password, a user can only pair, which signs nobody in by
	// password.
	rec = ts.call(t, "POST", "/api/v1/admin/users", ts.adminToken, local, map[string]any{"name": "kim"}, &u)
	if rec.Code != 201 {
		t.Fatalf("a user without a password: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login("kim", "", "d"), nil); rec.Code != 400 {
		t.Fatalf("an empty password: %d", rec.Code)
	}
}

func TestTokenUseIsWrittenEveryTenMinutes(t *testing.T) {
	ts := newTestServer(t)
	ts.withProbeRoutes()
	ts.addUser(t, "ada", "correct horse", true)
	var a loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "d"), &a)
	signedInAt := ts.now().UnixMilli()

	lastUsed := func() int64 {
		tu, err := ts.db.UserByToken(context.Background(), auth.HashToken(a.Token))
		if err != nil {
			t.Fatal(err)
		}
		return tu.LastUsedAt
	}
	if got := lastUsed(); got != signedInAt {
		t.Fatalf("last_used_at %d after sign-in, want %d", got, signedInAt)
	}

	ts.advance(9 * time.Minute)
	ts.call(t, "GET", "/api/v1/test/json", a.Token, "", nil, nil)
	time.Sleep(50 * time.Millisecond)
	if got := lastUsed(); got != signedInAt {
		t.Fatalf("written again after 9 minutes: %d", got)
	}

	ts.advance(2 * time.Minute)
	ts.call(t, "GET", "/api/v1/test/json", a.Token, "", nil, nil)
	ts.call(t, "GET", "/api/v1/test/json", a.Token, "", nil, nil)
	want := ts.now().UnixMilli()
	for deadline := time.Now().Add(2 * time.Second); lastUsed() != want && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := lastUsed(); got != want {
		t.Fatalf("not written after 11 minutes: %d, want %d", got, want)
	}
}
