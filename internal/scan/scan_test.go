// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/tasks"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

type env struct {
	t    *testing.T
	db   *db.DB
	s    *Scanner
	root string
	lib  model.Library
	now  time.Time
}

// newEnv makes a database, a library with one root in a temporary folder,
// and a scanner without a prober.
func newEnv(t *testing.T, kind string) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	root := filepath.Join(t.TempDir(), "media")
	os.MkdirAll(root, 0o755)
	lib, err := d.CreateLibrary(ctx, "Test", kind, "default", []string{root}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: d, root: root, lib: lib, now: time.Unix(1_800_000_000, 0)}
	e.s = New(d, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.s.now = func() time.Time { return e.now }
	return e
}

// put writes a file of random bytes, so that no two files share a
// fingerprint.
func (e *env) put(rel string, size int) string {
	e.t.Helper()
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	b := make([]byte, size)
	rand.Read(b)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) scan() model.ScanResult {
	e.t.Helper()
	lib, err := e.db.Library(context.Background(), e.lib.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	res, err := e.s.ScanLibrary(context.Background(), lib)
	if err != nil || len(res) != 1 {
		e.t.Fatalf("scan: %v %v", res, err)
	}
	return res[0]
}

func (e *env) known() map[string]db.KnownFile {
	e.t.Helper()
	k, err := e.db.KnownFiles(context.Background(), e.lib.Roots[0].ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return k
}

// fingerprintAll stores each present file's fingerprint, as a probe would.
func (e *env) fingerprintAll() {
	e.t.Helper()
	for _, k := range e.known() {
		if k.MissingSince != 0 {
			continue
		}
		p := filepath.Join(e.root, filepath.FromSlash(k.RelPath))
		fp, err := Fingerprint(p)
		if err != nil {
			e.t.Fatal(err)
		}
		target := db.ProbeTarget{FileID: k.ID, Path: p, Size: k.Size, MtimeNS: k.MtimeNS}
		if _, err := e.db.SaveProbe(context.Background(), target, model.Probe{}, fp, probe.Version, 1); err != nil {
			e.t.Fatal(err)
		}
	}
}

// toProbe lists the files whose probe is missing or behind, by path.
func (e *env) toProbe() []string {
	e.t.Helper()
	ids, err := e.db.Unprobed(context.Background(), probe.Version)
	if err != nil {
		e.t.Fatal(err)
	}
	byID := map[int64]string{}
	for _, k := range e.known() {
		byID[k.ID] = filepath.Join(e.root, filepath.FromSlash(k.RelPath))
	}
	var out []string
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

// queue is a background queue that runs probes with prober.
func (e *env) queue(prober *probe.Prober) *tasks.Queue {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := tasks.New(e.db, tasks.NewGate(), log)
	p := NewProbes(e.db, prober, log)
	p.now = func() time.Time { return e.now }
	q.Register(p.Kind())
	return q
}

func (e *env) available() bool {
	e.t.Helper()
	lib, err := e.db.Library(context.Background(), e.lib.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return lib.Roots[0].Available
}

func want(t *testing.T, r model.ScanResult, added, changed, removed int) {
	t.Helper()
	if r.Added != added || r.Changed != changed || r.Removed != removed {
		t.Fatalf("scan found added %d, changed %d, removed %d; want %d, %d, %d (%+v)",
			r.Added, r.Changed, r.Removed, added, changed, removed, r)
	}
}

func TestFirstScanAndRescan(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("Invented Film (2001)/Invented Film (2001).mkv", 4000)
	e.put("Invented Film (2001)/extras/Making of.mkv", 4000)
	e.put("Invented Film (2001)/Invented Film (2001)-trailer.mp4", 4000)
	e.put("Invented Film (2001)/Invented Film (2001).srt", 100)
	e.put("Invented Film (2001)/poster.jpg", 100)
	e.put("Lone Film (1999).mp4", 4000)
	e.put("Another Film (2010)/Another Film (2010).mkv.part", 4000)
	e.put("Another Film (2010)/sample.mkv", 4000)
	e.put(".hidden/Hidden Film (2000).mkv", 4000)
	e.put("@eaDir/Thumbs (2000).mkv", 4000)

	r := e.scan()
	want(t, r, 4, 0, 0)
	k := e.known()
	if len(k) != 4 {
		t.Fatalf("%d files: %v", len(k), k)
	}
	for rel, kind := range map[string]string{
		"Invented Film (2001)/Invented Film (2001).mkv":         "video",
		"Invented Film (2001)/extras/Making of.mkv":             "extra",
		"Invented Film (2001)/Invented Film (2001)-trailer.mp4": "extra",
		"Lone Film (1999).mp4":                                  "video",
	} {
		if k[rel].Kind != kind {
			t.Errorf("%s: kind %q, want %q", rel, k[rel].Kind, kind)
		}
	}
	if !e.available() || r.Error != "" || r.At != e.now.UnixMilli() {
		t.Fatalf("root: available %v, result %+v", e.available(), r)
	}

	// Nothing changed: nothing found.
	want(t, e.scan(), 0, 0, 0)
	// Only videos are probed; extras are not.
	if targets := e.toProbe(); len(targets) != 2 {
		t.Fatalf("%d files to probe, want the 2 videos", len(targets))
	}
}

func TestChangeAndMissing(t *testing.T) {
	e := newEnv(t, "movies")
	a := e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.put("B Film (2002)/B Film (2002).mkv", 4000)
	e.scan()
	e.fingerprintAll()
	before := e.known()

	// A file that changed is probed again, with the same id.
	e.put("A Film (2001)/A Film (2001).mkv", 5000)
	want(t, e.scan(), 0, 1, 0)
	after := e.known()
	if after["A Film (2001)/A Film (2001).mkv"].ID != before["A Film (2001)/A Film (2001).mkv"].ID {
		t.Fatal("a changed file got a new id")
	}
	if targets := e.toProbe(); len(targets) != 1 || targets[0] != a {
		t.Fatalf("to probe after the change: %+v", targets)
	}

	// A file that went is marked missing, once.
	os.Remove(filepath.Join(e.root, "B Film (2002)/B Film (2002).mkv"))
	want(t, e.scan(), 0, 0, 1)
	if e.known()["B Film (2002)/B Film (2002).mkv"].MissingSince != e.now.UnixMilli() {
		t.Fatal("the gone file is not marked missing")
	}
	e.now = e.now.Add(time.Hour)
	want(t, e.scan(), 0, 0, 0)

	// After 30 days missing, its row goes.
	e.now = e.now.Add(30 * 24 * time.Hour)
	e.scan()
	if _, ok := e.known()["B Film (2002)/B Film (2002).mkv"]; ok {
		t.Fatal("a file missing for 30 days was kept")
	}
}

func TestComingBack(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.scan()
	id := e.known()["A Film (2001)/A Film (2001).mkv"].ID
	tmp := filepath.Join(t.TempDir(), "held")
	os.Rename(filepath.Join(e.root, "A Film (2001)"), tmp)
	e.put("Placeholder (2000)/Placeholder (2000).mkv", 4000) // so the root is not empty
	want(t, e.scan(), 1, 0, 1)
	os.Rename(tmp, filepath.Join(e.root, "A Film (2001)"))
	want(t, e.scan(), 1, 0, 0)
	k := e.known()["A Film (2001)/A Film (2001).mkv"]
	if k.ID != id || k.MissingSince != 0 {
		t.Fatalf("back: %+v, want id %d and not missing", k, id)
	}
}

func TestRenameKeepsTheID(t *testing.T) {
	e := newEnv(t, "shows")
	e.put("Invented Show (2020)/Season 01/Invented Show - 01.mkv", 4000)
	e.put("Invented Show (2020)/Season 01/Invented Show - 02.mkv", 4000)
	e.scan()
	e.fingerprintAll()
	ids := map[string]int64{}
	for rel, k := range e.known() {
		ids[rel] = k.ID
	}

	// A rename in place, and a move to another folder, in one scan.
	os.Rename(filepath.Join(e.root, "Invented Show (2020)/Season 01/Invented Show - 01.mkv"),
		filepath.Join(e.root, "Invented Show (2020)/Season 01/Invented Show - S01E01.mkv"))
	os.MkdirAll(filepath.Join(e.root, "Invented Show (2020)/Season 02"), 0o755)
	os.Rename(filepath.Join(e.root, "Invented Show (2020)/Season 01/Invented Show - 02.mkv"),
		filepath.Join(e.root, "Invented Show (2020)/Season 02/Invented Show - S02E01.mkv"))

	want(t, e.scan(), 0, 2, 0)
	k := e.known()
	if len(k) != 2 {
		t.Fatalf("%d rows after the renames, want 2: %v", len(k), k)
	}
	if k["Invented Show (2020)/Season 01/Invented Show - S01E01.mkv"].ID != ids["Invented Show (2020)/Season 01/Invented Show - 01.mkv"] ||
		k["Invented Show (2020)/Season 02/Invented Show - S02E01.mkv"].ID != ids["Invented Show (2020)/Season 01/Invented Show - 02.mkv"] {
		t.Fatalf("a renamed file got a new id: %v, before %v", k, ids)
	}
	// Its probe stays: the content is the same.
	if targets := e.toProbe(); len(targets) != 0 {
		t.Fatalf("a moved file is to be probed again: %+v", targets)
	}
}

func TestRenameOfAMissingFile(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.put("B Film (2002)/B Film (2002).mkv", 4000)
	e.scan()
	e.fingerprintAll()
	id := e.known()["A Film (2001)/A Film (2001).mkv"].ID

	// Moved out of the library, marked missing, then back under another name.
	tmp := filepath.Join(t.TempDir(), "a.mkv")
	os.Rename(filepath.Join(e.root, "A Film (2001)/A Film (2001).mkv"), tmp)
	want(t, e.scan(), 0, 0, 1)
	os.Rename(tmp, filepath.Join(e.root, "A Film (2001)/A Film (2001) - 1080p.mkv"))
	want(t, e.scan(), 0, 1, 0)
	if got := e.known()["A Film (2001)/A Film (2001) - 1080p.mkv"]; got.ID != id || got.MissingSince != 0 {
		t.Fatalf("%+v, want id %d", got, id)
	}
}

func TestUnprobedRenameIsNewAndMissing(t *testing.T) {
	// Without a fingerprint a rename cannot be told from a new file.
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.scan()
	os.Rename(filepath.Join(e.root, "A Film (2001)/A Film (2001).mkv"), filepath.Join(e.root, "A Film (2001)/Renamed.mkv"))
	want(t, e.scan(), 1, 0, 1)
}

func TestRootAway(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.put("B Film (2002)/B Film (2002).mkv", 4000)
	e.scan()
	seq0 := e.lib.UpdatedSeq

	// Gone: cannot be listed.
	tmp := filepath.Join(t.TempDir(), "away")
	os.Rename(e.root, tmp)
	r := e.scan()
	if r.Error == "" || e.available() {
		t.Fatalf("a root that cannot be listed: %+v, available %v", r, e.available())
	}
	for _, k := range e.known() {
		if k.MissingSince != 0 {
			t.Fatal("a file was marked missing while its root was away")
		}
	}
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if lib.UpdatedSeq <= seq0 {
		t.Fatal("the library's sequence did not move when its root went away")
	}

	// Back.
	os.Rename(tmp, e.root)
	want(t, e.scan(), 0, 0, 0)
	if !e.available() {
		t.Fatal("the root did not come back")
	}

	// Empty, as an unmounted mount point is.
	held := filepath.Join(t.TempDir(), "held")
	os.MkdirAll(held, 0o755)
	for _, d := range []string{"A Film (2001)", "B Film (2002)"} {
		os.Rename(filepath.Join(e.root, d), filepath.Join(held, d))
	}
	r = e.scan()
	if r.Error == "" || e.available() {
		t.Fatalf("an empty root: %+v", r)
	}
	for _, k := range e.known() {
		if k.MissingSince != 0 {
			t.Fatal("a file was marked missing while its root was empty")
		}
	}
}

func TestUnreadableFolderMarksNothingMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root from reading a folder")
	}
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	e.put("B Film (2002)/B Film (2002).mkv", 4000)
	e.scan()
	locked := filepath.Join(e.root, "B Film (2002)")
	os.Chmod(locked, 0)
	defer os.Chmod(locked, 0o755)
	r := e.scan()
	if r.Removed != 0 || r.Error == "" {
		t.Fatalf("%+v: nothing in an unreadable folder is missing, and the scan says so", r)
	}
}

func TestScanStopsWithItsContext(t *testing.T) {
	e := newEnv(t, "movies")
	for i := 0; i < 50; i++ {
		e.put(filepath.Join("F", string(rune('a'+i%26))+string(rune('a'+i/26))+".mkv"), 10)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if _, err := e.s.ScanLibrary(ctx, lib); err == nil {
		t.Fatal("a cancelled scan reported no error")
	}
	if len(e.known()) != 0 {
		t.Fatal("a cancelled scan wrote files")
	}
}

// TestProbeAfterScan probes real files: two playable, one that ffprobe
// cannot read, one that changes between scan and probe.
func TestProbeAfterScan(t *testing.T) {
	_, ffprobe := tm.Tools(t)
	e := newEnv(t, "movies")
	q := e.queue(probe.New(ffprobe, 2))
	for _, rel := range []string{"A Film (2001)/A Film (2001).mkv", "B Film (2002)/B Film (2002).mp4"} {
		p := filepath.Join(e.root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		tm.Make(t, p, tm.Join(tm.Video("1"), tm.Tone("440", "1"), []string{"-c:v", "libx264", "-c:a", "aac"})...)
	}
	e.put("C Film (2003)/C Film (2003).mkv", 4000) // not a video at all
	changed := e.put("D Film (2004)/D Film (2004).mkv", 4000)
	e.scan()
	os.WriteFile(changed, []byte("changed after the scan"), 0o644)

	if err := q.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	k := e.known()
	for _, rel := range []string{"A Film (2001)/A Film (2001).mkv", "B Film (2002)/B Film (2002).mp4", "C Film (2003)/C Film (2003).mkv"} {
		if k[rel].Fingerprint == "" {
			t.Errorf("%s has no fingerprint after its probe", rel)
		}
	}
	if k["D Film (2004)/D Film (2004).mkv"].Fingerprint != "" {
		t.Error("a file that changed after the scan was probed")
	}
	if targets := e.toProbe(); len(targets) != 1 || filepath.Base(targets[0]) != "D Film (2004).mkv" {
		t.Fatalf("left to probe: %+v; want only the changed file", targets)
	}
	// Done tasks go once their kind and library have nothing left.
	if ts, _ := e.db.Tasks(context.Background()); len(ts) != 0 {
		t.Fatalf("tasks left after the pass: %+v", ts)
	}

	// The next scan sees the change, and the next pass probes it.
	want(t, e.scan(), 0, 1, 0)
	q.Drain(context.Background())
	if targets := e.toProbe(); len(targets) != 0 {
		t.Fatalf("left to probe: %+v", targets)
	}
}

func TestRunScansWhenAsked(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	e.s.Request(0)
	e.s.Request(e.lib.ID) // gathered into the same scan, or the next
	deadline := time.Now().Add(5 * time.Second)
	for len(e.known()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	if len(e.known()) != 1 {
		t.Fatal("the scan asked for did not run")
	}
}

// TestWalkingAndForget: a scan says which library it goes through while it
// walks and builds, and a root that Forget is told of loses the scans
// asked for it.
func TestWalkingAndForget(t *testing.T) {
	e := newEnv(t, "movies")
	e.put("A Film (2001)/A Film (2001).mkv", 4000)
	var mu sync.Mutex
	var during, other []bool
	e.s.Built = func(ctx context.Context, id int64) error {
		mu.Lock()
		during, other = append(during, e.s.Walking(id)), append(other, e.s.Walking(id+1))
		mu.Unlock()
		return nil
	}
	var forgotten []int64
	e.s.Forgotten = func(id int64) { forgotten = append(forgotten, id) }
	e.s.RequestFolders(e.lib.Roots[0].ID, []string{"A Film (2001)"})
	e.s.Forget([]int64{e.lib.Roots[0].ID})
	if len(forgotten) != 1 || forgotten[0] != e.lib.Roots[0].ID {
		t.Fatalf("forgotten: %v", forgotten)
	}
	if e.s.runAsked(context.Background()) {
		t.Fatal("a forgotten root's folders were scanned")
	}

	e.s.Request(e.lib.ID)
	if !e.s.runAsked(context.Background()) {
		t.Fatal("the scan asked for did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(during) != 1 || !during[0] || other[0] {
		t.Fatalf("walking while built: %v, another library: %v", during, other)
	}
	if e.s.Walking(e.lib.ID) {
		t.Fatal("still walking after the scan")
	}
}
