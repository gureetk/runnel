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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/config"
	"github.com/gureetk/runnel/internal/meta/jellyfin/standin"
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
	for _, args := range [][]string{{"user", "remove", "ada"}, {"user"}, {"user", "list", "ada"}, {"user", "passwd"},
		{"user", "passwd", "ada", "--admin"}} {
		if code := run(args, os.Stdin, &out, &errOut); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
	}
	for _, args := range [][]string{{"library"}, {"library", "list", "extra"}, {"library", "remove"}, {"library", "remove", "x"},
		{"library", "remove", "0"}, {"library", "rename", "1"}, {"import"}, {"import", "plex"}, {"import", "jellyfin"},
		{"import", "jellyfin", "users", "--url", "http://jf.lan", "--write"}, {"import", "jellyfin", "--url", "http://jf.lan", "--user", "ada"}} {
		if code := run(args, os.Stdin, &out, &errOut); code != 2 {
			t.Errorf("%v: %d", args, code)
		}
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
	signIn := func(name, password string) int {
		t.Helper()
		resp, err := http.Post("http://"+srv.addr+"/api/v1/auth/login", "application/json", strings.NewReader(
			`{"name":"`+name+`","password":"`+password+`","device":{"id":"test-device"}}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := signIn("ada", "correct horse"); code != 200 {
		t.Fatalf("sign-in: %d", code)
	}

	// A new password, for a name in any case; the list.
	out.Reset()
	if err := userPasswd(ctx, client, "NOOR", "a third horse", &out); err != nil ||
		out.String() != "Set a new password for noor. Their devices stay signed in.\n" {
		t.Fatalf("passwd: %q %v", out.String(), err)
	}
	if signIn("noor", "another horse") != 401 || signIn("noor", "a third horse") != 200 {
		t.Fatal("the new password is not the one that signs in")
	}
	if err := userPasswd(ctx, client, "kim", "a third horse", &out); err == nil || !strings.Contains(err.Error(), "no user named kim") {
		t.Fatalf("passwd for nobody: %v", err)
	}
	if err := userPasswd(ctx, client, "noor", "short", &out); !errors.As(err, &e) || e.Code != "weak_password" {
		t.Fatalf("passwd, a short password: %v", err)
	}
	out.Reset()
	if err := userList(ctx, client, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "1  ada: admin, signs in with a password; 1 device, last active ") ||
		!strings.HasPrefix(lines[1], "2  noor: signs in with a password; 1 device, last active ") {
		t.Fatalf("list:\n%s", out.String())
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
	tm.Settle(t, root)

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

	// The list, once the second scan has built its items, and the
	// library's removal.
	srv.waitFor(t, `msg="items built" library=1 ms=\d+ added=0 changed=0 removed=0`)
	out.Reset()
	if err := libraryList(ctx, client, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1  Films (movies): 2 films and series; scan: idle\n", "   folder 1  " + root + "  scanned ",
		" ms: 0 added, 0 changed, 0 removed\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the list lacks %q:\n%s", want, out.String())
		}
	}
	if err := libraryRemove(ctx, client, 1, false, nil, &out, io.Discard); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("without a terminal or --yes: %v", err)
	}
	if err := libraryRemove(ctx, client, 9, true, nil, &out, io.Discard); err == nil || !strings.Contains(err.Error(), "no library 9") {
		t.Fatalf("an unknown library: %v", err)
	}
	out.Reset()
	if err := libraryRemove(ctx, client, 1, true, nil, &out, io.Discard); err != nil || out.String() != "Removed library 1, Films.\n" {
		t.Fatalf("remove: %q %v", out.String(), err)
	}
	out.Reset()
	if err := libraryList(ctx, client, &out); err != nil || !strings.HasPrefix(out.String(), "There is no library yet.") {
		t.Fatalf("an empty list: %q %v", out.String(), err)
	}
}

// TestANewFileIsPlayableSoon is stage 2's sixth exit criterion in the
// test's own sandbox: a file written into a library is playable through
// the API within 15 seconds, without a scan being asked for
// (docs/01-roadmap.md, section 4).
func TestANewFileIsPlayableSoon(t *testing.T) {
	tm.Tools(t)
	srv := startServer(t)
	ctx := context.Background()
	client, err := newAdminClient(config.Config{Listen: srv.addr, DataDir: srv.data})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "films")
	first := filepath.Join(root, "Invented Film (2001)", "Invented Film (2001).mkv")
	os.MkdirAll(filepath.Dir(first), 0o755)
	tm.Make(t, first, tm.Join(tm.Video("1"), tm.Tone("440", "1"), []string{"-c:v", "libx264", "-c:a", "aac"})...)
	tm.Settle(t, root)
	if err := userAdd(ctx, client, "ada", "correct horse", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := libraryAdd(ctx, client, "Films", "movies", "", []string{root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	srv.waitFor(t, `msg="probing done" probed=1 unreadable=0`)

	// Sign in as a client does.
	base := "http://" + srv.addr + "/api/v1"
	body := `{"name":"ada","password":"correct horse","device":{"id":"test"}}`
	resp, err := http.Post(base+"/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	get := func(path string, out any) {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set("Authorization", "Bearer "+login.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(out)
	}

	// A new film is written into the library as a copy is: a file whose
	// time is now.
	made := filepath.Join(t.TempDir(), "made.mkv")
	tm.Make(t, made, tm.Join(tm.Video("1"), tm.Tone("440", "1"), []string{"-c:v", "libx264", "-c:a", "aac"})...)
	b, _ := os.ReadFile(made)
	start := time.Now()
	dir := filepath.Join(root, "Paper Comets (2019)")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "Paper Comets (2019).mkv"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for time.Since(start) < 20*time.Second {
		var page struct {
			Items []struct {
				ID    int64
				Title string
			}
		}
		get("/items?library=1", &page)
		for _, it := range page.Items {
			if it.Title != "Paper Comets" {
				continue
			}
			var d struct{ Files []struct{ ID int64 } }
			get("/items/"+strconv.FormatInt(it.ID, 10), &d)
			if len(d.Files) == 1 {
				took := time.Since(start)
				t.Logf("playable after %v", took.Round(10*time.Millisecond))
				if took > 15*time.Second {
					t.Fatalf("playable after %v, more than 15 seconds", took)
				}
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("not playable after 20 seconds:\n%s", srv.logs.String())
}

func TestImportJellyfin(t *testing.T) {
	srv := startServer(t)
	ctx := context.Background()
	client, err := newAdminClient(config.Config{Listen: srv.addr, DataDir: srv.data})
	if err != nil {
		t.Fatal(err)
	}
	pollEvery = 10 * time.Millisecond
	if err := userAdd(ctx, client, "ada", "correct horse", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "films")
	film := filepath.Join(root, "Low Water (2001)", "Low Water (2001).mkv")
	os.MkdirAll(filepath.Dir(film), 0o755)
	os.WriteFile(film, []byte("not a video"), 0o644)
	tm.Settle(t, root)
	if err := libraryAdd(ctx, client, "Films", "movies", "", []string{root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	srv.waitFor(t, `msg="items built" library=1 ms=\d+ added=1`)

	jf := &standin.Server{Key: "0123456789abcdef0123456789abcdef", Version: "12.4.0",
		Users: []standin.User{{ID: "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c", Name: "Ada"}},
		Items: []standin.Item{{ID: "a1", Type: "Movie", Path: "/films/Low Water (2001)/Low Water (2001).mkv",
			UserData: map[string]standin.UserData{"4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c": {Played: true, PlayCount: 1, LastPlayedDate: "2026-10-01T20:00:00Z"}}},
			{ID: "a2", Type: "Movie", Path: "/films/Gone (2000)/Gone (2000).mkv"}}}
	url := jf.Start(t).URL

	var out bytes.Buffer
	if err := jellyfinUsers(ctx, client, url, jf.Key, &out); err != nil || out.String() != "Jellyfin 12.4.0 has 1 users:\n  Ada\n" {
		t.Fatalf("users: %q %v", out.String(), err)
	}
	body := map[string]any{"url": url, "api_key": jf.Key, "users": map[string]string{"ada": "ada"},
		"paths": []map[string]string{{"from": "/films", "to": root}}, "dry_run": true}
	out.Reset()
	if err := importJellyfin(ctx, client, body, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"A dry run against Jellyfin 12.4.0: nothing was written.", "Films and episodes: 1 joined, 1 not (50.0% joined).",
		"Play states for ada: 1.", "Not joined (1):\n  movie\t/films/Gone (2000)/Gone (2000).mkv\tno file at that path here\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the dry run's report lacks %q:\n%s", want, out.String())
		}
	}
	body["dry_run"] = false
	out.Reset()
	if err := importJellyfin(ctx, client, body, &out); err != nil || !strings.HasPrefix(out.String(), "Imported from Jellyfin 12.4.0.\n") ||
		!strings.Contains(out.String(), "Play states for ada: 1.") {
		t.Fatalf("the import: %q %v", out.String(), err)
	}
	body["api_key"] = "wrong-key"
	var e *apiError
	if err := importJellyfin(ctx, client, body, &out); !errors.As(err, &e) || e.Code != "bad_key" {
		t.Fatalf("a wrong key: %v", err)
	}
	srv.waitFor(t, `msg="Jellyfin import done" dry_run=false joined=1 not_joined=1`)
}
