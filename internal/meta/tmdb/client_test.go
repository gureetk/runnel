// SPDX-License-Identifier: GPL-3.0-or-later

package tmdb

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb/standin"
	"github.com/gureetk/runnel/internal/tasks"
)

// Invented credentials, in the two forms TMDB gives them.
const (
	testToken = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJpbnZlbnRlZCIsInN1YiI6InRlc3QifQ.aW52ZW50ZWQtc2lnbmF0dXJl"
	testKey   = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

const month = 30 * 24 * time.Hour

type env struct {
	db     *db.DB
	tmdb   *standin.Server
	client *Client
	logs   *bytes.Buffer
	key    string

	mu  sync.Mutex
	now time.Time
}

func newEnv(t *testing.T, key string) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &env{db: d, key: key, logs: &bytes.Buffer{}, now: time.UnixMilli(1795200000000)}
	e.tmdb = &standin.Server{Token: testToken, APIKey: testKey, Pages: map[string]string{
		"/movie/101": `{"id": 101, "title": "A Lantern at Low Tide", "release_date": "2001-03-04"}`,
	}}
	srv := e.tmdb.Start(t)
	log := slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.client = New(d, func(context.Context) (string, error) { return e.key, nil }, "test", log)
	e.client.SetBase(srv.URL + "/3")
	e.client.now = e.clock
	return e
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) later(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// noKey checks that neither credential is in the log.
func (e *env) noKey(t *testing.T) {
	t.Helper()
	if s := e.logs.String(); strings.Contains(s, testKey) || strings.Contains(s, testToken) {
		t.Errorf("a key is in the log: %s", s)
	}
}

func TestForms(t *testing.T) {
	if !IsToken(testToken) || IsToken(testKey) || IsToken("eyJ.a") {
		t.Error("the forms are told apart wrongly")
	}
	for key, want := range map[string]bool{
		testToken: true, testKey: true, "x": true,
		"": false, "with space": false, "tab\there": false, "dé": false, strings.Repeat("k", 1001): false,
	} {
		if ValidKey(key) != want {
			t.Errorf("ValidKey(%q) = %v", key, !want)
		}
	}
}

// TestHowTheKeyIsSent: a read access token goes in the Authorization
// header, an API key in the query, and neither in the other place.
func TestHowTheKeyIsSent(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{testToken, testKey} {
		e := newEnv(t, key)
		got, err := e.client.Get(ctx, "/movie/101", nil, month)
		if err != nil || got.Status != http.StatusOK || !strings.Contains(string(got.Body), "Lantern") {
			t.Fatalf("%v, %d %s", err, got.Status, got.Body)
		}
		r := e.tmdb.Requests()[0]
		bearer, inQuery := r.Header.Get("Authorization"), r.URL.Query().Get("api_key")
		if key == testToken && (bearer != "Bearer "+testToken || inQuery != "") {
			t.Errorf("a token is sent as %q and api_key=%q", bearer, inQuery)
		}
		if key == testKey && (bearer != "" || inQuery != testKey) {
			t.Errorf("an API key is sent as %q and api_key=%q", bearer, inQuery)
		}
		if r.Header.Get("User-Agent") != "runnel/test" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		e.noKey(t)
	}
}

// TestTheCache: an answer, a 404 too, is read from provider_cache while
// it is young enough, and asked for again after; the cache's key holds
// the query without the credential.
func TestTheCache(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, testKey)
	q := url.Values{"query": {"a lantern"}, "year": {"2001"}}
	e.tmdb.Set("/search/movie?query=a+lantern&year=2001", `{"page": 1, "results": [{"id": 101}]}`)
	for range 2 {
		if got, err := e.client.Get(ctx, "/search/movie", q, month); err != nil || got.Status != 200 {
			t.Fatal(got.Status, err)
		}
		if got, err := e.client.Get(ctx, "/movie/999", nil, month); err != nil || got.Status != 404 {
			t.Fatal(got.Status, err)
		}
	}
	if n := len(e.tmdb.Requests()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
	cached, found, err := e.db.CacheGet(ctx, Provider, "/search/movie?query=a+lantern&year=2001")
	if err != nil || !found || cached.FetchedAt != e.clock().UnixMilli() {
		t.Errorf("the cache's key: %v %v %+v", found, err, cached)
	}
	e.later(month + time.Minute)
	if _, err := e.client.Get(ctx, "/search/movie", q, month); err != nil {
		t.Fatal(err)
	}
	if n := len(e.tmdb.Requests()); n != 3 {
		t.Errorf("%d requests, want 3: an answer older than allowed is asked for again", n)
	}
	// An answer too old is not given in place of one that cannot be had:
	// the caller keeps what it has.
	e.tmdb.Fail = http.StatusInternalServerError
	if got, err := e.client.Get(ctx, "/movie/999", nil, month); err == nil {
		t.Errorf("a failing provider answered %d", got.Status)
	}
	if n, err := e.db.PruneCache(ctx, e.clock().Add(-time.Hour).UnixMilli()); err != nil || n != 1 {
		t.Errorf("pruned %d, %v; want the 404 fetched a month ago", n, err)
	}
	e.noKey(t)
}

// TestARefusedKey: a 401 is ErrBadKey and marks the key as not working;
// the next request that works marks it as working.
func TestARefusedKey(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "f0e1d2c3b4a5968778695a4b3c2d1e0f")
	if _, err := e.client.Get(ctx, "/movie/101", nil, month); !errors.Is(err, ErrBadKey) {
		t.Fatalf("got %v, want ErrBadKey", err)
	}
	_, s, _ := e.db.MetadataKey(ctx)
	if s.OK == nil || *s.OK || s.CheckedAt == nil || *s.CheckedAt != e.clock().UnixMilli() {
		t.Errorf("status %+v after a 401", s)
	}
	e.key = testKey
	e.later(time.Minute)
	if _, err := e.client.Get(ctx, "/movie/101", nil, month); err != nil {
		t.Fatal(err)
	}
	_, s, _ = e.db.MetadataKey(ctx)
	if s.OK == nil || !*s.OK || *s.CheckedAt != e.clock().UnixMilli() {
		t.Errorf("status %+v after a 200", s)
	}
	if !strings.Contains(e.logs.String(), "refuses the metadata key") {
		t.Error("a refused key is not logged")
	}
	e.noKey(t)
}

// TestTheStatusIsWrittenSeldom: a key that keeps working has its status
// written again at most every ten minutes.
func TestTheStatusIsWrittenSeldom(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, testKey)
	first := e.clock().UnixMilli()
	for i, step := range []time.Duration{0, time.Minute, 5 * time.Minute, 5 * time.Minute} {
		e.later(step)
		if _, err := e.client.Get(ctx, "/movie/101", nil, 0); err != nil {
			t.Fatal(err)
		}
		_, s, _ := e.db.MetadataKey(ctx)
		want := first
		if i == 3 {
			want = e.clock().UnixMilli()
		}
		if s.CheckedAt == nil || *s.CheckedAt != want {
			t.Errorf("request %d: checked at %v, want %d", i, s.CheckedAt, want)
		}
	}
	// A key that changed is written at its first request.
	e.client.Forget()
	e.later(time.Second)
	if _, err := e.client.Get(ctx, "/movie/101", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, s, _ := e.db.MetadataKey(ctx); *s.CheckedAt != e.clock().UnixMilli() {
		t.Error("the status is not written after Forget")
	}
}

// TestWaiting: after a 429 no request is sent until Retry-After, or for a
// pause that doubles without one; an answer resets it.
func TestWaiting(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, testKey)
	e.tmdb.Busy = []standin.Busy{{RetryAfter: "30"}, {}, {}}
	busy := func(want time.Duration) {
		t.Helper()
		start := e.clock()
		_, err := e.client.Get(ctx, "/movie/101", nil, 0)
		var b *Busy
		if !errors.As(err, &b) || !b.Until.Equal(start.Add(want)) {
			t.Fatalf("got %v, want a wait of %v", err, want)
		}
		// Asked again before then, it sends nothing.
		n := len(e.tmdb.Requests())
		if _, err := e.client.Get(ctx, "/movie/101", nil, 0); !errors.As(err, &b) || len(e.tmdb.Requests()) != n {
			t.Fatalf("sent a request while asked to wait: %v", err)
		}
		e.later(want)
	}
	busy(30 * time.Second)
	busy(firstPause)
	busy(2 * firstPause)
	if _, err := e.client.Get(ctx, "/movie/101", nil, 0); err != nil {
		t.Fatal(err)
	}
	e.tmdb.Busy = []standin.Busy{{RetryAfter: "Thu, 01 Jan 2099 00:00:00 GMT"}, {}}
	busy(maxRetry)
	busy(firstPause)

	err := AsPause(&Busy{Until: e.clock().Add(time.Minute)}, e.clock())
	var p *tasks.Pause
	if !errors.As(err, &p) || p.For != time.Minute {
		t.Errorf("AsPause gave %v", err)
	}
	if err := AsPause(ErrBadKey, e.clock()); err != ErrBadKey {
		t.Errorf("AsPause changed %v", err)
	}
	e.noKey(t)
}

// TestUnreachable: a provider that fails or cannot be reached is an
// UpstreamError whose message holds no key.
func TestUnreachable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, testKey)
	e.tmdb.Fail = http.StatusBadGateway
	_, err := e.client.Get(ctx, "/movie/101", nil, 0)
	var up *UpstreamError
	if !errors.As(err, &up) || !strings.Contains(err.Error(), "502") {
		t.Errorf("got %v", err)
	}
	gone := &standin.Server{}
	srv := gone.Start(t)
	srv.Close()
	e.client.SetBase(srv.URL + "/3")
	_, err = e.client.Get(ctx, "/movie/101", nil, 0)
	if !errors.As(err, &up) || strings.Contains(err.Error(), testKey) {
		t.Errorf("got %v", err)
	}
	if err := e.client.Check(ctx, testToken); !errors.As(err, &up) || strings.Contains(err.Error(), testToken) {
		t.Errorf("got %v", err)
	}
	e.noKey(t)
}

// TestNoKey: without a key nothing is asked.
func TestNoKey(t *testing.T) {
	e := newEnv(t, "")
	if _, err := e.client.Get(context.Background(), "/movie/101", nil, 0); !errors.Is(err, ErrNoKey) {
		t.Errorf("got %v", err)
	}
	if n := len(e.tmdb.Requests()); n != 0 {
		t.Errorf("%d requests without a key", n)
	}
}

// TestCheck: a key is checked with one GET /configuration, kept in the
// cache when TMDB takes the key.
func TestCheck(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "")
	if err := e.client.Check(ctx, "not a key"); !errors.Is(err, ErrBadKey) || len(e.tmdb.Requests()) != 0 {
		t.Errorf("a key of the wrong form: %v, %d requests", err, len(e.tmdb.Requests()))
	}
	if err := e.client.Check(ctx, "f0e1d2c3b4a5968778695a4b3c2d1e0f"); !errors.Is(err, ErrBadKey) {
		t.Errorf("a refused key: %v", err)
	}
	if _, found, _ := e.db.CacheGet(ctx, Provider, "/configuration"); found {
		t.Error("a refused key's check is kept")
	}
	for _, key := range []string{testToken, testKey} {
		if err := e.client.Check(ctx, key); err != nil {
			t.Errorf("a good key: %v", err)
		}
	}
	got, found, _ := e.db.CacheGet(ctx, Provider, "/configuration")
	if !found || !strings.Contains(string(got.Body), "still_sizes") {
		t.Error("the configuration is not kept")
	}
	for _, r := range e.tmdb.Requests() {
		if r.URL.Path != "/3/configuration" {
			t.Errorf("checked with %s", r.URL.Path)
		}
	}
	e.noKey(t)
}

// TestStored: the environment's key wins over the one an admin set.
func TestStored(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "")
	set := "an-invented-key-set-by-an-admin"
	if err := e.db.SetMetadataKey(ctx, &set, 1); err != nil {
		t.Fatal(err)
	}
	if k, err := Stored(e.db, "")(ctx); err != nil || k != set {
		t.Errorf("%q %v", k, err)
	}
	if k, _ := Stored(e.db, "an-invented-key-from-the-environment")(ctx); k != "an-invented-key-from-the-environment" {
		t.Errorf("%q", k)
	}
}
