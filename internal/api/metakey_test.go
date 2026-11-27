// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/meta/tmdb"
	"github.com/gureetk/runnel/internal/meta/tmdb/standin"
)

// Invented credentials.
const (
	inventedToken = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJpbnZlbnRlZCJ9.aW52ZW50ZWQtc2lnbmF0dXJlLWFwaQ"
	inventedShort = "short-key"
	inventedEnv   = "an-invented-key-from-the-environment"
)

func TestMetadataKeySetting(t *testing.T) {
	ctx := context.Background()
	ts := newTestServer(t)
	ada := ts.signIn(t, "ada", true, "phone")
	bo := ts.signIn(t, "bo", false, "laptop")
	provider := &standin.Server{Token: inventedToken, APIKey: inventedShort}
	srv := provider.Start(t)
	client := tmdb.New(ts.db, tmdb.Stored(ts.db, ""), "test", slog.New(slog.NewTextHandler(ts.log, nil)))
	client.SetBase(srv.URL + "/3")
	ts.metadata = client

	type key struct {
		Set       bool    `json:"set"`
		Hint      *string `json:"hint"`
		OK        *bool   `json:"ok"`
		CheckedAt *int64  `json:"checked_at"`
		Locked    bool    `json:"locked"`
	}
	type settings struct {
		ServerName settingBody `json:"server_name"`
		TMDBKey    key         `json:"tmdb_key"`
	}
	var got settings
	patch := func(body map[string]any, want int, code string) {
		t.Helper()
		got = settings{}
		rec := ts.call(t, "PATCH", "/api/v1/admin/settings", ada, "", body, nil)
		if rec.Code != want || (code != "" && !strings.Contains(rec.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("%v: %d %s, want %d %s", body, rec.Code, rec.Body.String(), want, code)
		}
		if strings.Contains(rec.Body.String(), inventedToken) {
			t.Fatal("the key is sent back")
		}
		if want == 200 {
			mustDecode(t, rec, &got)
		}
	}
	stored := func() string {
		k, _, err := ts.db.MetadataKey(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}

	if rec := ts.call(t, "GET", "/api/v1/admin/settings", ada, "", nil, &got); rec.Code != 200 ||
		got.TMDBKey.Set || got.TMDBKey.Hint != nil || got.TMDBKey.OK != nil || got.TMDBKey.CheckedAt != nil || got.TMDBKey.Locked {
		t.Fatalf("without a key: %d %+v", rec.Code, got.TMDBKey)
	}
	if !strings.Contains(ts.call(t, "GET", "/api/v1/admin/settings", ada, "", nil, nil).Body.String(), `"hint":null`) {
		t.Error("hint is not null without a key")
	}
	if rec := ts.call(t, "PATCH", "/api/v1/admin/settings", bo, "", map[string]any{"tmdb_key": inventedToken}, nil); rec.Code != 403 {
		t.Errorf("not an admin: %d", rec.Code)
	}

	// Refused, or not of the form, nothing is kept, and nothing else is
	// written either.
	patch(map[string]any{"tmdb_key": "two words"}, 400, "bad_key")
	patch(map[string]any{"server_name": "Upstairs", "tmdb_key": "eyJhbGciOiJIUzI1NiJ9.eyJ9.bm90LWl0"}, 400, "bad_key")
	if stored() != "" {
		t.Fatal("a refused key is kept")
	}
	if n, _, _ := ts.db.Setting(ctx, "server_name"); n != "" {
		t.Fatal("the name is written beside a refused key")
	}
	requests := len(provider.Requests())

	// A key TMDB takes is kept, as working, and only its hint is shown.
	patch(map[string]any{"tmdb_key": inventedToken}, 200, "")
	if k := got.TMDBKey; !k.Set || k.Hint == nil || *k.Hint != inventedToken[len(inventedToken)-4:] || k.OK == nil || !*k.OK ||
		k.CheckedAt == nil || *k.CheckedAt != ts.now().UnixMilli() || k.Locked {
		t.Fatalf("after a good key: %+v", k)
	}
	if stored() != inventedToken || len(provider.Requests()) != requests+1 {
		t.Fatalf("the key is not kept, or was checked with %d requests", len(provider.Requests())-requests)
	}

	// A provider that fails, or asks to wait, keeps the key there was.
	provider.Fail = http.StatusInternalServerError
	patch(map[string]any{"tmdb_key": inventedShort}, 502, "upstream")
	provider.Fail = 0
	// (A client of its own, whose pause the rest of the test does not
	// wait out.)
	waiting := tmdb.New(ts.db, tmdb.Stored(ts.db, ""), "test", slog.New(slog.NewTextHandler(ts.log, nil)))
	waiting.SetBase(srv.URL + "/3")
	ts.metadata = waiting
	provider.Busy = []standin.Busy{{RetryAfter: "60"}}
	patch(map[string]any{"tmdb_key": inventedShort}, 502, "upstream")
	ts.metadata = client
	if stored() != inventedToken {
		t.Fatal("a key that could not be checked replaced the one there was")
	}

	// A short key has no hint. The key requests are made with is the
	// stored one.
	patch(map[string]any{"tmdb_key": inventedShort}, 200, "")
	if got.TMDBKey.Hint != nil || !got.TMDBKey.Set {
		t.Errorf("a short key: %+v", got.TMDBKey)
	}
	if _, err := client.Get(ctx, "/configuration", nil, 0); err != nil {
		t.Fatal(err)
	}
	if last := provider.Requests()[len(provider.Requests())-1]; last.URL.Query().Get("api_key") != inventedShort {
		t.Error("a request is not made with the key kept")
	}

	// null removes the key and its status.
	patch(map[string]any{"tmdb_key": nil}, 200, "")
	if got.TMDBKey.Set || got.TMDBKey.OK != nil || stored() != "" {
		t.Errorf("after removing: %+v", got.TMDBKey)
	}

	// The environment's key wins and locks the setting.
	ts.envKey = inventedEnv
	ts.call(t, "GET", "/api/v1/admin/settings", ada, "", nil, &got)
	if k := got.TMDBKey; !k.Set || !k.Locked || *k.Hint != inventedEnv[len(inventedEnv)-4:] {
		t.Errorf("the environment's key: %+v", k)
	}
	patch(map[string]any{"tmdb_key": inventedToken}, 409, "locked")
	patch(map[string]any{"tmdb_key": nil}, 409, "locked")

	for _, secret := range []string{inventedToken, inventedShort, inventedEnv} {
		if strings.Contains(ts.log.String(), secret) {
			t.Errorf("a key is in the log")
		}
	}
}
