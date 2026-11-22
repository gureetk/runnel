// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"errors"
	"maps"
	"testing"
)

func TestMyList(t *testing.T) {
	d := watchFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		id   int64
		want error
	}{{101, ErrNotListable}, {11, ErrNotListable}, {999, ErrNotFound}} {
		if err := d.SetListed(ctx, 1, c.id, true, 5); !errors.Is(err, c.want) {
			t.Errorf("listing %d: %v, want %v", c.id, err, c.want)
		}
	}
	for _, c := range []struct{ id, at int64 }{{50, 10}, {10, 20}, {60, 30}} {
		if err := d.SetListed(ctx, 1, c.id, true, c.at); err != nil {
			t.Fatal(err)
		}
	}

	// Listing again changes nothing: no new time, no sequence value.
	before := seqNow(t, d)
	if err := d.SetListed(ctx, 1, 50, true, 99); err != nil || seqNow(t, d) != before {
		t.Fatalf("listing again: %v, seq %d after %d", err, seqNow(t, d), before)
	}
	got, err := d.ListedOf(ctx, 1, []int64{50, 10, 60, 101})
	if err != nil || !maps.Equal(got, map[int64]int64{50: 10, 10: 20, 60: 30}) {
		t.Fatalf("listed %v %v", got, err)
	}
	if other, _ := d.ListedOf(ctx, 2, []int64{50}); len(other) != 0 {
		t.Fatalf("another user's list: %v", other)
	}

	// Pages, newest first and by title, of both types or one.
	page := func(typ string, sort string, desc bool, after *ListKey, limit int) []int64 {
		t.Helper()
		q := ListQuery{UserID: 1, Listed: true, Played: -1, Sort: sort, Desc: desc}
		if typ != "" {
			q.Types = []string{typ}
		}
		cards, keys, err := d.ListItems(ctx, q, after, 0, limit)
		if err != nil || len(keys) != len(cards) {
			t.Fatal(err)
		}
		var ids []int64
		for _, c := range cards {
			ids = append(ids, c.ID)
		}
		return ids
	}
	if ids := page("", "listed", true, nil, 2); len(ids) != 2 || ids[0] != 60 || ids[1] != 10 {
		t.Fatalf("newest first: %v", ids)
	}
	if ids := page("", "listed", true, &ListKey{Value: int64(20), Title: "harbor lantern", ID: 10}, 10); len(ids) != 1 || ids[0] != 50 {
		t.Fatalf("after the second: %v", ids)
	}
	if ids := page("", "title", false, nil, 10); len(ids) != 3 || ids[0] != 10 || ids[1] != 50 || ids[2] != 60 {
		t.Fatalf("by title: %v", ids)
	}
	if ids := page("movie", "listed", true, nil, 10); len(ids) != 2 || ids[0] != 60 || ids[1] != 50 {
		t.Fatalf("films: %v", ids)
	}
	if n, err := d.CountItems(ctx, ListQuery{UserID: 1, Listed: true, Played: -1, Types: []string{"series"}}); err != nil || n != 1 {
		t.Fatalf("count %d %v", n, err)
	}

	// Taking off keeps the row with no time and a new sequence value, so
	// that /changes can say it went. Taking off again writes nothing.
	if err := d.SetListed(ctx, 1, 50, false, 40); err != nil {
		t.Fatal(err)
	}
	var at any
	var seq int64
	if err := d.r.QueryRow(`SELECT listed_at, updated_seq FROM user_list WHERE user_id = 1 AND item_id = 50`).Scan(&at, &seq); err != nil {
		t.Fatal(err)
	}
	if at != nil || seq != seqNow(t, d) || seq == before {
		t.Fatalf("taken off: %v %d", at, seq)
	}
	if err := d.SetListed(ctx, 1, 50, false, 50); err != nil || seqNow(t, d) != seq {
		t.Fatalf("taking off again: %v", err)
	}
	if err := d.SetListed(ctx, 2, 50, false, 50); err != nil || seqNow(t, d) != seq {
		t.Fatalf("taking off what was never on: %v", err)
	}
	if n, _ := d.CountItems(ctx, ListQuery{UserID: 1, Listed: true, Played: -1}); n != 2 {
		t.Fatalf("count after taking off: %d", n)
	}
	// Put back, it is listed anew.
	if err := d.SetListed(ctx, 1, 50, true, 60); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.ListedOf(ctx, 1, []int64{50}); got[50] != 60 {
		t.Fatalf("put back: %v", got)
	}
}
