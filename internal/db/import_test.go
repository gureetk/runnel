// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"errors"
	"testing"
)

func TestImportJoins(t *testing.T) {
	d, filmsID, showsID := adminFixture(t)
	ctx := context.Background()
	films, _ := d.Library(ctx, filmsID)
	shows, _ := d.Library(ctx, showsID)
	if j, err := d.ItemOfFile(ctx, films.Roots[0].ID, "Low Water/Low Water.mkv"); err != nil || j.Type != "movie" || j.FileID == 0 || j.MatchState != "unmatched" {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := d.ItemOfFile(ctx, films.Roots[0].ID, "Gone/Gone.mkv"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no file: %v", err)
	}
	exec(t, d, `INSERT INTO files (root_id, rel_path, kind, size, mtime_ns, first_seen_at) VALUES (1, 'Low Water/extras/x.mkv', 'extra', 1, 1, 1)`)
	if _, err := d.ItemOfFile(ctx, films.Roots[0].ID, "Low Water/extras/x.mkv"); !errors.Is(err, ErrNoItem) {
		t.Fatalf("an extra: %v", err)
	}
	if j, err := d.SeriesOfFolder(ctx, shows.Roots[0].ID, "Harbor Lantern"); err != nil || j.Type != "series" || j.ItemID == 0 {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := d.SeriesOfFolder(ctx, shows.Roots[0].ID, "Harbor Lantern/Season 01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a season's folder: %v", err)
	}
}

func TestImportMatches(t *testing.T) {
	d, filmsID, _ := adminFixture(t)
	ctx := context.Background()
	ids := itemIDs(t, d, filmsID)
	exec(t, d, `UPDATE items SET match_state = 'manual' WHERE id = ?`, ids[1])
	exec(t, d, `UPDATE items SET match_state = 'ignored' WHERE id = ?`, ids[2])
	in := []ImportMatch{{ItemID: ids[0], IDs: map[string]string{"tmdb": "1"}}, {ItemID: ids[1], IDs: map[string]string{"tmdb": "2"}},
		{ItemID: ids[2], IDs: map[string]string{"tmdb": "3"}}, {ItemID: 999, IDs: map[string]string{"tmdb": "4"}}}
	seq, _ := d.Seq(ctx)
	if n, err := d.ImportMatches(ctx, in, false); err != nil || n != 1 {
		t.Fatalf("dry: %d %v", n, err)
	}
	if after, _ := d.Seq(ctx); after != seq || count(t, d, `SELECT count(*) FROM external_ids`) != 0 {
		t.Fatal("a dry run wrote")
	}
	if n, err := d.ImportMatches(ctx, in, true); err != nil || n != 1 {
		t.Fatalf("write: %d %v", n, err)
	}
	if m, _ := d.MatchOf(ctx, ids[0]); m.State != "matched" || m.ExternalIDs["tmdb"] != "1" {
		t.Fatalf("%+v", m)
	}
	if m, _ := d.MatchOf(ctx, ids[1]); m.State != "manual" || len(m.ExternalIDs) != 0 {
		t.Fatalf("a manual match: %+v", m)
	}
	if after, _ := d.Seq(ctx); after == seq {
		t.Fatal("the matched item was not stamped")
	}
	if n, _ := d.ImportMatches(ctx, in, true); n != 0 {
		t.Fatalf("again: %d", n)
	}
	// A changed id is written, and the other ids stay.
	exec(t, d, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, 'anidb', '9')`, ids[0])
	if n, _ := d.ImportMatches(ctx, []ImportMatch{{ItemID: ids[0], IDs: map[string]string{"tmdb": "5"}}}, true); n != 1 {
		t.Fatalf("a changed id: %d", n)
	}
	if m, _ := d.MatchOf(ctx, ids[0]); m.ExternalIDs["tmdb"] != "5" || m.ExternalIDs["anidb"] != "9" {
		t.Fatalf("%+v", m)
	}
}

func TestImportStates(t *testing.T) {
	d, filmsID, _ := adminFixture(t)
	ctx := context.Background()
	ids := itemIDs(t, d, filmsID)
	u, _ := d.CreateUser(ctx, "ada", nil, true, 1)
	if _, err := d.MarkPlayed(ctx, u.ID, []int64{ids[2]}, true, 5000); err != nil {
		t.Fatal(err)
	}
	in := []ImportState{
		{UserID: u.ID, ItemID: ids[0], Played: true, PlayCount: 3, LastPlayedAt: 4000, PositionMS: 70_000},
		{UserID: u.ID, ItemID: ids[1], FileID: 7, PositionMS: 60_000, LastPlayedAt: 4000},
		{UserID: u.ID, ItemID: ids[2], PositionMS: 60_000, LastPlayedAt: 4000}, // played later here
		{UserID: u.ID, ItemID: ids[1] + 1000},                                  // says nothing
	}
	if n, err := d.ImportStates(ctx, in, false); err != nil || n != 2 {
		t.Fatalf("dry: %d %v", n, err)
	}
	files, _ := d.LibraryFiles(ctx, filmsID)
	in[1].FileID = files[1].ID
	if n, err := d.ImportStates(ctx, in, true); err != nil || n != 2 {
		t.Fatalf("write: %d %v", n, err)
	}
	got, _ := d.StatesOf(ctx, u.ID, ids)
	if s := got[ids[0]]; !s.Played || s.PositionMS != 0 || s.PlayCount != 3 || s.LastPlayedAt != 4000 {
		t.Fatalf("played: %+v", s)
	}
	if s := got[ids[1]]; s.Played || s.PositionMS != 60_000 {
		t.Fatalf("a position: %+v", s)
	}
	if s := got[ids[2]]; !s.Played || s.LastPlayedAt != 5000 {
		t.Fatalf("newer here: %+v", s)
	}
	if n, _ := d.ImportStates(ctx, in, true); n != 0 {
		t.Fatalf("again: %d", n)
	}
	// The count is the greater of the two.
	if n, _ := d.ImportStates(ctx, []ImportState{{UserID: u.ID, ItemID: ids[0], Played: true, PlayCount: 1, LastPlayedAt: 4500}}, true); n != 1 {
		t.Fatalf("a later play: %d", n)
	}
	if s, _ := d.StatesOf(ctx, u.ID, ids[:1]); s[ids[0]].PlayCount != 3 || s[ids[0]].LastPlayedAt != 4500 {
		t.Fatalf("%+v", s[ids[0]])
	}
}
