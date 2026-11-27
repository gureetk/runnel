// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
)

// scanFolders scans some folders of the env's root.
func (e *env) scanFolders(folders ...string) model.ScanResult {
	e.t.Helper()
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	r, err := e.s.scanRoot(context.Background(), lib.Roots[0], folders)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestFolderScan(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.put("A Film (2001)/A Film (2001) - 720p.mkv", 4000)
	e.put("B Film (2002)/B Film (2002).mkv", 4000)
	e.put("C Film (2003)/C Film (2003).mkv", 4000)
	e.scan()
	e.fingerprintAll()
	before := e.known()

	// In A and B: one file moves from A to B, one goes. In C, which is not
	// scanned, one goes and one comes.
	os.Rename(filepath.Join(e.root, "A Film (2001)/A Film (2001) - 720p.mkv"), filepath.Join(e.root, "B Film (2002)/B Film (2002) - 720p.mkv"))
	os.Remove(filepath.Join(e.root, "A Film (2001)/A Film (2001).mkv"))
	os.Remove(filepath.Join(e.root, "C Film (2003)/C Film (2003).mkv"))
	e.put("C Film (2003)/C Film (2003) - 1080p.mkv", 4000)
	want(t, e.scanFolders("A Film (2001)", "B Film (2002)"), 0, 1, 1)
	k := e.known()
	if moved := k["B Film (2002)/B Film (2002) - 720p.mkv"]; moved.ID != before["A Film (2001)/A Film (2001) - 720p.mkv"].ID {
		t.Fatalf("the file that moved between the folders scanned got a new id: %+v", moved)
	}
	if k["A Film (2001)/A Film (2001).mkv"].MissingSince == 0 {
		t.Fatal("the file gone from a folder scanned is not missing")
	}
	if k["C Film (2003)/C Film (2003).mkv"].MissingSince != 0 {
		t.Fatal("a file in a folder not scanned was marked missing")
	}
	if _, ok := k["C Film (2003)/C Film (2003) - 1080p.mkv"]; ok {
		t.Fatal("a file in a folder not scanned was found")
	}

	// A folder that is gone: what it held is missing, or moved out of it.
	os.Rename(filepath.Join(e.root, "B Film (2002)"), filepath.Join(e.root, "B Film (2012)"))
	want(t, e.scanFolders("B Film (2002)", "B Film (2012)"), 0, 2, 0)
	if k := e.known(); k["B Film (2012)/B Film (2002).mkv"].ID != before["B Film (2002)/B Film (2002).mkv"].ID {
		t.Fatalf("a renamed folder's file got a new id: %+v", k)
	}

	// The whole root finds what the folder scans did not look at.
	want(t, e.scan(), 1, 0, 1)
}

func TestFilesBeingWritten(t *testing.T) {
	e := newEnv(t, "movies")
	old := e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.scan()
	id := e.known()["A Film (2001)/A Film (2001).mkv"].ID

	// Modified 4 seconds ago: still being written. The new file is not
	// taken, and the changed one keeps what was known of it.
	fresh := e.put("B Film (2002)/B Film (2002).mkv", 4000)
	e.put("A Film (2001)/A Film (2001).mkv", 5000)
	for _, p := range []string{fresh, old} {
		at := e.now.Add(-4 * time.Second)
		os.Chtimes(p, at, at)
	}
	want(t, e.scan(), 0, 0, 0)
	k := e.known()
	if _, ok := k["B Film (2002)/B Film (2002).mkv"]; ok || k["A Film (2001)/A Film (2001).mkv"].Size != 4000 || k["A Film (2001)/A Film (2001).mkv"].MissingSince != 0 {
		t.Fatalf("files being written were taken: %+v", k)
	}
	// Their folders are scanned again once the files are 10 seconds old.
	at, ok := e.s.nextLater()
	if wantAt := e.now.Add(6*time.Second + 500*time.Millisecond).UnixMilli(); !ok || at != wantAt {
		t.Fatalf("scanned again at %d, want %d", at, wantAt)
	}
	if e.s.runAsked(context.Background()) {
		t.Fatal("scanned again too early")
	}
	e.now = e.now.Add(7 * time.Second)
	if !e.s.runAsked(context.Background()) {
		t.Fatal("not scanned again")
	}
	k = e.known()
	if _, ok := k["B Film (2002)/B Film (2002).mkv"]; !ok || k["A Film (2001)/A Film (2001).mkv"].Size != 5000 || k["A Film (2001)/A Film (2001).mkv"].ID != id {
		t.Fatalf("after 10 seconds: %+v", k)
	}
	if _, ok := e.s.nextLater(); ok {
		t.Fatal("still to scan again")
	}

	// A time in the future is not a file being written.
	future := e.put("C Film (2003)/C Film (2003).mkv", 4000)
	at = e.now.Add(time.Hour).UnixMilli()
	os.Chtimes(future, time.UnixMilli(at), time.UnixMilli(at))
	want(t, e.scan(), 1, 0, 0)
}

func TestCheckedHook(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	var seen []bool
	e.s.Checked = func(r model.Root) { seen = append(seen, r.Available) }
	e.scan()
	e.fingerprintAll()
	held := filepath.Join(t.TempDir(), "held")
	os.Rename(e.root, held)
	e.scan()
	if !slices.Equal(seen, []bool{true, false}) {
		t.Fatalf("checked %v", seen)
	}
}

func TestOutermostAndNightly(t *testing.T) {
	got := outermost(map[string]bool{"A": true, "A/Season 01": true, "B/x": true, "Bee": true, "B": true})
	if !slices.Equal(got, []string{"A", "B", "Bee"}) {
		t.Fatalf("outermost %v", got)
	}
	loc := time.FixedZone("here", 2*3600)
	for _, c := range []struct {
		now  time.Time
		want time.Duration
	}{
		{time.Date(2026, 11, 21, 3, 0, 0, 0, loc), time.Hour},
		{time.Date(2026, 11, 21, 4, 0, 0, 0, loc), 24 * time.Hour},
		{time.Date(2026, 11, 21, 23, 30, 0, 0, loc), 4*time.Hour + 30*time.Minute},
	} {
		if got := untilNightly(c.now); got != c.want {
			t.Errorf("from %v: %v, want %v", c.now, got, c.want)
		}
	}
}

func TestRunWalksAtStart(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(e.known()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if len(e.known()) != 1 {
		t.Fatal("nothing was scanned at start")
	}
}

// TestTheScopeOfABuild: what a scan hands to the build is what it looked
// at: the top folders of the folders it walked, those of files whose rows
// go after 30 days missing, wherever they are, or the whole root
// (docs/06-library-and-metadata.md, section 5).
func TestTheScopeOfABuild(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.put("B Film (2002)/Extras/B Film (2002) - interview.mkv", 4000)
	e.put("C Film (2003)/C Film (2003).mkv", 4000)
	e.put("Lone (1999).mkv", 4000)
	var scopes []db.Scope
	e.s.Built = func(ctx context.Context, id int64, scope db.Scope) error {
		scopes = append(scopes, scope)
		return nil
	}
	root := e.lib.Roots[0].ID
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	last := func() db.Scope { return scopes[len(scopes)-1] }

	// A scan of the library builds all of it.
	if _, err := e.s.ScanLibrary(context.Background(), lib); err != nil || last() != nil {
		t.Fatalf("a scan of the library: %v, scope %v", err, last())
	}
	// Folders: their top folders, deeper ones too, and a lone file's name.
	e.s.RequestFolders(root, []string{"B Film (2002)/Extras", "A Film (2001)"})
	e.s.runAsked(context.Background())
	if s := last(); len(s) != 1 || !slices.Equal(slices.Sorted(slices.Values(s[root])), []string{"A Film (2001)", "B Film (2002)"}) {
		t.Fatalf("a scan of two folders: %v", s)
	}
	// The whole root asked for: all of it.
	e.s.RequestFolders(root, []string{""})
	e.s.runAsked(context.Background())
	if s := last(); len(s) != 1 || s[root] != nil {
		t.Fatalf("a scan of the root: %v", s)
	}
	// A file missing for more than 30 days goes with any scan of its root,
	// and its top folder is built with the folders scanned.
	os.Remove(filepath.Join(e.root, "C Film (2003)", "C Film (2003).mkv"))
	e.s.RequestFolders(root, []string{"C Film (2003)"})
	e.s.runAsked(context.Background())
	e.now = e.now.Add(31 * 24 * time.Hour)
	e.s.RequestFolders(root, []string{"A Film (2001)"})
	e.s.runAsked(context.Background())
	if s := last(); !slices.Equal(slices.Sorted(slices.Values(s[root])), []string{"A Film (2001)", "C Film (2003)"}) {
		t.Fatalf("a scan that removes a file elsewhere: %v", s)
	}
	if _, ok := e.known()["C Film (2003)/C Film (2003).mkv"]; ok {
		t.Fatal("the file missing for 31 days is still known")
	}
}
