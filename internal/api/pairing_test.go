// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type pairStart struct {
	Code      string `json:"code"`
	Secret    string `json:"secret"`
	ExpiresIn int    `json:"expires_in"`
	PollEvery int    `json:"poll_every"`
}

// signIn adds a user with a password and signs them in on a device.
func (ts *testServer) signIn(t *testing.T, name string, admin bool, device string) string {
	t.Helper()
	ts.addUser(t, name, "correct horse", admin)
	var a loginAnswer
	if rec := ts.call(t, "POST", "/api/v1/auth/login", "", "", login(name, "correct horse", device), &a); rec.Code != 200 {
		t.Fatalf("login %s: %d", name, rec.Code)
	}
	return a.Token
}

func (ts *testServer) startPairing(t *testing.T, from, device string) (pairStart, int) {
	t.Helper()
	var p pairStart
	rec := ts.call(t, "POST", "/api/v1/auth/pair/start", "", from,
		map[string]any{"device": map[string]string{"id": device, "name": "Bedroom TV", "client": "runnel-androidtv/1.0.0"}}, &p)
	return p, rec.Code
}

func TestPairing(t *testing.T) {
	ts := newTestServer(t)
	ada := ts.signIn(t, "ada", true, "phone")
	bo := ts.signIn(t, "bo", false, "laptop")

	p, code := ts.startPairing(t, "192.0.2.7:4000", "tv-1")
	if code != 200 || len(p.Code) != 6 || !strings.HasPrefix(p.Secret, "s_") || p.ExpiresIn != 600 || p.PollEvery != 2 {
		t.Fatalf("start: %d %+v", code, p)
	}
	poll := func(secret string) int {
		t.Helper()
		return ts.call(t, "POST", "/api/v1/auth/pair/poll", "", "", map[string]string{"code": p.Code, "secret": secret}, nil).Code
	}
	if c := poll(p.Secret); c != 202 {
		t.Fatalf("before approval: %d", c)
	}

	// Looked up as typed, in small letters with a dash.
	var look struct {
		Code   string            `json:"code"`
		Device map[string]string `json:"device"`
		Expire int               `json:"expires_in"`
	}
	typed := strings.ToLower(p.Code[:3] + "-" + p.Code[3:])
	if rec := ts.call(t, "GET", "/api/v1/auth/pair/"+typed, bo, "", nil, &look); rec.Code != 200 || look.Code != p.Code ||
		look.Device["name"] != "Bedroom TV" || look.Device["client"] != "runnel-androidtv/1.0.0" || look.Expire != 600 {
		t.Fatalf("lookup: %d %+v", rec.Code, look)
	}
	// Only an admin signs a device in as someone else.
	var e apiError
	if rec := ts.call(t, "POST", "/api/v1/auth/pair/approve", bo, "", map[string]any{"code": p.Code, "user_id": 1}, &e); rec.Code != 403 {
		t.Fatalf("bo for ada: %d %+v", rec.Code, e)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/pair/approve", bo, "", map[string]any{"code": p.Code}, nil); rec.Code != 204 {
		t.Fatalf("approve: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/pair/approve", ada, "", map[string]any{"code": p.Code}, &e); rec.Code != 409 {
		t.Fatalf("approved twice: %d", rec.Code)
	}
	if c := poll("s_" + strings.Repeat("0", 32)); c != 404 {
		t.Fatalf("a wrong secret: %d", c)
	}
	var signed loginAnswer
	if rec := ts.call(t, "POST", "/api/v1/auth/pair/poll", "", "", map[string]string{"code": p.Code, "secret": p.Secret}, &signed); rec.Code != 200 ||
		signed.User.Name != "bo" {
		t.Fatalf("collect: %d %+v", rec.Code, signed)
	}
	var me struct {
		User   map[string]any `json:"user"`
		Device myDeviceBody   `json:"device"`
	}
	if ts.call(t, "GET", "/api/v1/auth/me", signed.Token, "", nil, &me); me.User["name"] != "bo" || me.Device.Name != "Bedroom TV" || !me.Device.Current {
		t.Fatalf("the paired device: %+v", me)
	}
	// Spent: the code answers 410 for an hour, then not at all.
	if c := poll(p.Secret); c != 410 {
		t.Fatalf("polled again: %d", c)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/pair/"+p.Code, ada, "", nil, nil); rec.Code != 410 {
		t.Fatalf("lookup of a spent code: %d", rec.Code)
	}
	ts.advance(61 * time.Minute)
	if rec := ts.call(t, "GET", "/api/v1/auth/pair/"+p.Code, ada, "", nil, nil); rec.Code != 404 {
		t.Fatalf("an hour after: %d", rec.Code)
	}

	// A code runs out after ten minutes.
	q, _ := ts.startPairing(t, "192.0.2.8:4000", "tv-2")
	ts.advance(11 * time.Minute)
	if rec := ts.call(t, "POST", "/api/v1/auth/pair/poll", "", "", map[string]string{"code": q.Code, "secret": q.Secret}, nil); rec.Code != 410 {
		t.Fatalf("an old code: %d", rec.Code)
	}

	// The admin's list of what waits, and an admin approving for someone.
	r, _ := ts.startPairing(t, "192.0.2.9:4000", "tv-3")
	var list struct {
		Pairings []struct {
			Code   string            `json:"code"`
			Device map[string]string `json:"device"`
		} `json:"pairings"`
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/pairings", ada, "", nil, &list); rec.Code != 200 || len(list.Pairings) != 1 || list.Pairings[0].Code != r.Code {
		t.Fatalf("waiting: %d %+v", rec.Code, list)
	}
	if rec := ts.call(t, "GET", "/api/v1/admin/pairings", bo, "", nil, nil); rec.Code != 403 {
		t.Fatalf("not an admin: %d", rec.Code)
	}
	if rec := ts.call(t, "POST", "/api/v1/auth/pair/approve", ada, "", map[string]any{"code": r.Code, "user_id": 2}, nil); rec.Code != 204 {
		t.Fatalf("for bo: %d", rec.Code)
	}
	ts.call(t, "POST", "/api/v1/auth/pair/poll", "", "", map[string]string{"code": r.Code, "secret": r.Secret}, &signed)
	if signed.User.Name != "bo" {
		t.Fatalf("signed in as %+v", signed.User)
	}
	for _, c := range []struct {
		method, target, token string
		body                  any
		want                  int
	}{
		{"POST", "/api/v1/auth/pair/start", "", map[string]any{"device": map[string]string{}}, 400},
		{"POST", "/api/v1/auth/pair/start", "", map[string]any{"device": map[string]string{"id": strings.Repeat("d", 201)}}, 400},
		{"GET", "/api/v1/auth/pair/ABCDEF", "", nil, 401},
		{"GET", "/api/v1/auth/pair/ABC", ada, nil, 404},
		{"POST", "/api/v1/auth/pair/approve", ada, map[string]any{"code": "ABCDEF"}, 404},
		{"POST", "/api/v1/auth/pair/approve", ada, map[string]any{"code": "ABCDEF", "user_id": 99}, 400},
		{"POST", "/api/v1/auth/pair/poll", "", map[string]any{"code": "ABCDEF", "secret": "s_x"}, 404},
	} {
		if rec := ts.call(t, c.method, c.target, c.token, "", c.body, nil); rec.Code != c.want {
			t.Errorf("%s %s %v: %d, want %d", c.method, c.target, c.body, rec.Code, c.want)
		}
	}
}

func TestPairingLimits(t *testing.T) {
	ts := newTestServer(t)
	ada := ts.signIn(t, "ada", true, "phone")
	// Ten starts a minute from one address.
	for i := range 10 {
		if _, code := ts.startPairing(t, "192.0.2.1:1", "tv-"+strconv.Itoa(i)); code != 200 {
			t.Fatalf("start %d: %d", i, code)
		}
	}
	rec := ts.call(t, "POST", "/api/v1/auth/pair/start", "", "192.0.2.1:1", map[string]any{"device": map[string]string{"id": "tv-x"}}, nil)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the eleventh start: %d", rec.Code)
	}
	// Twenty waiting at once, whoever starts them.
	for i := range 10 {
		ts.startPairing(t, "192.0.2.2:1", "tv-b"+strconv.Itoa(i))
	}
	if _, code := ts.startPairing(t, "192.0.2.3:1", "tv-c"); code != 429 {
		t.Fatalf("the twenty-first waiting: %d", code)
	}
	// Ten lookups a minute for one token.
	for i := range 10 {
		ts.call(t, "GET", "/api/v1/auth/pair/ABCDE"+string(rune('A'+i)), ada, "", nil, nil)
	}
	if rec := ts.call(t, "GET", "/api/v1/auth/pair/ABCDEZ", ada, "", nil, nil); rec.Code != 429 {
		t.Fatalf("the eleventh lookup: %d", rec.Code)
	}
	ts.advance(time.Minute)
	if rec := ts.call(t, "GET", "/api/v1/auth/pair/ABCDEZ", ada, "", nil, nil); rec.Code != 404 {
		t.Fatalf("a minute later: %d", rec.Code)
	}
}

// TestLocalTokenBehindAProxy checks the rule of docs/02-architecture.md,
// section 10: the local admin token approves a pairing from the server's
// own host, but not on a request that a reverse proxy passed on.
func TestLocalTokenBehindAProxy(t *testing.T) {
	ts := newTestServer(t)
	ts.signIn(t, "ada", true, "phone")
	p, _ := ts.startPairing(t, "192.0.2.4:1", "tv")
	approve := func(header string, body map[string]any) int {
		req := httptest.NewRequest("POST", "/api/v1/auth/pair/approve", strings.NewReader(string(mustJSON(body))))
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("Authorization", "Bearer "+ts.adminToken)
		if header != "" {
			req.Header.Set(header, "for=198.51.100.9")
		}
		rec := httptest.NewRecorder()
		ts.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	for _, h := range []string{"Forwarded", "X-Forwarded-For"} {
		if code := approve(h, map[string]any{"code": p.Code, "user_id": 1}); code != 401 {
			t.Fatalf("with %s: %d", h, code)
		}
	}
	if code := approve("", map[string]any{"code": p.Code}); code != 400 {
		t.Fatalf("without user_id: %d", code)
	}
	if code := approve("", map[string]any{"code": p.Code, "user_id": 1}); code != 204 {
		t.Fatalf("from the server's own host: %d", code)
	}
	// The limits stay keyed by the address a request arrived from, never by
	// what a header says.
	for i := range 10 {
		req := httptest.NewRequest("POST", "/api/v1/auth/pair/start", strings.NewReader(`{"device":{"id":"d`+strconv.Itoa(i)+`"}}`))
		req.RemoteAddr = "127.0.0.1:5001"
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		ts.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest("POST", "/api/v1/auth/pair/start", strings.NewReader(`{"device":{"id":"d-last"}}`))
	req.RemoteAddr = "127.0.0.1:5001"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	rec := httptest.NewRecorder()
	ts.Handler().ServeHTTP(rec, req)
	if rec.Code != 429 {
		t.Fatalf("a new header gets around the limit: %d", rec.Code)
	}
}
