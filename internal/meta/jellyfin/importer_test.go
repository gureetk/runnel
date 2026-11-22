// SPDX-License-Identifier: GPL-3.0-or-later

package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/meta/jellyfin/standin"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/tasks"
)

const (
	adaJF  = "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c"
	noorJF = "5b4d20b7e5df4f2a8a2d3e4f5a6b7c8d"
)

type importEnv struct {
	t          *testing.T
	db         *db.DB
	dir        string
	jf         *standin.Server
	url        string
	gate       *tasks.Gate
	q          *tasks.Queue
	im         *Importer
	ada, noor  model.User
	films      model.Library
	shows      model.Library
	itemOfPath map[string]int64 // rel path in the films or shows folder -> item id
}

func newImportEnv(t *testing.T) *importEnv {
	t.Helper()
	ctx := context.Background()
	e := &importEnv{t: t, dir: t.TempDir(), gate: tasks.NewGate(), itemOfPath: map[string]int64{}}
	var err error
	if e.db, err = db.Open(ctx, filepath.Join(e.dir, "runnel.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.db.Close() })
	e.ada, _ = e.db.CreateUser(ctx, "ada", nil, true, 1)
	e.noor, _ = e.db.CreateUser(ctx, "noor", nil, false, 1)
	e.films, _ = e.db.CreateLibrary(ctx, "Films", "movies", "default", []string{"/srv/media/films"}, 1)
	e.shows, _ = e.db.CreateLibrary(ctx, "Shows", "shows", "default", []string{"/srv/media/shows"}, 1)
	add := func(lib model.Library, rels ...string) {
		var seen []db.SeenFile
		for i, rel := range rels {
			kind := "video"
			if filepath.Base(filepath.Dir(rel)) == "extras" {
				kind = "extra"
			}
			seen = append(seen, db.SeenFile{RelPath: rel, Kind: kind, Size: int64(1000*int(lib.ID) + i), MtimeNS: 1})
		}
		if err := e.db.ApplyScan(ctx, lib.Roots[0].ID, db.ScanChanges{New: seen}, 2, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := library.Build(ctx, e.db, lib.ID, 2); err != nil {
			t.Fatal(err)
		}
	}
	add(e.films, "Low Water (2001)/Low Water (2001).mkv", "Salt Road (2004)/Salt Road (2004).mkv",
		"Low Water (2001)/extras/Behind the Scenes.mkv")
	add(e.shows, "Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E01.mkv", "Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E02.mkv")
	for _, lib := range []model.Library{e.films, e.shows} {
		items, _ := e.db.LibraryItems(ctx, lib.ID)
		files, _ := e.db.LibraryFiles(ctx, lib.ID)
		rel := map[int64]string{}
		for _, f := range files {
			rel[f.ID] = f.RelPath
		}
		for _, it := range items {
			if it.Type == "series" {
				e.itemOfPath[it.PathKey] = it.ID
			}
			for _, f := range it.FileIDs {
				if it.Type == "movie" || it.Type == "episode" {
					e.itemOfPath[rel[f]] = it.ID
				}
			}
		}
	}
	e.jf = &standin.Server{Key: "0123456789abcdef0123456789abcdef", Version: "12.4.0",
		Users: []standin.User{{ID: adaJF, Name: "Ada"}, {ID: noorJF, Name: "Noor"}},
		Items: []standin.Item{
			{ID: "a1", Name: "Low Water", Type: "Movie", Path: "/media/films/Low Water (2001)/Low Water (2001).mkv",
				ProviderIDs: map[string]string{"Tmdb": "101", "imdb": "tt0101", "Zap2It": "z"},
				UserData: map[string]standin.UserData{
					adaJF:  {Played: true, PlayCount: 2, LastPlayedDate: "2026-10-01T20:00:00.0000000Z"},
					noorJF: {PositionTicks: 6_000_000_000, LastPlayedDate: "2026-10-02T20:00:00Z"}}},
			{ID: "a2", Name: "Salt Road", Type: "Movie", Path: "/media/films/Salt Road (2004)/Salt Road (2004).mkv",
				ProviderIDs: map[string]string{"Tmdb": "102"}},
			{ID: "a3", Name: "Gone Film", Type: "Movie", Path: "/media/films/Gone Film (2000)/Gone Film (2000).mkv"},
			{ID: "a4", Name: "Elsewhere", Type: "Movie", Path: "/other/Elsewhere (2003).mkv"},
			{ID: "a5", Name: "Behind the Scenes", Type: "Movie", Path: "/media/films/Low Water (2001)/extras/Behind the Scenes.mkv"},
			{ID: "s1", Name: "Harbor Lantern", Type: "Series", Path: "/media/shows/Harbor Lantern (2010)",
				ProviderIDs: map[string]string{"Tvdb": "555", "Tmdb": "777"}},
			{ID: "s2", Name: "Missing Show", Type: "Series", Path: "/media/shows/Missing Show (2012)"},
			{ID: "e1", Name: "Episode 1", Type: "Episode", Path: "/media/shows/Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E01.mkv",
				UserData: map[string]standin.UserData{adaJF: {Played: true, PlayCount: 1, LastPlayedDate: "2026-10-03T20:00:00Z"}}},
			{ID: "e2", Name: "Episode 2", Type: "Episode", Path: "/media/shows/Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E02.mkv",
				UserData: map[string]standin.UserData{adaJF: {PositionTicks: 3_000_000_000, LastPlayedDate: "2026-10-04T20:00:00Z"}}},
		}}
	e.url = e.jf.Start(t).URL
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.im = NewImporter(e.db, e.dir, e.gate, log)
	e.im.Now = func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	e.q = tasks.New(e.db, e.gate, log)
	e.q.Register(e.im.Kind())
	return e
}

// drain runs what the queue may run now.
func (e *importEnv) drain() {
	e.t.Helper()
	if err := e.q.Drain(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *importEnv) request(dry bool) Request {
	return Request{URL: e.url, APIKey: e.jf.Key, Users: map[string]string{"ada": "Ada", "Noor": "noor"},
		Paths: []PathMap{{From: "/media", To: "/srv/media"}, {From: "/med", To: "/nowhere"}}, DryRun: dry}
}

func (e *importEnv) run(req Request) Report {
	e.t.Helper()
	if err := e.im.Start(context.Background(), req); err != nil {
		e.t.Fatal(err)
	}
	e.drain()
	rep, err := e.im.Report(context.Background(), true)
	if err != nil {
		e.t.Fatal(err)
	}
	return rep
}

func checkCounts(t *testing.T, rep Report, matches, ada, noor int) {
	t.Helper()
	if rep.State != "done" || rep.Version != "12.4.0" || rep.Joined != 4 || rep.NotJoined != 3 || rep.Series != 1 || rep.SeriesNot != 1 ||
		rep.Matches != matches || rep.States["ada"] != ada || rep.States["noor"] != noor || len(rep.Missing) != 4 || rep.Error != nil ||
		rep.FinishedAt == nil {
		t.Fatalf("%+v", rep)
	}
}

func TestImport(t *testing.T) {
	e := newImportEnv(t)
	ctx := context.Background()
	seq, _ := e.db.Seq(ctx)

	// The dry run counts and writes nothing.
	rep := e.run(e.request(true))
	checkCounts(t, rep, 3, 3, 1)
	if !rep.DryRun {
		t.Fatal("not a dry run")
	}
	reasons := map[string]string{}
	for _, m := range rep.Missing {
		reasons[m.Path] = m.Type + ": " + m.Reason
	}
	for p, want := range map[string]string{
		"/media/films/Gone Film (2000)/Gone Film (2000).mkv":         "movie: no file at that path here",
		"/other/Elsewhere (2003).mkv":                                "movie: it is outside every library folder here, as /other/Elsewhere (2003).mkv",
		"/media/films/Low Water (2001)/extras/Behind the Scenes.mkv": "movie: the file is no film's or episode's here",
		"/media/shows/Missing Show (2012)":                           "series: no series here is built from that folder",
	} {
		if reasons[p] != want {
			t.Errorf("%s: %q, want %q", p, reasons[p], want)
		}
	}
	if after, _ := e.db.Seq(ctx); after != seq {
		t.Fatalf("a dry run moved the sequence from %d to %d", seq, after)
	}
	if states, _ := e.db.StatesOf(ctx, e.ada.ID, []int64{e.itemOfPath["Low Water (2001)/Low Water (2001).mkv"]}); len(states) != 0 {
		t.Fatalf("a dry run wrote %+v", states)
	}

	// The import writes what the dry run counted: played flags and
	// positions equal Jellyfin's (exit criterion 3, in the stand-in).
	rep = e.run(e.request(false))
	checkCounts(t, rep, 3, 3, 1)
	low := e.itemOfPath["Low Water (2001)/Low Water (2001).mkv"]
	e1 := e.itemOfPath["Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E01.mkv"]
	e2 := e.itemOfPath["Harbor Lantern (2010)/Season 01/Harbor Lantern - S01E02.mkv"]
	ada, _ := e.db.StatesOf(ctx, e.ada.ID, []int64{low, e1, e2})
	want := map[int64]db.State{
		low: {ItemID: low, Played: true, PlayCount: 2, LastPlayedAt: time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC).UnixMilli()},
		e1:  {ItemID: e1, Played: true, PlayCount: 1, LastPlayedAt: time.Date(2026, 10, 3, 20, 0, 0, 0, time.UTC).UnixMilli()},
		e2:  {ItemID: e2, PositionMS: 300_000, LastPlayedAt: time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC).UnixMilli()},
	}
	for id, w := range want {
		if ada[id] != w {
			t.Errorf("ada's %d: %+v, want %+v", id, ada[id], w)
		}
	}
	noor, _ := e.db.StatesOf(ctx, e.noor.ID, []int64{low})
	if s := noor[low]; s.Played || s.PositionMS != 600_000 {
		t.Fatalf("noor's: %+v", s)
	}
	m, err := e.db.MatchOf(ctx, low)
	if err != nil || m.State != "matched" || m.ExternalIDs["tmdb"] != "101" || m.ExternalIDs["imdb"] != "tt0101" || len(m.ExternalIDs) != 2 {
		t.Fatalf("Low Water: %+v %v", m, err)
	}
	if m, _ := e.db.MatchOf(ctx, e.itemOfPath["Harbor Lantern (2010)"]); m.State != "matched" || m.ExternalIDs["tvdb"] != "555" {
		t.Fatalf("the series: %+v", m)
	}

	// Again: nothing to write. The import is safe to repeat.
	seq, _ = e.db.Seq(ctx)
	checkCounts(t, e.run(e.request(false)), 0, 0, 0)
	if after, _ := e.db.Seq(ctx); after != seq {
		t.Fatalf("a repeated import moved the sequence from %d to %d", seq, after)
	}

	// A state played later here is kept.
	e.jf.Items[8].UserData[adaJF] = standin.UserData{PositionTicks: 9_000_000_000, LastPlayedDate: "2026-10-05T20:00:00Z"}
	if _, err := e.db.MarkPlayed(ctx, e.ada.ID, []int64{e2}, true, 1_800_000_000_000); err != nil {
		t.Fatal(err)
	}
	checkCounts(t, e.run(e.request(false)), 0, 0, 0)
	if s, _ := e.db.StatesOf(ctx, e.ada.ID, []int64{e2}); !s[e2].Played {
		t.Fatalf("a newer state here was lowered: %+v", s[e2])
	}

	// The report is kept, and read back by the next start.
	again := NewImporter(e.db, e.dir, e.gate, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if kept, err := again.Report(ctx, false); err != nil || kept.State != "done" || kept.Joined != 4 || len(kept.Missing) != 4 {
		t.Fatalf("%+v %v", kept, err)
	}
}

func TestImportRefusals(t *testing.T) {
	e := newImportEnv(t)
	ctx := context.Background()
	var bad *RequestError
	for _, req := range []Request{
		{URL: e.url, APIKey: e.jf.Key, Users: map[string]string{"kim": "ada"}},
		{URL: e.url, APIKey: e.jf.Key, Users: map[string]string{"ada": "kim"}},
		{URL: e.url, APIKey: e.jf.Key, Paths: []PathMap{{From: "media", To: "/srv"}}},
		{URL: "jellyfin.lan", APIKey: e.jf.Key},
	} {
		if err := e.im.Start(ctx, req); !errors.As(err, &bad) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	if err := e.im.Start(ctx, Request{URL: e.url, APIKey: "wrong-key"}); !errors.Is(err, ErrBadKey) {
		t.Fatalf("a wrong key: %v", err)
	}
	if _, err := e.im.Report(ctx, false); !errors.Is(err, ErrNoReport) {
		t.Fatalf("before any import: %v", err)
	}

	// While a playback starts the import waits, and a second one is
	// refused until it ends. The import also waits at the gate before
	// each page.
	release := e.gate.Hold()
	if err := e.im.Start(ctx, e.request(true)); err != nil {
		t.Fatal(err)
	}
	if err := e.im.Start(ctx, e.request(true)); !errors.Is(err, ErrRunning) {
		t.Fatalf("a second import: %v", err)
	}
	if rep, _ := e.im.Report(ctx, false); rep.State != "running" || rep.Joined != 0 {
		t.Fatalf("held at the gate: %+v", rep)
	}
	waits := &countingGate{Gate: e.gate}
	e.im.gate = waits
	release()
	e.drain()
	if waits.n != 2 {
		t.Fatalf("%d waits at the gate, want one before each person's one page", waits.n)
	}
	if rep, _ := e.im.Report(ctx, false); rep.State != "done" {
		t.Fatalf("after the gate opened: %+v", rep)
	}

	// A stop that cuts a run short queues it again, and a new importer,
	// as at the next start, reports it as running until it runs.
	if err := e.im.Start(ctx, e.request(true)); err != nil {
		t.Fatal(err)
	}
	stopped, stop := context.WithCancel(ctx)
	stop()
	if err := e.im.runTask(stopped, db.Task{Payload: mustPayload(t, e.request(true))}); err == nil {
		t.Fatal("a run cut short returned no error to the queue")
	}
	next := NewImporter(e.db, e.dir, e.gate, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rep, _ := next.Report(ctx, false); rep.State != "running" {
		t.Fatalf("queued, after a stop: %+v", rep)
	}
	if err := next.Start(ctx, e.request(true)); !errors.Is(err, ErrRunning) {
		t.Fatalf("an import while one is queued: %v", err)
	}
}

func mustPayload(t *testing.T, req Request) string {
	t.Helper()
	b, err := json.Marshal(stored{req.URL, req.APIKey, req.Users, req.Paths, req.DryRun})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestImportFailureIsTheReports: a run that fails says so in its report,
// and the queue does not try it again.
func TestImportFailureIsTheReports(t *testing.T) {
	e := newImportEnv(t)
	ctx := context.Background()
	if err := e.im.Start(ctx, e.request(true)); err != nil {
		t.Fatal(err)
	}
	e.jf.Fail = 500
	e.drain()
	rep, _ := e.im.Report(ctx, false)
	if rep.State != "failed" || rep.Error == nil {
		t.Fatalf("%+v", rep)
	}
	if n, _ := e.db.CountTasks(ctx, "import", "queued", "running", "failed"); n != 0 {
		t.Fatalf("%d import tasks left", n)
	}
}

func TestImportReportsTheFirstHundred(t *testing.T) {
	e := newImportEnv(t)
	for i := range 130 {
		e.jf.Items = append(e.jf.Items, standin.Item{ID: fmt.Sprint("x", i), Type: "Episode", Path: fmt.Sprintf("/media/shows/Gone/E%03d.mkv", i)})
	}
	if err := e.im.Start(context.Background(), Request{URL: e.url, APIKey: e.jf.Key, Paths: e.request(true).Paths, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	short, _ := e.im.Report(context.Background(), false)
	full, _ := e.im.Report(context.Background(), true)
	if short.NotJoined != 133 || len(short.Missing) != MissingShown || short.MoreMissed != 134-MissingShown || len(full.Missing) != 134 ||
		full.MoreMissed != 0 || len(short.States) != 0 {
		t.Fatalf("%d shown, %d more; %d in full", len(short.Missing), short.MoreMissed, len(full.Missing))
	}
}

func TestRewrite(t *testing.T) {
	maps := []PathMap{{From: "/media", To: "/srv/media"}, {From: "/media/anime", To: "/mnt/anime"}, {From: "/data/", To: "/srv/data"}}
	for in, want := range map[string]string{
		"/media/films/a.mkv":   "/srv/media/films/a.mkv",
		"/media/anime/b.mkv":   "/mnt/anime/b.mkv",
		"/mediaplus/c.mkv":     "/mediaplus/c.mkv",
		"/data/d.mkv":          "/srv/data/d.mkv",
		"/media":               "/srv/media",
		"/elsewhere/media/x.y": "/elsewhere/media/x.y",
	} {
		if got := Rewrite(in, maps); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// countingGate counts the waits at a gate.
type countingGate struct {
	Gate
	n int
}

func (g *countingGate) Wait(ctx context.Context) error {
	g.n++
	return g.Gate.Wait(ctx)
}
