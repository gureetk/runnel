// SPDX-License-Identifier: GPL-3.0-or-later

//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tm "github.com/gureetk/runnel/internal/testmedia"
)

// TestTheBuiltBinary builds runnel and drives it the way a person does:
// serve, then user add, library add, scan and report, each a process of
// its own, on a small library made with ffmpeg. The other tests run the
// server inside the test process. This one runs the binary that ships,
// with its environment, its output and its exit statuses.
func TestTheBuiltBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	tm.Tools(t)
	goTool, err := exec.LookPath("go")
	if err != nil {
		goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, err := os.Stat(goTool); err != nil {
			t.Skip("the go command is not on PATH")
		}
	}
	bin := filepath.Join(t.TempDir(), "runnel")
	build := exec.Command(goTool, "build", "-tags", "sqlite_fts5", "-ldflags", "-X main.version=v0.0.0-test", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// The subcommands find the server from RUNNEL_LISTEN, so the server gets
	// a free port of its own rather than :0.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	data := filepath.Join(t.TempDir(), "data")
	env := append(os.Environ(), "RUNNEL_LISTEN="+addr, "RUNNEL_DATA="+data, "RUNNEL_NAME=Built", "RUNNEL_LOG=info")

	logs := &logBuffer{}
	serve := exec.Command(bin, "serve")
	serve.Env = env
	serve.Stdout, serve.Stderr = logs, logs
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- serve.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			serve.Process.Kill()
			<-exited
		}
	})
	srv := &running{addr: addr, data: data, logs: logs}
	srv.waitFor(t, `msg=listening addr=`+regexp.QuoteMeta(addr))

	run := func(stdin string, args ...string) (stdout, stderr string, status int) {
		t.Helper()
		c := exec.Command(bin, args...)
		c.Env = env
		c.Stdin = strings.NewReader(stdin)
		var out, errOut bytes.Buffer
		c.Stdout, c.Stderr = &out, &errOut
		var exit *exec.ExitError
		switch err := c.Run(); {
		case errors.As(err, &exit):
			status = exit.ExitCode()
		case err != nil:
			t.Fatalf("runnel %s: %v", strings.Join(args, " "), err)
		}
		return out.String(), errOut.String(), status
	}
	// count waits until the log has at least n lines that match re.
	count := func(re string, n int) {
		t.Helper()
		pattern := regexp.MustCompile(re)
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if len(pattern.FindAllString(logs.String(), -1)) >= n {
				return
			}
		}
		t.Fatalf("fewer than %d log lines match %q:\n%s", n, re, logs.String())
	}

	if out, _, status := run("", "version"); status != 0 || strings.TrimSpace(out) != "v0.0.0-test" {
		t.Fatalf("version: %d %q", status, out)
	}
	// The check of what can encode ran before the server listened. No
	// render device is in the test's environment, so it is software.
	srv.waitFor(t, `msg="encoders checked" hw=none ms=\d+ hevc10=`)
	if out, _, status := run("", "doctor"); status != 0 || !strings.Contains(out, "ffmpeg: ") ||
		!regexp.MustCompile(`(?m)^  h264   8-bit  software  libx264     yes$`).MatchString(out) {
		t.Errorf("doctor: %d\n%s", status, out)
	}
	resp, err := http.Get("http://" + addr + "/api/v1/server")
	if err != nil {
		t.Fatal(err)
	}
	var server struct{ Version, Name string }
	json.NewDecoder(resp.Body).Decode(&server)
	resp.Body.Close()
	if server.Version != "v0.0.0-test" || server.Name != "Built" {
		t.Fatalf("GET /server: %+v", server)
	}
	for _, method := range []string{"GET", "HEAD"} {
		req, _ := http.NewRequest(method, "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if want := map[string]string{"GET": "ok\n", "HEAD": ""}[method]; resp.StatusCode != 200 || string(body) != want {
			t.Fatalf("%s /healthz: %d %q", method, resp.StatusCode, body)
		}
	}

	// user add reads the password from standard input when it is not a
	// terminal. The first user is an admin.
	if out, errOut, status := run("correct horse\n", "user", "add", "ada"); status != 0 || out != "Added user 1, ada, an admin.\n" {
		t.Fatalf("user add: %d %q %q", status, out, errOut)
	}
	if _, errOut, status := run("correct horse\n", "user", "add", "ADA"); status != 1 || !strings.Contains(errOut, "(conflict)") {
		t.Fatalf("user add, a taken name: %d %q", status, errOut)
	}
	if _, errOut, status := run("", "user", "add"); status != 2 || !strings.Contains(errOut, "Usage: runnel user add") {
		t.Fatalf("user add without a name: %d %q", status, errOut)
	}
	if out, errOut, status := run("a new long horse\n", "user", "passwd", "ada"); status != 0 || !strings.HasPrefix(out, "Set a new password for ada.") {
		t.Fatalf("user passwd: %d %q %q", status, out, errOut)
	}
	if _, errOut, status := run("a new long horse\n", "user", "passwd", "kim"); status != 1 || !strings.Contains(errOut, "no user named kim") {
		t.Fatalf("user passwd for nobody: %d %q", status, errOut)
	}
	if out, errOut, status := run("", "user", "list"); status != 0 || out != "1  ada: admin, signs in with a password; no device signed in\n" {
		t.Fatalf("user list: %d %q %q", status, out, errOut)
	}

	films := filepath.Join(t.TempDir(), "films")
	shows := filepath.Join(t.TempDir(), "shows")
	media := map[string]string{
		filepath.Join(films, "Invented Film (2001)", "Invented Film (2001).mkv"):                  "30",
		filepath.Join(films, "Lone Film (1999).mp4"):                                              "1",
		filepath.Join(shows, "Harbor Lantern (2021)", "Season 01", "Harbor Lantern - S01E01.mkv"): "1",
		// In a folder that is not a season folder: the report notes it.
		filepath.Join(shows, "Harbor Lantern (2021)", "S02", "Harbor Lantern - S02E01.mkv"): "1",
	}
	for p, seconds := range media {
		os.MkdirAll(filepath.Dir(p), 0o755)
		tm.Make(t, p, tm.Join(tm.Video(seconds), tm.Tone("440", seconds), []string{"-c:v", "libx264", "-c:a", "aac"})...)
	}
	tm.Settle(t, films)
	tm.Settle(t, shows)

	out, errOut, status := run("", "library", "add", "--name", "Films", "--kind", "movies", "--root", films)
	if status != 0 || !strings.HasPrefix(out, "Added library 1, Films (movies), with folders: 1 "+films+".") {
		t.Fatalf("library add: %d %q %q", status, out, errOut)
	}
	count(`msg="probing done" probed=2 unreadable=0`, 1)
	if out, errOut, status = run("", "library", "add", "--name", "Shows", "--kind", "shows", "--root", shows); status != 0 {
		t.Fatalf("library add: %d %q %q", status, out, errOut)
	}
	count(`msg="probing done" probed=2 unreadable=0`, 2)

	if out, _, status = run("", "scan"); status != 0 || out != "Asked the server to scan every library.\n" {
		t.Fatalf("scan: %d %q", status, out)
	}
	count(`msg="scan done" root=\S+ ms=\d+ files=2 added=0 back=0 changed=0 moved=0 missing=0`, 2)
	if _, errOut, status = run("", "scan", "--library", "9"); status != 1 || !strings.Contains(errOut, "there is no library 9") {
		t.Fatalf("scan of a library that does not exist: %d %q", status, errOut)
	}

	if out, errOut, status = run("", "report", "--class", "all"); status != 0 {
		t.Fatalf("report: %d %q", status, errOut)
	}
	for _, want := range []string{
		"Library 1, Films (movies): 2 files. explicit 2, anchored 0, dash 0, guess 0, none 0. With a note 0. ffprobe could not read 0.\n",
		"Library 2, Shows (shows): 2 files. explicit 2, anchored 0, dash 0, guess 0, none 0. With a note 1. ffprobe could not read 0.\n",
		// A walk goes in name order, and S02 sorts before Season 01.
		"explicit\t3\tHarbor Lantern (2021) S02E01\tHarbor Lantern (2021)/S02/Harbor Lantern - S02E01.mkv\tin the folder \"S02\", which is not a season folder\n",
		"explicit\t4\tHarbor Lantern (2021) S01E01\tHarbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the report lacks %q:\n%s", want, out)
		}
	}
	// Two summaries, a blank line, and the one noted file.
	if out, _, _ = run("", "report", "--noted"); strings.Count(out, "\n") != 4 || !strings.Contains(out, "\nexplicit\t3\t") {
		t.Fatalf("report --noted:\n%s", out)
	}
	if _, errOut, status = run("", "report", "--class"); status != 2 || !strings.Contains(errOut, "Usage: runnel report") {
		t.Fatalf("report with a flag and no value: %d %q", status, errOut)
	}

	if out, errOut, status = run("", "library", "list"); status != 0 || !strings.HasPrefix(out, "1  Films (movies): 2 films and series; scan: ") ||
		!strings.Contains(out, "\n2  Shows (shows): 1 films and series; scan: ") {
		t.Fatalf("library list: %d %q %q", status, out, errOut)
	}
	if _, errOut, status = run("yes\n", "library", "remove", "2"); status != 1 || !strings.Contains(errOut, "give --yes") {
		t.Fatalf("library remove without a terminal: %d %q", status, errOut)
	}
	if out, errOut, status = run("", "library", "remove", "--yes", "2"); status != 0 || out != "Removed library 2, Shows.\n" {
		t.Fatalf("library remove: %d %q %q", status, out, errOut)
	}
	if out, _, _ = run("", "library", "list"); strings.Contains(out, "Shows") {
		t.Fatalf("library list after the removal:\n%s", out)
	}

	// A transcode streams from the built server, and is still running
	// when it is told to stop.
	resp, err = http.Post("http://"+addr+"/api/v1/auth/login", "application/json", strings.NewReader(
		`{"name": "ada", "password": "a new long horse", "device": {"id": "binary-test", "name": "Test", "client": "runnel-test/0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var signedIn struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&signedIn)
	resp.Body.Close()
	var film int64
	for id := int64(1); id <= 2 && film == 0; id++ {
		req, _ := http.NewRequest("GET", "http://"+addr+"/api/v1/files/"+strconv.FormatInt(id, 10), nil)
		req.Header.Set("Authorization", "Bearer "+signedIn.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			DurationMS int64 `json:"duration_ms"`
		}
		json.NewDecoder(resp.Body).Decode(&f)
		resp.Body.Close()
		if f.DurationMS > 20000 {
			film = id
		}
	}
	stream, err := http.Get("http://" + addr + "/api/v1/files/" + strconv.FormatInt(film, 10) + "/transcode.mkv?v=h264&t=" + signedIn.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if _, err := io.ReadFull(stream.Body, make([]byte, 16<<10)); err != nil || stream.StatusCode != 200 ||
		stream.Header.Get("X-Runnel-Encoder") != "libx264" {
		t.Fatalf("transcode of file %d: %d %v %v", film, stream.StatusCode, stream.Header, err)
	}

	// SIGTERM stops the server cleanly, within the 5 seconds of
	// docs/02-architecture.md, section 5, and takes ffmpeg with it.
	start := time.Now()
	serve.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			t.Fatalf("serve exited with %v:\n%s", err, logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("serve did not stop within 5 s:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "msg=stopped") {
		t.Fatalf("no stopped line:\n%s", logs.String())
	}
	t.Logf("stopped in %v", time.Since(start))
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		if b, err := os.ReadFile("/proc/" + d.Name() + "/cmdline"); err == nil && bytes.Contains(b, []byte(films)) {
			t.Errorf("a child outlived the server: %q", b)
		}
	}
	if _, errOut, status = run("", "scan"); status != 1 || !strings.Contains(errOut, "is runnel serve running?") {
		t.Fatalf("scan with the server stopped: %d %q", status, errOut)
	}
}
