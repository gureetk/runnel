// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"slices"
	"testing"
)

// changesFixture writes rows at chosen sequence values:
//
//	1  library 1
//	2  300 films, 1001 to 1300
//	3  500 films, 1301 to 1800
//	4  100 episodes, 3001 to 3100, of season 11 of series 10
//	5  user 1's state of three films
//	6  user 2's state of five films
//	7  series 10 and season 11, counting a new episode 3101
//	8  film 1001 deleted
//	9  user 1 puts film 1002 on the list
//	10 user 1 resets the track choice of series 10
func changesFixture(t *testing.T) *DB {
	t.Helper()
	d, _ := openTemp(t)
	exec(t, d, `INSERT INTO libraries (id, name, kind, created_at, updated_seq) VALUES (1, 'Mixed', 'shows', 1, 1)`)
	exec(t, d, `INSERT INTO roots (id, library_id, path) VALUES (1, 1, '/m')`)
	exec(t, d, `INSERT INTO users (id, name, created_at) VALUES (1, 'ada', 1), (2, 'bo', 1)`)
	exec(t, d, `WITH RECURSIVE k(i) AS (SELECT 1001 UNION ALL SELECT i + 1 FROM k WHERE i < 1800)
		INSERT INTO items (id, library_id, type, root_id, path_key, title, sort_title, added_at, updated_seq)
		SELECT i, 1, 'movie', 1, 'f' || i, 'Film ' || i, 'film ' || i, 1, CASE WHEN i <= 1300 THEN 2 ELSE 3 END FROM k`)
	exec(t, d, `INSERT INTO items (id, library_id, type, parent_id, series_id, season_number, root_id, path_key, title, sort_title, added_at, updated_seq)
		VALUES (10, 1, 'series', NULL, NULL, NULL, 1, 's', 'Harbor Lantern', 'harbor lantern', 1, 7),
		       (11, 1, 'season', 10, 10, 1, NULL, NULL, 'Season 1', 'season 1', 1, 7)`)
	exec(t, d, `WITH RECURSIVE k(i) AS (SELECT 3001 UNION ALL SELECT i + 1 FROM k WHERE i < 3101)
		INSERT INTO items (id, library_id, type, parent_id, series_id, season_number, episode_number, title, sort_title, added_at, updated_seq)
		SELECT i, 1, 'episode', 11, 10, 1, i - 3000, 'Episode', 'episode', 1, CASE WHEN i < 3101 THEN 4 ELSE 7 END FROM k`)
	exec(t, d, `INSERT INTO user_state (user_id, item_id, position_ms, played, play_count, updated_seq) VALUES
		(1, 1002, 60000, 0, 0, 5), (1, 1003, 0, 1, 1, 5), (1, 1004, 0, 1, 1, 5),
		(2, 1002, 0, 1, 1, 6), (2, 1003, 0, 1, 1, 6), (2, 1004, 0, 1, 1, 6), (2, 1005, 0, 1, 1, 6), (2, 1006, 0, 1, 1, 6)`)
	exec(t, d, `DELETE FROM items WHERE id = 1001`)
	exec(t, d, `INSERT INTO tombstones (seq, item_id, at) VALUES (8, 1001, 1)`)
	exec(t, d, `INSERT INTO user_list (user_id, item_id, listed_at, updated_seq) VALUES (1, 1002, 99, 9)`)
	exec(t, d, `INSERT INTO series_prefs (user_id, series_id, updated_seq) VALUES (1, 10, 10)`)
	exec(t, d, `UPDATE meta SET value = '10' WHERE key = 'seq'`)
	return d
}

// pageOf reads a whole page: the ids of its items in order, and how many
// rows of each part it has.
type pageOf struct {
	since, upto, latest int64
	reset               bool
	items               []int64
	itemSeqs            []int64
	types               []string
	states, prefs, list int
	deleted             []int64
	libraries, all      []int64
}

func readPage(t *testing.T, d *DB, user, since int64, limit int) pageOf {
	t.Helper()
	ctx := context.Background()
	c, err := d.Changes(ctx, user, since, limit)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := pageOf{since: c.Since, upto: c.Upto, latest: c.Latest, reset: c.Reset}
	if err := c.Items(ctx, func(card Card) error {
		p.items, p.itemSeqs, p.types = append(p.items, card.ID), append(p.itemSeqs, card.Seq), append(p.types, card.Type)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.States(ctx, func(ChangedState) error { p.states++; return nil })
	c.Prefs(ctx, func(ChangedPrefs) error { p.prefs++; return nil })
	c.List(ctx, func(ChangedListing) error { p.list++; return nil })
	c.Deleted(ctx, func(id int64) error { p.deleted = append(p.deleted, id); return nil })
	libs, all, err := c.Libraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range libs {
		p.libraries = append(p.libraries, l.ID)
	}
	p.all = all
	return p
}

func TestChangesPages(t *testing.T) {
	d := changesFixture(t)

	// Whole values only: 1 and 2 make 300 rows, and 3 would pass 500.
	p := readPage(t, d, 1, 0, 500)
	if p.upto != 2 || p.latest != 10 || p.reset || len(p.items) != 299 || !slices.Equal(p.libraries, []int64{1}) ||
		!slices.Equal(p.all, []int64{1}) {
		t.Fatalf("first page: upto %d, %d items, libraries %v", p.upto, len(p.items), p.libraries)
	}
	// One value of 500 rows fills a page of 500.
	if p = readPage(t, d, 1, 2, 500); p.upto != 3 || len(p.items) != 500 {
		t.Fatalf("second page: upto %d, %d items", p.upto, len(p.items))
	}
	// A limit below a value's rows still takes the whole value.
	if p = readPage(t, d, 1, 2, 10); p.upto != 3 || len(p.items) != 500 {
		t.Fatalf("a small limit: upto %d, %d items", p.upto, len(p.items))
	}

	// Value 4 holds episodes whose season and series changed at 7: they
	// come first on the page, and again at 7.
	p = readPage(t, d, 1, 3, 100)
	if p.upto != 4 || len(p.items) != 102 || p.items[0] != 10 || p.items[1] != 11 || p.types[2] != "episode" || p.itemSeqs[0] != 7 {
		t.Fatalf("episodes with their parents: upto %d, %d items, first %v", p.upto, len(p.items), p.items[:3])
	}
	// The rest: the user's state at 5; 6 is another user's; the parents
	// and the new episode at 7, parents first; a deletion, a listing, a
	// reset.
	p = readPage(t, d, 1, 4, 100)
	if p.upto != 10 || p.states != 3 || !slices.Equal(p.items, []int64{10, 11, 3101}) || !slices.Equal(p.deleted, []int64{1001}) ||
		p.list != 1 || p.prefs != 1 || len(p.libraries) != 0 {
		t.Fatalf("the rest: %+v", p)
	}
	if other := readPage(t, d, 2, 4, 100); other.states != 5 || other.list != 0 || other.prefs != 0 {
		t.Fatalf("another user's page: %+v", other)
	}
	// Nothing left: the page reaches the latest value and is empty.
	if p = readPage(t, d, 1, 10, 100); p.upto != 10 || len(p.items)+p.states+len(p.deleted) != 0 {
		t.Fatalf("up to date: %+v", p)
	}

	// Below the floor, or ahead of the sequence, a copy starts again.
	exec(t, d, `UPDATE meta SET value = '5' WHERE key = 'tombstone_floor'`)
	for _, since := range []int64{3, 11} {
		if p = readPage(t, d, 1, since, 100); !p.reset || len(p.items) != 0 || !slices.Equal(p.all, []int64{1}) {
			t.Fatalf("since %d: %+v", since, p)
		}
	}
	if p = readPage(t, d, 1, 0, 100); p.reset {
		t.Fatal("a full copy was told to reset")
	}
	if p = readPage(t, d, 1, 5, 100); p.reset {
		t.Fatal("a copy at the floor was told to reset")
	}
}

func TestPrune(t *testing.T) {
	d := changesFixture(t)
	ctx := context.Background()
	const day = 24 * 60 * 60 * 1000
	now := int64(200 * day)
	exec(t, d, `DELETE FROM tombstones`)
	exec(t, d, `INSERT INTO tombstones (seq, item_id, at) VALUES (3, 1, ?), (6, 2, ?), (6, 3, ?), (9, 4, ?)`,
		now-100*day, now-91*day, now-91*day, now-10*day)
	exec(t, d, `INSERT INTO user_list (user_id, item_id, listed_at, updated_seq) VALUES (1, 1003, NULL, 6), (1, 1004, NULL, 7), (2, 1003, 5, 2)`)
	exec(t, d, `INSERT INTO series_prefs (user_id, series_id, audio_lang, sub_lang, sub_mode, updated_seq) VALUES (2, 10, 'eng', NULL, 'on', 1)`)
	exec(t, d, `UPDATE series_prefs SET updated_seq = 4 WHERE user_id = 1`)

	p, err := d.Prune(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if p != (Pruned{Tombstones: 3, Listings: 1, Prefs: 1, Floor: 6}) {
		t.Fatalf("pruned %+v", p)
	}
	var tombstones, listings, prefs int
	d.r.QueryRow(`SELECT (SELECT count(*) FROM tombstones), (SELECT count(*) FROM user_list), (SELECT count(*) FROM series_prefs)`).
		Scan(&tombstones, &listings, &prefs)
	// Left: the newer marker; a listing taken off above the floor and the
	// ones still on; the choice that was not a reset.
	if tombstones != 1 || listings != 3 || prefs != 1 {
		t.Fatalf("left %d tombstones, %d listings, %d prefs", tombstones, listings, prefs)
	}
	if c := readPage(t, d, 1, 5, 100); !c.reset {
		t.Fatal("a copy below the new floor was not reset")
	}
	if again, err := d.Prune(ctx, now); err != nil || again != (Pruned{Floor: 6}) {
		t.Fatalf("again: %+v %v", again, err)
	}
}
