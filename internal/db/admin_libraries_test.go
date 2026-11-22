// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// adminFixture makes a library of films at /a and one of shows at /s,
// with two films in /a and a series of two episodes in /s.
func adminFixture(t *testing.T) (*DB, int64, int64) {
	t.Helper()
	d, _ := openTemp(t)
	ctx := context.Background()
	films, err := d.CreateLibrary(ctx, "Films", "movies", "default", []string{"/a", "/b"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	shows, err := d.CreateLibrary(ctx, "Shows", "shows", "default", []string{"/s"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	addFilms(t, d, films.ID, films.Roots[0].ID, "Low Water", "Salt Road")
	addFilms(t, d, films.ID, films.Roots[1].ID, "North Light")
	addSeries(t, d, shows.ID, shows.Roots[0].ID, "Harbor Lantern", 2)
	return d, films.ID, shows.ID
}

// addFilms adds a film for each title, in a folder of its name, and builds
// the library with those films and the ones it had.
func addFilms(t *testing.T, d *DB, libraryID, rootID int64, titles ...string) {
	t.Helper()
	ctx := context.Background()
	var seen []SeenFile
	for i, title := range titles {
		seen = append(seen, SeenFile{RelPath: title + "/" + title + ".mkv", Kind: "video", Size: int64(1000 + i), MtimeNS: 1})
	}
	if err := d.ApplyScan(ctx, rootID, ScanChanges{New: seen}, 2, 0); err != nil {
		t.Fatal(err)
	}
	rebuildFilms(t, d, libraryID)
}

// rebuildFilms builds a library of films from every file it has that is
// not missing.
func rebuildFilms(t *testing.T, d *DB, libraryID int64) BuildResult {
	t.Helper()
	ctx := context.Background()
	files, err := d.LibraryFiles(ctx, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	var want []DesiredFilm
	for _, f := range files {
		folder, _, _ := strings.Cut(f.RelPath, "/")
		want = append(want, DesiredFilm{RootID: f.RootID, Key: folder, Title: folder, Files: []DesiredFile{{ID: f.ID}}})
	}
	res, err := d.BuildItems(ctx, libraryID, want, nil, strings.ToLower, 3)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func addSeries(t *testing.T, d *DB, libraryID, rootID int64, title string, episodes int) {
	t.Helper()
	ctx := context.Background()
	var seen []SeenFile
	for e := 1; e <= episodes; e++ {
		seen = append(seen, SeenFile{RelPath: title + "/Season 01/E" + string(rune('0'+e)) + ".mkv", Kind: "video", Size: int64(5000 + e), MtimeNS: 1})
	}
	if err := d.ApplyScan(ctx, rootID, ScanChanges{New: seen}, 2, 0); err != nil {
		t.Fatal(err)
	}
	files, err := d.LibraryFiles(ctx, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	s := DesiredSeries{RootID: rootID, Key: title, Title: title}
	for i, f := range files {
		s.Episodes = append(s.Episodes, DesiredEpisode{Season: 1, Episode: i + 1, Files: []DesiredFile{{ID: f.ID}}})
	}
	if _, err := d.BuildItems(ctx, libraryID, nil, []DesiredSeries{s}, strings.ToLower, 3); err != nil {
		t.Fatal(err)
	}
}

func itemIDs(t *testing.T, d *DB, libraryID int64) []int64 {
	t.Helper()
	items, err := d.LibraryItems(context.Background(), libraryID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func count(t *testing.T, d *DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.r.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestIDsAreNeverGivenAgain: an item or a library that goes leaves its id
// unused, also where it had the highest, which SQLite would give again.
func TestIDsAreNeverGivenAgain(t *testing.T) {
	d, filmsID, showsID := adminFixture(t)
	ctx := context.Background()
	before := itemIDs(t, d, filmsID)
	highest := slices.Max(append(before, itemIDs(t, d, showsID)...))

	// The series, with the highest ids, goes with its library.
	if _, err := d.DeleteLibrary(ctx, showsID); err != nil {
		t.Fatal(err)
	}
	lib, err := d.CreateLibrary(ctx, "Anime", "shows", "anime", []string{"/s"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if lib.ID <= showsID {
		t.Fatalf("a new library took id %d; the removed one had %d", lib.ID, showsID)
	}
	addSeries(t, d, lib.ID, lib.Roots[0].ID, "Harbor Lantern", 1)
	if got := slices.Min(itemIDs(t, d, lib.ID)); got <= highest {
		t.Fatalf("a new item took id %d; the removed ones went up to %d", got, highest)
	}

	// An item that a build removes, and one that the next build adds.
	removed := itemIDs(t, d, lib.ID)
	files, _ := d.LibraryFiles(ctx, lib.ID)
	if err := d.ApplyScan(ctx, lib.Roots[0].ID, ScanChanges{Missing: []int64{files[0].ID}}, 5, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := d.BuildItems(ctx, lib.ID, nil, nil, strings.ToLower, 6); err != nil {
		t.Fatal(err)
	}
	if left := itemIDs(t, d, lib.ID); len(left) != 0 {
		t.Fatalf("items left: %v", left)
	}
	films, _ := d.Library(ctx, filmsID)
	addFilms(t, d, filmsID, films.Roots[0].ID, "Tidewater")
	if got := slices.Max(itemIDs(t, d, filmsID)); got <= slices.Max(removed) {
		t.Fatalf("a new film took id %d; the removed items went up to %d", got, slices.Max(removed))
	}
}

func TestChangeLibrary(t *testing.T) {
	d, filmsID, showsID := adminFixture(t)
	ctx := context.Background()
	films, _ := d.Library(ctx, filmsID)
	shows, _ := d.Library(ctx, showsID)
	seqBefore, _ := d.Seq(ctx)
	str := func(s string) *string { return &s }

	for _, c := range []struct {
		change LibraryChange
		want   error
	}{
		{LibraryChange{Name: str("Shows")}, ErrNameTaken},
		{LibraryChange{RemoveRoots: []int64{shows.Roots[0].ID}}, ErrNoSuchRoot},
		{LibraryChange{RemoveRoots: []int64{films.Roots[0].ID, films.Roots[1].ID}}, ErrRootCount},
		{LibraryChange{AddRoots: []string{"/s/inner"}}, ErrRootOverlap},
		{LibraryChange{AddRoots: []string{"/c", "/c/"}}, ErrRootOverlap},
	} {
		if _, err := d.ChangeLibrary(ctx, filmsID, c.change, 9); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.change, err, c.want)
		}
	}
	if _, err := d.ChangeLibrary(ctx, 99, LibraryChange{Name: str("X")}, 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such library: %v", err)
	}
	if seq, _ := d.Seq(ctx); seq != seqBefore {
		t.Fatalf("refused changes moved the sequence from %d to %d", seqBefore, seq)
	}

	// The name, the profile, a folder added and one removed, together. A
	// folder inside the removed one may be added in its place.
	lib, err := d.ChangeLibrary(ctx, filmsID, LibraryChange{Name: str("films"), Profile: str("anime"),
		AddRoots: []string{"/a/inner/", "/c"}, RemoveRoots: []int64{films.Roots[0].ID}}, 9)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range lib.Roots {
		paths = append(paths, r.Path)
	}
	if lib.Name != "films" || lib.Profile != "anime" || !slices.Equal(paths, []string{"/b", "/a/inner", "/c"}) || lib.UpdatedSeq <= seqBefore {
		t.Fatalf("%+v", lib)
	}
	// The two films of /a went, each with a tombstone, and so did their
	// files and their search rows.
	if got := itemIDs(t, d, filmsID); len(got) != 1 {
		t.Fatalf("items left: %v", got)
	}
	if n := count(t, d, `SELECT count(*) FROM tombstones WHERE at = 9`); n != 2 {
		t.Fatalf("%d tombstones", n)
	}
	if n := count(t, d, `SELECT count(*) FROM files WHERE root_id = ?`, films.Roots[0].ID); n != 0 {
		t.Fatalf("%d files of the removed folder", n)
	}
	if n := count(t, d, `SELECT count(*) FROM items_fts WHERE rowid NOT IN (SELECT id FROM items)`); n != 0 {
		t.Fatalf("%d search rows of removed items", n)
	}

	// A series' folder: its episodes and its season go with it, none
	// stamped after the series.
	series := itemIDs(t, d, showsID)
	if _, err := d.ChangeLibrary(ctx, showsID, LibraryChange{AddRoots: []string{"/t"}, RemoveRoots: []int64{shows.Roots[0].ID}}, 10); err != nil {
		t.Fatal(err)
	}
	rows, err := d.r.Query(`SELECT item_id, seq FROM tombstones WHERE at = 10`)
	if err != nil {
		t.Fatal(err)
	}
	seqOf := map[int64]int64{}
	for rows.Next() {
		var id, seq int64
		rows.Scan(&id, &seq)
		seqOf[id] = seq
	}
	rows.Close()
	if len(seqOf) != len(series) {
		t.Fatalf("tombstones %v for items %v", seqOf, series)
	}
	items, _ := d.LibraryItems(ctx, showsID)
	if len(items) != 0 {
		t.Fatalf("items left: %+v", items)
	}
	for _, id := range series[1:] {
		if seqOf[id] > seqOf[series[0]] {
			t.Fatalf("item %d's tombstone is after its series': %v", id, seqOf)
		}
	}
}

func TestDeleteLibrary(t *testing.T) {
	d, filmsID, showsID := adminFixture(t)
	ctx := context.Background()
	if err := d.AddTask(ctx, "probe", "file:1", filmsID, "", 1); err != nil {
		t.Fatal(err)
	}
	seq, _ := d.Seq(ctx)
	films, _ := d.Library(ctx, filmsID)
	roots, err := d.DeleteLibrary(ctx, filmsID)
	if err != nil || len(roots) != 2 || roots[0] != films.Roots[0].ID || roots[1] != films.Roots[1].ID {
		t.Fatalf("%v %v", roots, err)
	}
	if n := count(t, d, `SELECT count(*) FROM items WHERE library_id = ?`, filmsID); n != 0 {
		t.Fatalf("%d items left", n)
	}
	if n := count(t, d, `SELECT count(*) FROM tombstones`); n != 0 {
		t.Fatalf("%d tombstones: removing a library writes none", n)
	}
	if n := count(t, d, `SELECT count(*) FROM tasks`); n != 0 {
		t.Fatalf("%d tasks left", n)
	}
	if n := count(t, d, `SELECT count(*) FROM items_fts WHERE rowid NOT IN (SELECT id FROM items)`); n != 0 {
		t.Fatalf("%d search rows of removed items", n)
	}
	if after, _ := d.Seq(ctx); after != seq+1 {
		t.Fatalf("the sequence went from %d to %d", seq, after)
	}
	if len(itemIDs(t, d, showsID)) != 4 {
		t.Fatal("the other library lost items")
	}
	if _, err := d.DeleteLibrary(ctx, filmsID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("again: %v", err)
	}
}

func TestWorkOf(t *testing.T) {
	d, filmsID, _ := adminFixture(t)
	ctx := context.Background()
	for i := 1; i <= 4; i++ {
		if err := d.AddTask(ctx, "probe", FileKey(int64(i)), filmsID, "", int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.AddTask(ctx, "import", "jellyfin", 0, "", 5); err != nil {
		t.Fatal(err)
	}
	claim := func() Task {
		t.Helper()
		task, ok, err := d.ClaimTask(ctx, "probe", 100)
		if err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		return task
	}
	done, failed, retrying := claim(), claim(), claim()
	d.FinishTask(ctx, done, Done, 0, "")
	d.FinishTask(ctx, failed, Failed, 0, "ffprobe crashed")
	d.FinishTask(ctx, retrying, Retry, 200, "timed out")
	running := claim()

	work, err := d.WorkOf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 2 || work[0].LibraryID != 0 || work[0].Kind != "import" {
		t.Fatalf("%+v", work)
	}
	p := work[1]
	if p.Kind != "probe" || p.LibraryID != filmsID || p.Done != 1 || p.Total != 4 || p.Failed != 1 || p.Running != 1 ||
		p.Queued != 1 || p.Error != "ffprobe crashed" || p.Since != 1 {
		t.Fatalf("%+v", p)
	}

	// Cancelling drops what is queued and what failed; what runs finishes.
	if n, err := d.CancelWork(ctx, "probe", filmsID); err != nil || n != 2 {
		t.Fatalf("cancelled %d: %v", n, err)
	}
	d.FinishTask(ctx, running, Failed, 0, "gone")
	if n, err := d.RetryWork(ctx, "probe", filmsID, 300); err != nil || n != 1 {
		t.Fatalf("retried %d: %v", n, err)
	}
	work, _ = d.WorkOf(ctx)
	if p := work[1]; p.Total != 1 || p.Queued != 1 || p.Failed != 0 || p.Error != "" {
		t.Fatalf("after a retry: %+v", p)
	}
	if n, err := d.CancelWork(ctx, "import", 0); err != nil || n != 1 {
		t.Fatalf("work of no library: %d %v", n, err)
	}
}

func TestKeysInUse(t *testing.T) {
	d, filmsID, _ := adminFixture(t)
	ctx := context.Background()
	item := itemIDs(t, d, filmsID)[0]
	image, photo, unused := strings.Repeat("1a", 16), strings.Repeat("2b", 16), strings.Repeat("3c", 16)
	if _, err := d.SetLocalImage(ctx, item, "poster", ImageSet{Key: image, Widths: []int{342}}, LocalSource{Root: 1, Path: "p.jpg"}); err != nil {
		t.Fatal(err)
	}
	exec(t, d, `INSERT INTO people (provider, provider_id, name, image_key) VALUES ('tmdb', '1', 'Ada', '`+photo+`')`)
	used, err := d.KeysInUse(ctx, []string{image, photo, unused})
	if err != nil || len(used) != 2 || !used[image] || !used[photo] {
		t.Fatalf("%v %v", used, err)
	}
}
