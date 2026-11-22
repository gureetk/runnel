// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// watchFixture makes, with invented titles:
//
//   - a shows library with the series 10, "Harbor Lantern": season 11
//     (episodes 101, 102, 103 of season 1), season 12 (episode 201), and
//     the specials 13 (episode 301), each in a file of 24 minutes but 103,
//     whose file has no duration;
//   - a films library with film 50 in one file of 100 minutes, and film 60
//     in two parts of 50 minutes with a 1080p version of 95 minutes;
//   - the users 1 and 2, and a token 7 of user 1 on "Living room TV".
func watchFixture(t *testing.T) *DB {
	t.Helper()
	d, _ := openTemp(t)
	exec(t, d, `INSERT INTO libraries (id, name, kind, created_at) VALUES (1, 'Shows', 'shows', 1), (2, 'Films', 'movies', 1)`)
	exec(t, d, `INSERT INTO roots (id, library_id, path) VALUES (1, 1, '/shows'), (2, 2, '/films')`)
	exec(t, d, `INSERT INTO users (id, name, created_at) VALUES (1, 'ada', 1), (2, 'bo', 1)`)
	exec(t, d, `INSERT INTO tokens (id, hash, user_id, device_id, device_name, client, created_at)
		VALUES (7, x'01', 1, 'd1', 'Living room TV', 'runnel-test/1.0', 1)`)
	exec(t, d, `INSERT INTO items (id, library_id, type, parent_id, series_id, season_number, episode_number, root_id, path_key,
		  title, sort_title, added_at, updated_seq) VALUES
		(10, 1, 'series', NULL, NULL, NULL, NULL, 1, 'Harbor Lantern', 'Harbor Lantern', 'harbor lantern', 1, 0),
		(11, 1, 'season', 10, 10, 1, NULL, NULL, NULL, 'Season 1', 'season 1', 1, 0),
		(12, 1, 'season', 10, 10, 2, NULL, NULL, NULL, 'Season 2', 'season 2', 1, 0),
		(13, 1, 'season', 10, 10, 0, NULL, NULL, NULL, 'Specials', 'specials', 1, 0),
		(101, 1, 'episode', 11, 10, 1, 1, NULL, NULL, 'Episode 1', 'episode 1', 1, 0),
		(102, 1, 'episode', 11, 10, 1, 2, NULL, NULL, 'Episode 2', 'episode 2', 1, 0),
		(103, 1, 'episode', 11, 10, 1, 3, NULL, NULL, 'Episode 3', 'episode 3', 1, 0),
		(201, 1, 'episode', 12, 10, 2, 1, NULL, NULL, 'Episode 1', 'episode 1', 1, 0),
		(301, 1, 'episode', 13, 10, 0, 1, NULL, NULL, 'Episode 1', 'episode 1', 1, 0),
		(50, 2, 'movie', NULL, NULL, NULL, NULL, 2, 'Paper Comets (2019)', 'Paper Comets', 'paper comets', 1, 0),
		(60, 2, 'movie', NULL, NULL, NULL, NULL, 2, 'The Salt Road (2005)', 'The Salt Road', 'salt road', 1, 0)`)
	exec(t, d, `INSERT INTO files (id, root_id, rel_path, size, mtime_ns, first_seen_at, duration_ms) VALUES
		(1101, 1, 'e101.mkv', 1, 1, 1, 1440000), (1102, 1, 'e102.mkv', 1, 1, 1, 1440000), (1103, 1, 'e103.mkv', 1, 1, 1, NULL),
		(1201, 1, 'e201.mkv', 1, 1, 1, 1440000), (1301, 1, 'e301.mkv', 1, 1, 1, 1440000),
		(500, 2, 'f50.mkv', 1, 1, 1, 6000000),
		(601, 2, 'f60-1.mkv', 1, 1, 1, 3000000), (602, 2, 'f60-2.mkv', 1, 1, 1, 3000000), (603, 2, 'f60-1080p.mkv', 1, 1, 1, 5700000)`)
	exec(t, d, `INSERT INTO item_files (item_id, file_id, label, part) VALUES
		(101, 1101, NULL, 0), (102, 1102, NULL, 0), (103, 1103, NULL, 0), (201, 1201, NULL, 0), (301, 1301, NULL, 0),
		(50, 500, NULL, 0), (60, 601, NULL, 1), (60, 602, NULL, 2), (60, 603, '1080p', 0)`)
	return d
}

func stateOf(t *testing.T, d *DB, user, item int64) State {
	t.Helper()
	states, err := d.StatesOf(context.Background(), user, []int64{item})
	if err != nil {
		t.Fatal(err)
	}
	return stateOr(states, item)
}

func seqNow(t *testing.T, d *DB) int64 {
	t.Helper()
	s, err := d.Seq(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProgressRules(t *testing.T) {
	const runtime, at = 1_000_000, 5000
	fresh := State{ItemID: 1}
	inProgress := State{ItemID: 1, PositionMS: 300_000, LastPlayedAt: 100}
	played := State{ItemID: 1, Played: true, PlayCount: 2, LastPlayedAt: 100}
	for _, c := range []struct {
		name     string
		cur      State
		pos      int64
		stopped  bool
		runtime  int64
		want     State
		withFile bool
	}{
		{"a start below 30 s keeps nothing", fresh, 29_999, false, runtime, fresh, false},
		{"from 30 s the position is kept", fresh, 30_000, false, runtime, State{ItemID: 1, PositionMS: 30_000, LastPlayedAt: at}, true},
		{"starting over clears a kept position", inProgress, 10_000, false, runtime, State{ItemID: 1, LastPlayedAt: 100}, false},
		{"a stop below 30 s clears it too", inProgress, 5_000, true, runtime, State{ItemID: 1, LastPlayedAt: 100}, false},
		{"a stop short of 90% keeps the position", fresh, 899_999, true, runtime, State{ItemID: 1, PositionMS: 899_999, LastPlayedAt: at}, true},
		{"a stop at 90% marks it played", inProgress, 900_000, true, runtime, State{ItemID: 1, Played: true, PlayCount: 1, LastPlayedAt: at}, true},
		{"playing past 90% is not a stop", fresh, 990_000, false, runtime, State{ItemID: 1, PositionMS: 990_000, LastPlayedAt: at}, true},
		{"an unknown runtime keeps the position", fresh, 990_000, true, 0, State{ItemID: 1, PositionMS: 990_000, LastPlayedAt: at}, true},
		{"a second viewing makes a played item unplayed", played, 60_000, false, runtime, State{ItemID: 1, PositionMS: 60_000, PlayCount: 2, LastPlayedAt: at}, true},
		{"a short look leaves a played item as it was", played, 10_000, true, runtime, played, false},
		{"finishing it again counts again", played, 950_000, true, runtime, State{ItemID: 1, Played: true, PlayCount: 3, LastPlayedAt: at}, true},
	} {
		got, withFile := progressed(c.cur, Progress{ItemID: 1, PositionMS: c.pos, Stopped: c.stopped, At: at}, c.runtime)
		if got != c.want || withFile != c.withFile {
			t.Errorf("%s: %+v %v, want %+v %v", c.name, got, withFile, c.want, c.withFile)
		}
	}
}

func TestSaveProgress(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		p    Progress
		want error
	}{
		{Progress{ItemID: 999, FileID: 500}, ErrNotFound},
		{Progress{ItemID: 10, FileID: 1101}, ErrNoState},
		{Progress{ItemID: 11, FileID: 1101}, ErrNoState},
		{Progress{ItemID: 50, FileID: 601}, ErrWrongFile},
	} {
		if _, err := d.SaveProgress(ctx, 1, c.p); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.p, err, c.want)
		}
	}

	// A report that changes nothing takes no sequence value.
	before := seqNow(t, d)
	if changed, err := d.SaveProgress(ctx, 1, Progress{ItemID: 50, FileID: 500, PositionMS: 10_000, At: 10}); err != nil || changed {
		t.Fatalf("a start: %v %v", changed, err)
	}
	if seqNow(t, d) != before {
		t.Fatal("a report that changed nothing took a sequence value")
	}

	// The 1080p version of film 60 runs 95 minutes: 87% of it is not played.
	if changed, err := d.SaveProgress(ctx, 1, Progress{ItemID: 60, FileID: 603, PositionMS: 5_000_000, Stopped: true, At: 20}); err != nil || !changed {
		t.Fatalf("a stop: %v %v", changed, err)
	}
	if s := stateOf(t, d, 1, 60); s != (State{ItemID: 60, PositionMS: 5_000_000, LastPlayedAt: 20}) {
		t.Fatalf("after a stop at 87%%: %+v", s)
	}
	var file, seq int64
	if err := d.r.QueryRow(`SELECT file_id, updated_seq FROM user_state WHERE user_id = 1 AND item_id = 60`).Scan(&file, &seq); err != nil {
		t.Fatal(err)
	}
	if file != 603 || seq != seqNow(t, d) || seq == before {
		t.Fatalf("the row's file %d and seq %d (now %d, before %d)", file, seq, seqNow(t, d), before)
	}

	// The main version is in two parts of 50 minutes: 91 minutes in is the
	// second part, past 90% of the whole.
	if _, err := d.SaveProgress(ctx, 2, Progress{ItemID: 60, FileID: 602, PositionMS: 5_460_000, Stopped: true, At: 30}); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, d, 2, 60); s != (State{ItemID: 60, Played: true, PlayCount: 1, LastPlayedAt: 30}) {
		t.Fatalf("after a stop in the second part: %+v", s)
	}
	if s := stateOf(t, d, 1, 60); s.Played {
		t.Fatal("one user's state changed another's")
	}

	// An episode whose file has no duration keeps the position at a stop.
	if _, err := d.SaveProgress(ctx, 1, Progress{ItemID: 103, FileID: 1103, PositionMS: 1_400_000, Stopped: true, At: 40}); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, d, 1, 103); s.Played || s.PositionMS != 1_400_000 {
		t.Fatalf("an unknown runtime: %+v", s)
	}
}

func TestMarkPlayedAndRestore(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	if _, err := d.SaveProgress(ctx, 1, Progress{ItemID: 102, FileID: 1102, PositionMS: 600_000, At: 5}); err != nil {
		t.Fatal(err)
	}
	exec(t, d, `UPDATE user_state SET play_count = 3 WHERE item_id = 102`)

	// A series reaches every episode, the specials too, in order.
	prev, err := d.MarkPlayed(ctx, 1, []int64{10}, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, p := range prev {
		ids = append(ids, p.ItemID)
	}
	if !slices.Equal(ids, []int64{101, 102, 103, 201, 301}) {
		t.Fatalf("marked %v", ids)
	}
	if prev[1] != (State{ItemID: 102, PositionMS: 600_000, PlayCount: 3, LastPlayedAt: 5}) || prev[0] != (State{ItemID: 101}) {
		t.Fatalf("previous %+v", prev)
	}
	if s := stateOf(t, d, 1, 102); s != (State{ItemID: 102, Played: true, PlayCount: 3, LastPlayedAt: 100}) {
		t.Fatalf("a played episode keeps its count: %+v", s)
	}
	if s := stateOf(t, d, 1, 301); s != (State{ItemID: 301, Played: true, PlayCount: 1, LastPlayedAt: 100}) {
		t.Fatalf("marked played counts one play: %+v", s)
	}

	// Marking again changes nothing and takes no sequence value.
	before := seqNow(t, d)
	if again, err := d.MarkPlayed(ctx, 1, []int64{11, 101, 999}, true, 200); err != nil || len(again) != 0 || seqNow(t, d) != before {
		t.Fatalf("again: %v %v, seq %d after %d", again, err, seqNow(t, d), before)
	}

	// Undo puts back what was there, a never-started item as one.
	if err := d.RestoreStates(ctx, 1, prev); err != nil {
		t.Fatal(err)
	}
	for _, p := range prev {
		if s := stateOf(t, d, 1, p.ItemID); s != p {
			t.Fatalf("restored %+v, want %+v", s, p)
		}
	}
	// Nothing differs now, so writing it again writes nothing.
	before = seqNow(t, d)
	if err := d.RestoreStates(ctx, 1, prev); err != nil || seqNow(t, d) != before {
		t.Fatalf("restoring the same: %v, seq %d after %d", err, seqNow(t, d), before)
	}
	if err := d.RestoreStates(ctx, 1, []State{{ItemID: 11}}); !errors.Is(err, ErrNoState) {
		t.Fatalf("restoring a season: %v", err)
	}
	if err := d.RestoreStates(ctx, 1, []State{{ItemID: 999, Played: true}, {ItemID: 50, PlayCount: 1}, {ItemID: 50, PlayCount: 2}}); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, d, 1, 50); s.PlayCount != 2 {
		t.Fatalf("the later state of an item wins: %+v", s)
	}

	// Unplayed clears played and the position, and keeps the count and the
	// time; an item neither played nor started is left alone.
	if _, err := d.MarkPlayed(ctx, 1, []int64{12}, true, 300); err != nil {
		t.Fatal(err)
	}
	prev, err = d.MarkPlayed(ctx, 1, []int64{12, 11}, false, 400)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, p := range prev {
		ids = append(ids, p.ItemID)
	}
	if !slices.Equal(ids, []int64{201, 102}) {
		t.Fatalf("unmarked %v", ids)
	}
	if s := stateOf(t, d, 1, 201); s != (State{ItemID: 201, PlayCount: 1, LastPlayedAt: 300}) {
		t.Fatalf("unplayed: %+v", s)
	}
	if s := stateOf(t, d, 2, 201); s != (State{ItemID: 201}) {
		t.Fatalf("another user's: %+v", s)
	}
}

// TestMarkingManyStampsInBatches checks that marking a long series played
// stamps no sequence value on more than StampBatch rows
// (docs/03-data-model.md, section 5).
func TestMarkingManyStampsInBatches(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	exec(t, d, `WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM k WHERE i < 1203)
		INSERT INTO items (id, library_id, type, parent_id, series_id, season_number, episode_number, title, sort_title, added_at, updated_seq)
		SELECT 10000 + i, 1, 'episode', 12, 10, 2, i + 1, 'Episode', 'episode', 1, 0 FROM k`)
	prev, err := d.MarkPlayed(ctx, 1, []int64{12}, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(prev) != 1204 {
		t.Fatalf("marked %d", len(prev))
	}
	perSeq := map[int64]int{}
	rows, err := d.r.QueryContext(ctx, `SELECT updated_seq, count(*) FROM user_state GROUP BY updated_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var counts []int
	for rows.Next() {
		var seq int64
		var n int
		rows.Scan(&seq, &n)
		perSeq[seq] = n
	}
	for _, n := range perSeq {
		counts = append(counts, n)
	}
	slices.Sort(counts)
	if !slices.Equal(counts, []int{204, StampBatch, StampBatch}) {
		t.Fatalf("rows per sequence value: %v", perSeq)
	}
}

func TestUnplayed(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	if _, err := d.MarkPlayed(ctx, 1, []int64{101, 301}, true, 100); err != nil {
		t.Fatal(err)
	}
	got, err := d.Unplayed(ctx, 1, []int64{10}, []int64{11, 12, 13})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int64]int{10: 3, 11: 2, 12: 1}; !maps.Equal(got, want) {
		t.Fatalf("unplayed %v, want %v (the specials' season has none left)", got, want)
	}
	if other, _ := d.Unplayed(ctx, 2, []int64{10}, nil); other[10] != 4 {
		t.Fatalf("another user's: %v", other)
	}

	byLib, err := d.UnplayedByLibrary(ctx, 1)
	if err != nil || !maps.Equal(byLib, map[int64]int{1: 1, 2: 2}) {
		t.Fatalf("by library %v %v", byLib, err)
	}
	// With every episode outside the specials played, the series is not
	// unplayed, whatever the specials are.
	if _, err := d.MarkPlayed(ctx, 1, []int64{102, 103, 201, 50}, true, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MarkPlayed(ctx, 1, []int64{301}, false, 100); err != nil {
		t.Fatal(err)
	}
	byLib, _ = d.UnplayedByLibrary(ctx, 1)
	if !maps.Equal(byLib, map[int64]int{2: 1}) {
		t.Fatalf("by library %v", byLib)
	}
}

func TestResume(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	where := func(user int64) string {
		t.Helper()
		r, ok, err := d.ResumeOf(ctx, user, 10)
		if err != nil || !ok {
			t.Fatalf("resume: %v %v", ok, err)
		}
		return fmt.Sprintf("%d@%d", r.ID, r.PositionMS)
	}
	mark := func(played bool, at int64, ids ...int64) {
		t.Helper()
		if _, err := d.MarkPlayed(ctx, 1, ids, played, at); err != nil {
			t.Fatal(err)
		}
	}
	progress := func(item, file, pos, at int64) {
		t.Helper()
		if _, err := d.SaveProgress(ctx, 1, Progress{ItemID: item, FileID: file, PositionMS: pos, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	steps := []struct {
		do   func()
		want string
	}{
		{func() {}, "101@0"},                                      // nothing played: the first
		{func() { mark(true, 10, 301) }, "101@0"},                 // a special played last
		{func() { mark(true, 20, 101) }, "102@0"},                 // the next one
		{func() { mark(true, 30, 103) }, "201@0"},                 // after the one played last, not the gap
		{func() { progress(102, 1102, 60_000, 40) }, "102@60000"}, // in progress
		{func() { progress(201, 1201, 90_000, 50) }, "201@90000"}, // the one played last of two
		{func() { mark(true, 60, 101, 102, 103, 201) }, "101@0"},  // all played: the first
		{func() { mark(false, 70, 102, 103, 201) }, "102@0"},      // the first after 101
		{func() { mark(true, 80, 102, 103) }, "201@0"},            // marked together: the latest of them
	}
	for i, s := range steps {
		s.do()
		if got := where(1); got != s.want {
			t.Fatalf("step %d: %s, want %s", i, got, s.want)
		}
	}
	if got := where(2); got != "101@0" {
		t.Fatalf("another user: %s", got)
	}
	exec(t, d, `INSERT INTO items (id, library_id, type, root_id, path_key, title, sort_title, added_at, updated_seq)
		VALUES (20, 1, 'series', 1, 'Empty', 'Empty', 'empty', 1, 0)`)
	if _, ok, err := d.ResumeOf(ctx, 1, 20); ok || err != nil {
		t.Fatalf("a series without episodes: %v %v", ok, err)
	}
}

func TestPrefs(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	prefs := func(user, series int64) Prefs {
		t.Helper()
		p, err := d.PrefsFor(ctx, user, series)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := prefs(1, 10); p != (Prefs{SubMode: "auto"}) {
		t.Fatalf("a new user's defaults: %+v", p)
	}
	defaults := Prefs{AudioLang: "jpn", SubLang: "eng", SubMode: "on"}
	before := seqNow(t, d)
	if err := d.SetDefaultPrefs(ctx, 1, defaults); err != nil {
		t.Fatal(err)
	}
	if prefs(1, 0) != defaults || prefs(1, 10) != defaults || seqNow(t, d) != before {
		t.Fatalf("defaults %+v %+v, seq %d after %d", prefs(1, 0), prefs(1, 10), seqNow(t, d), before)
	}

	own := Prefs{AudioLang: "eng", SubMode: "off"}
	if err := d.SetSeriesPrefs(ctx, 1, 10, own); err != nil {
		t.Fatal(err)
	}
	stamped := seqNow(t, d)
	if prefs(1, 10) != own || prefs(1, 0) != defaults || prefs(2, 10) != (Prefs{SubMode: "auto"}) || stamped == before {
		t.Fatalf("a series' choice: %+v %+v %+v", prefs(1, 10), prefs(1, 0), prefs(2, 10))
	}
	if err := d.SetSeriesPrefs(ctx, 1, 10, own); err != nil || seqNow(t, d) != stamped {
		t.Fatalf("the same choice again: %v, seq %d after %d", err, seqNow(t, d), stamped)
	}
	for _, id := range []int64{50, 101, 999} {
		if err := d.SetSeriesPrefs(ctx, 1, id, own); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a choice for %d: %v", id, err)
		}
	}
	def, series, err := d.AllPrefs(ctx, 1)
	if err != nil || def != defaults || len(series) != 1 || series[0] != (SeriesPrefs{SeriesID: 10, Title: "Harbor Lantern", Prefs: own}) {
		t.Fatalf("all: %+v %+v %v", def, series, err)
	}

	// A reset keeps the row, with its three choices null, so that /changes
	// can carry it.
	if err := d.SetSeriesPrefs(ctx, 1, 10, Prefs{}); err != nil {
		t.Fatal(err)
	}
	var mode any
	var seq int64
	if err := d.r.QueryRow(`SELECT sub_mode, updated_seq FROM series_prefs WHERE user_id = 1 AND series_id = 10`).Scan(&mode, &seq); err != nil {
		t.Fatal(err)
	}
	if mode != nil || seq <= stamped || prefs(1, 10) != defaults {
		t.Fatalf("reset: %v %d, %+v", mode, seq, prefs(1, 10))
	}
	if _, series, _ := d.AllPrefs(ctx, 1); len(series) != 0 {
		t.Fatalf("after a reset: %+v", series)
	}
	// Nothing to reset: no row is written.
	reset := seqNow(t, d)
	if err := d.SetSeriesPrefs(ctx, 1, 10, Prefs{}); err != nil || seqNow(t, d) != reset {
		t.Fatalf("a second reset: %v", err)
	}
	if err := d.SetSeriesPrefs(ctx, 2, 10, Prefs{}); err != nil || seqNow(t, d) != reset {
		t.Fatalf("a reset without a choice: %v", err)
	}
}

func TestPlayLog(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	loaded, playing, dropped, hw := int64(310), int64(640), int64(0), true
	start := PlayStart{At: 1000, UserID: 1, TokenID: 7, ItemID: 102, FileID: 1102, Mode: "direct", Warmed: true,
		LoadedMS: &loaded, PlayingMS: &playing, Decoder: "mediacodec", Hardware: &hw, DroppedFrames: &dropped}
	if err := d.LogPlay(ctx, start); err != nil {
		t.Fatal(err)
	}
	rows, err := d.PlayLog(ctx, 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	r := rows[0]
	if r.User != "ada" || r.Device != "Living room TV" || r.Client != "runnel-test/1.0" || r.Title != "Harbor Lantern · S1 E2" ||
		r.Mode != "direct" || !r.Warmed || *r.LoadedMS != 310 || *r.PlayingMS != 640 || r.ServerFirstByteMS != nil ||
		r.Decoder != "mediacodec" || !*r.Hardware || *r.DroppedFrames != 0 || r.ItemID != 102 || r.At != 1000 {
		t.Fatalf("row %+v", r)
	}

	// The snapshot stays when the device signs out and the item goes.
	exec(t, d, `DELETE FROM tokens WHERE id = 7`)
	exec(t, d, `DELETE FROM items WHERE id = 102`)
	if rows, _ := d.PlayLog(ctx, 0, 10); rows[0].Device != "Living room TV" || rows[0].Title != "Harbor Lantern · S1 E2" {
		t.Fatalf("after the device and the item went: %+v", rows[0])
	}

	// A film is named by its title. The log keeps its newest rows.
	exec(t, d, `WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM k WHERE i < ?)
		INSERT INTO play_log (at, mode) SELECT i, 'direct' FROM k`, PlayLogKeep+2)
	if err := d.LogPlay(ctx, PlayStart{At: 2000, UserID: 1, ItemID: 50, FileID: 500, Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	var n, oldest int64
	d.r.QueryRow(`SELECT count(*), min(id) FROM play_log`).Scan(&n, &oldest)
	rows, _ = d.PlayLog(ctx, 0, 2)
	// 1 + 1002 + 1 rows were written, so the four oldest went.
	if n != PlayLogKeep || oldest != 5 || rows[0].Title != "Paper Comets" || rows[0].Device != "" {
		t.Fatalf("%d rows from %d, newest %+v", n, oldest, rows[0])
	}
	if older, _ := d.PlayLog(ctx, rows[1].ID, 1); len(older) != 1 || older[0].ID != rows[1].ID-1 {
		t.Fatalf("a page before %d: %+v", rows[1].ID, older)
	}
}
