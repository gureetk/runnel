// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/config"
)

func TestVersionHasADefault(t *testing.T) {
	if version == "" {
		t.Fatal("version must never be empty")
	}
}

func TestCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Errorf("version: %d %q", code, out.String())
	}
	if code := run([]string{"frobnicate"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("unknown command: %d %q", code, errOut.String())
	}
	if code := run(nil, &out, &errOut); code != 2 {
		t.Errorf("no command: %d", code)
	}
}

// logBuffer collects the server's log while it runs.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestServeStartsAnswersAndStops(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	cfg := config.Config{Listen: "127.0.0.1:0", DataDir: data, Name: "Test", LogLevel: slog.LevelInfo}
	logs := &logBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, log) }()

	addr := ""
	re := regexp.MustCompile(`msg=listening addr=(\S+)`)
	for deadline := time.Now().Add(10 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := re.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
		}
	}
	if addr == "" {
		t.Fatalf("the server did not start:\n%s", logs.String())
	}

	resp, err := http.Get("http://" + addr + "/api/v1/server")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Product, Name string
		SetupDone     bool `json:"setup_done"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != 200 || body.Product != "runnel" || body.Name != "Test" || body.SetupDone {
		t.Fatalf("GET /server: %d %+v", resp.StatusCode, body)
	}

	// The data directory holds the database, readable by its owner only.
	fi, err := os.Stat(data)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("data directory: %v %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(data, "runnel.db")); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server took more than 5 seconds to stop")
	}
	t.Logf("stopped in %v", time.Since(start))
}
