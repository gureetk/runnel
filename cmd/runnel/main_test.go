// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
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
	tm "github.com/gureetk/runnel/internal/testmedia"
)

func TestVersionHasADefault(t *testing.T) {
	if version == "" {
		t.Fatal("version must never be empty")
	}
}

func TestCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, os.Stdin, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Errorf("version: %d %q", code, out.String())
	}
	if code := run([]string{"frobnicate"}, os.Stdin, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("unknown command: %d %q", code, errOut.String())
	}
	if code := run([]string{"user", "remove", "ada"}, os.Stdin, &out, &errOut); code != 2 {
		t.Errorf("user remove: %d", code)
	}
	if code := run(nil, os.Stdin, &out, &errOut); code != 2 {
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

// running is a server started inside the test.
type running struct {
	addr string
	data string
	logs *logBuffer
	stop func() error // stops the server and returns what runServer returned
}

func startServer(t *testing.T) *running {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	cfg := config.Config{Listen: "127.0.0.1:0", DataDir: data, Name: "Test", LogLevel: slog.LevelInfo}
	logs := &logBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))

	ctx, cancel := context.WithCancel(context.Background())
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
		cancel()
		t.Fatalf("the server did not start:\n%s", logs.String())
	}
	stopped := false
	r := &running{addr: addr, data: data, logs: logs}
	r.stop = func() error {
		if stopped {
			return nil
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("the server took more than 5 seconds to stop")
			return nil
		}
	}
	t.Cleanup(func() { r.stop() })
	return r
}

func TestServeStartsAnswersAndStops(t *testing.T) {
	srv := startServer(t)

	resp, err := http.Get("http://" + srv.addr + "/api/v1/server")
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

	// The data directory holds the database and the local admin token,
	// readable by their owner only.
	for name, perm := range map[string]os.FileMode{"": 0o700, "admin.token": 0o600} {
		fi, err := os.Stat(filepath.Join(srv.data, name))
		if err != nil || fi.Mode().Perm() != perm {
			t.Fatalf("%q: %v %v, want %v", name, fi.Mode(), err, perm)
		}
	}
	if _, err := os.Stat(filepath.Join(srv.data, "runnel.db")); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	t.Logf("stopped in %v", time.Since(start))
}

func TestUserAdd(t *testing.T) {
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
	if out.String() != "Added user 1, ada, an admin.\n" {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	if err := userAdd(ctx, client, "noor", "another horse", false, &out); err != nil || out.String() != "Added user 2, noor.\n" {
		t.Fatalf("second user: %q %v", out.String(), err)
	}

	var e *apiError
	if err := userAdd(ctx, client, "ADA", "correct horse", false, &out); !errors.As(err, &e) || e.Code != "conflict" {
		t.Fatalf("a taken name: %v", err)
	}
	if err := userAdd(ctx, client, "sam", "short", false, &out); !errors.As(err, &e) || e.Code != "weak_password" {
		t.Fatalf("a short password: %v", err)
	}

	// The user can now sign in.
	resp, err := http.Post("http://"+srv.addr+"/api/v1/auth/login", "application/json", strings.NewReader(
		`{"name":"ada","password":"correct horse","device":{"id":"test-device"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("sign-in: %d", resp.StatusCode)
	}

	// With the server stopped, the subcommand says so.
	srv.stop()
	if err := userAdd(ctx, client, "kim", "correct horse", false, &out); err == nil || !strings.Contains(err.Error(), "is runnel serve running?") {
		t.Fatalf("with the server stopped: %v", err)
	}
}

func TestAdminClientNeedsTheToken(t *testing.T) {
	_, err := newAdminClient(config.Config{Listen: ":8610", DataDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "is the server running") {
		t.Fatalf("no admin.token: %v", err)
	}
}

func TestLocalAddress(t *testing.T) {
	for in, want := range map[string]string{
		":8610":             "127.0.0.1:8610",
		"0.0.0.0:8610":      "127.0.0.1:8610",
		"[::]:8610":         "127.0.0.1:8610",
		"192.0.2.10:8610":   "192.0.2.10:8610",
		"[2001:db8::1]:900": "[2001:db8::1]:900",
	} {
		if got := localAddress(in); got != want {
			t.Errorf("localAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadPasswordFromAPipe(t *testing.T) {
	for in, want := range map[string]string{"correct horse\n": "correct horse", "correct horse\r\n": "correct horse", "no newline": "no newline", "\n": ""} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		w.WriteString(in)
		w.Close()
		got, err := readPassword(r, io.Discard, "ada")
		r.Close()
		if want == "" {
			if err == nil {
				t.Errorf("%q: no error", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
}

func TestParseArgs(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "")
	pos, err := parseArgs(fs, []string{"ada", "--admin"})
	if err != nil || len(pos) != 1 || pos[0] != "ada" || !*admin {
		t.Fatalf("%v %v %v", pos, *admin, err)
	}
}

// waitFor waits until the server's log has a line that matches re.
func (r *running) waitFor(t *testing.T, re string) string {
	t.Helper()
	pattern := regexp.MustCompile(re)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m := pattern.FindString(r.logs.String()); m != "" {
			return m
		}
	}
	t.Fatalf("no log line matching %q:\n%s", re, r.logs.String())
	return ""
}

func TestLibraryAddAndScan(t *testing.T) {
	tm.Tools(t)
	srv := startServer(t)
	ctx := context.Background()
	client, err := newAdminClient(config.Config{Listen: srv.addr, DataDir: srv.data})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "films")
	for _, rel := range []string{"Invented Film (2001)/Invented Film (2001).mkv", "Lone Film (1999).mp4"} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		tm.Make(t, p, tm.Join(tm.Video("1"), tm.Tone("440", "1"), []string{"-c:v", "libx264", "-c:a", "aac"})...)
	}

	var out bytes.Buffer
	if err := libraryAdd(ctx, client, "Films", "movies", "", []string{root}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Added library 1, Films (movies), with folders: 1 "+root+".") {
		t.Fatalf("output %q", out.String())
	}
	srv.waitFor(t, `msg="scan done" root=\S+ ms=\d+ files=2 added=2`)
	srv.waitFor(t, `msg="probing done" probed=2 unreadable=0`)

	out.Reset()
	if err := scanRequest(ctx, client, 1, &out); err != nil || out.String() != "Asked the server to scan library 1.\n" {
		t.Fatalf("scan: %q %v", out.String(), err)
	}
	srv.waitFor(t, `msg="scan done" root=\S+ ms=\d+ files=2 added=0 back=0 changed=0 moved=0 missing=0`)
	var e *apiError
	if err := scanRequest(ctx, client, 7, &out); err == nil || !strings.Contains(err.Error(), "no library 7") {
		t.Fatalf("an unknown library: %v (%v)", err, errors.As(err, &e))
	}

	// Items were built, and the report says how the names parsed.
	srv.waitFor(t, `msg="items built" library=1 ms=\d+ added=2`)
	out.Reset()
	if err := report(ctx, client, 0, "all", false, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Library 1, Films (movies): 2 files. explicit 2, anchored 0, dash 0, guess 0, none 0. With a note 0. ffprobe could not read 0.",
		"explicit\t1\tInvented Film (2001)\tInvented Film (2001)/Invented Film (2001).mkv",
		"explicit\t2\tLone Film (1999)\tLone Film (1999).mp4",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the report lacks %q:\n%s", want, out.String())
		}
	}
}
