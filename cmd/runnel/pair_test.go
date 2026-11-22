// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/config"
)

func TestPairApprove(t *testing.T) {
	srv := startServer(t)
	ctx := context.Background()
	client, err := newAdminClient(config.Config{Listen: srv.addr, DataDir: srv.data})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := userAdd(ctx, client, "ada", "correct horse", false, &out); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string, v any) int {
		t.Helper()
		resp, err := http.Post("http://"+srv.addr+"/api/v1"+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if v != nil {
			json.NewDecoder(resp.Body).Decode(v)
		}
		return resp.StatusCode
	}
	var p struct{ Code, Secret string }
	if code := post("/auth/pair/start", `{"device":{"id":"tv","name":"Living room TV","client":"runnel-androidtv/1.0.0"}}`, &p); code != 200 {
		t.Fatalf("start: %d", code)
	}

	out.Reset()
	if err := pairApprove(ctx, client, strings.ToLower(p.Code), "ADA", &out); err != nil {
		t.Fatal(err)
	}
	if want := "Approved " + p.Code + ": Living room TV (runnel-androidtv/1.0.0) is signed in as ada when it next asks.\n"; out.String() != want {
		t.Fatalf("printed %q, want %q", out.String(), want)
	}
	var signed struct {
		Token string
		User  struct{ Name string }
	}
	if code := post("/auth/pair/poll", `{"code":"`+p.Code+`","secret":"`+p.Secret+`"}`, &signed); code != 200 || signed.User.Name != "ada" || signed.Token == "" {
		t.Fatalf("poll: %d %+v", code, signed)
	}
	if err := pairApprove(ctx, client, p.Code, "ada", &out); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("a spent code: %v", err)
	}
	if err := pairApprove(ctx, client, p.Code, "nobody", &out); err == nil || !strings.Contains(err.Error(), "no user") {
		t.Fatalf("an unknown user: %v", err)
	}
	var errOut bytes.Buffer
	if code := run([]string{"pair", "approve", p.Code}, nil, &out, &errOut); code != 2 {
		t.Fatalf("without --user: %d", code)
	}
}
