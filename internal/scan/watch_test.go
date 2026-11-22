// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package scan

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/model"
)

type watchEnv struct {
	t    *testing.T
	w    *Watcher
	root model.Root

	mu   sync.Mutex
	asks [][]string // the folders of each request, sorted
	log  strings.Builder
}

// newWatchEnv watches a fresh root with the given folders, gathering for
// 50 ms, and records what it asks to scan.
func newWatchEnv(t *testing.T, folders ...string) *watchEnv {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "media")
	for _, f := range append(folders, "") {
		if err := os.MkdirAll(filepath.Join(dir, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := &watchEnv{t: t, root: model.Root{ID: 7, Path: dir, Available: true}}
	e.w = &Watcher{log: slog.New(slog.NewTextHandler(io.Discard, nil)), gather: 50 * time.Millisecond, every: time.Hour,
		mounts: "/proc/self/mountinfo", changed: map[int64]map[string]bool{}, polled: map[int64]polledRoot{}}
	e.w.request = func(root int64, folders []string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if root != e.root.ID {
			t.Errorf("a request for root %d", root)
		}
		slices.Sort(folders)
		e.asks = append(e.asks, folders)
	}
	sys, err := newWatchSys()
	if err != nil {
		t.Fatal(err)
	}
	e.w.sys = sys
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e.w.Watch(e.root)
	return e
}

func (e *watchEnv) path(rel string) string {
	return filepath.Join(e.root.Path, filepath.FromSlash(rel))
}

// next waits for the next request and returns its folders, or nil when
// none comes within half a second.
func (e *watchEnv) next() []string {
	e.t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		if len(e.asks) > 0 {
			a := e.asks[0]
			e.asks = e.asks[1:]
			e.mu.Unlock()
			return a
		}
		e.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (e *watchEnv) want(folders ...string) {
	e.t.Helper()
	if got := e.next(); !slices.Equal(got, folders) {
		e.t.Fatalf("asked to scan %q, want %q", got, folders)
	}
}

func (e *watchEnv) nothing() {
	e.t.Helper()
	if got := e.next(); got != nil {
		e.t.Fatalf("asked to scan %q, want nothing", got)
	}
}

func TestWatchFolders(t *testing.T) {
	e := newWatchEnv(t, "Harbor Lantern (2021)/Season 01", "Paper Comets (2019)", ".hidden")

	// Changes in two folders, gathered into one request.
	os.WriteFile(e.path("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv"), []byte("x"), 0o644)
	os.WriteFile(e.path("Paper Comets (2019)/Paper Comets (2019).mkv"), []byte("x"), 0o644)
	os.WriteFile(e.path("Paper Comets (2019)/Paper Comets (2019).eng.srt"), []byte("x"), 0o644)
	e.want("Harbor Lantern (2021)/Season 01", "Paper Comets (2019)")

	// Reading a file, as probing and playing do, reports nothing. Neither
	// do hidden files and folders, and files still downloading.
	b, _ := os.ReadFile(e.path("Paper Comets (2019)/Paper Comets (2019).mkv"))
	os.Stat(e.path("Paper Comets (2019)/Paper Comets (2019).mkv"))
	os.WriteFile(e.path("Paper Comets (2019)/.Paper Comets (2019).mkv.tmp1"), b, 0o644)
	os.WriteFile(e.path("Paper Comets (2019)/Paper Comets (2019) - 720p.mkv.part"), b, 0o644)
	os.WriteFile(e.path(".hidden/A Film (2001).mkv"), b, 0o644)
	e.nothing()

	// A new folder is watched, and so is what it holds: here a folder made
	// with a file in it elsewhere, and moved in.
	os.Mkdir(e.path("Glass River (2011)"), 0o755)
	e.want("")
	os.WriteFile(e.path("Glass River (2011)/Glass River (2011).mkv"), b, 0o644)
	e.want("Glass River (2011)")
	outside := filepath.Join(t.TempDir(), "Salt Road (2010)")
	os.MkdirAll(filepath.Join(outside, "Season 01"), 0o755)
	os.Rename(outside, e.path("Salt Road (2010)"))
	e.want("")
	os.WriteFile(e.path("Salt Road (2010)/Season 01/Salt Road - 01.mkv"), b, 0o644)
	e.want("Salt Road (2010)/Season 01")

	// A renamed folder is watched under its new name.
	os.Rename(e.path("Paper Comets (2019)"), e.path("Paper Comets (2020)"))
	e.want("")
	os.Rename(e.path("Paper Comets (2020)/Paper Comets (2019).mkv"), e.path("Paper Comets (2020)/Paper Comets (2020).mkv"))
	e.want("Paper Comets (2020)")
	e.w.mu.Lock()
	for _, dw := range e.w.sys.byWD {
		if strings.HasPrefix(dw.rel, "Paper Comets (2019)") {
			t.Errorf("a watch under the old name: %+v", dw)
		}
	}
	e.w.mu.Unlock()

	// A folder that goes takes its watches with it.
	os.RemoveAll(e.path("Harbor Lantern (2021)"))
	e.want("", "Harbor Lantern (2021)", "Harbor Lantern (2021)/Season 01")
	time.Sleep(50 * time.Millisecond)
	e.w.mu.Lock()
	for _, dw := range e.w.sys.byWD {
		if strings.HasPrefix(dw.rel, "Harbor Lantern (2021)") {
			t.Errorf("a watch left for a folder that went: %+v", dw)
		}
	}
	n := len(e.w.sys.byWD)
	e.w.mu.Unlock()
	if n != 5 { // the root, Glass River, Salt Road and its season, Paper Comets
		t.Errorf("%d watches, want 5", n)
	}
	e.nothing()
}

func TestWatchRootGoesAndPolling(t *testing.T) {
	e := newWatchEnv(t, "A Film (2001)")
	// The root itself goes: it is scanned, which finds it away, and from
	// then on it is scanned every pollEvery.
	held := filepath.Join(t.TempDir(), "held")
	os.Rename(e.root.Path, held)
	e.want("")
	e.w.mu.Lock()
	p, polled := e.w.polled[e.root.ID]
	watching := e.w.sys.watching(e.root.ID)
	e.w.mu.Unlock()
	if !polled || watching || p.why != "the folder was moved or removed" {
		t.Fatalf("polled %v (%q), watching %v", polled, p.why, watching)
	}
	// It is back, and a scan has checked it: watched again.
	os.Rename(held, e.root.Path)
	e.w.Watch(e.root)
	e.w.mu.Lock()
	_, polled = e.w.polled[e.root.ID]
	e.w.mu.Unlock()
	if polled || !e.w.sys.watching(e.root.ID) {
		t.Fatal("not watched again")
	}
	os.WriteFile(e.path("A Film (2001)/A Film (2001).mkv"), []byte("x"), 0o644)
	e.want("A Film (2001)")

	// An away root is polled.
	e.w.Watch(model.Root{ID: e.root.ID, Path: e.root.Path})
	e.w.mu.Lock()
	p = e.w.polled[e.root.ID]
	e.w.mu.Unlock()
	if p.why != "the folder is away" || e.w.sys.watching(e.root.ID) {
		t.Fatalf("an away root: %+v", p)
	}
}

func TestPollTicks(t *testing.T) {
	asked := make(chan []string, 10)
	w := &Watcher{log: slog.New(slog.NewTextHandler(io.Discard, nil)), gather: time.Hour, every: 20 * time.Millisecond,
		changed: map[int64]map[string]bool{}, polled: map[int64]polledRoot{},
		request: func(root int64, folders []string) { asked <- folders }}
	w.Watch(model.Root{ID: 1, Path: "/nowhere"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	select {
	case f := <-asked:
		if !slices.Equal(f, []string{""}) {
			t.Fatalf("asked %q", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a polled root was not scanned")
	}
	cancel()
	<-done
}

func TestNetworkFS(t *testing.T) {
	table := strings.Join([]string{
		`22 1 0:21 / / rw,relatime shared:1 - ext4 /dev/sda1 rw`,
		`40 22 0:35 / /srv/media rw,relatime shared:20 - fuse.mergerfs disks rw,user_id=0`,
		`41 40 0:36 / /srv/media/remote rw,relatime shared:21 - nfs4 nas:/export rw,vers=4.2`,
		`42 22 0:37 / /mnt/my\040films rw,relatime - cifs //nas/films rw`,
		`43 22 0:38 / /mnt/cloud rw,relatime - fuse.rclone remote: rw`,
	}, "\n")
	for dir, want := range map[string]string{
		"/srv/media/Films":        "",
		"/srv/media/remote/Films": "nfs4",
		"/srv/mediaX":             "",
		"/mnt/my films/A":         "cifs",
		"/mnt/cloud":              "fuse.rclone",
		"/home/someone":           "",
	} {
		if got := networkFSIn(strings.NewReader(table), dir); got != want {
			t.Errorf("%s: %q, want %q", dir, got, want)
		}
	}
	// The sandbox's own table: its temporary folders are not on a network.
	if got := networkFS("/proc/self/mountinfo", t.TempDir()); got != "" {
		t.Errorf("a temporary folder is on %q", got)
	}
}

// TestForget: a root taken out of its library is neither watched nor
// polled.
func TestForget(t *testing.T) {
	e := newWatchEnv(t, "A Film (2001)")
	e.w.Forget(e.root.ID)
	e.w.mu.Lock()
	watching := e.w.sys.watching(e.root.ID)
	e.w.mu.Unlock()
	if watching {
		t.Fatal("still watched")
	}
	os.WriteFile(e.path("A Film (2001)/A Film (2001).mkv"), []byte("x"), 0o644)
	e.nothing()

	e.w.Watch(model.Root{ID: e.root.ID, Path: e.root.Path})
	e.w.Forget(e.root.ID)
	e.w.mu.Lock()
	_, polled := e.w.polled[e.root.ID]
	e.w.mu.Unlock()
	if polled {
		t.Fatal("still polled")
	}
}
