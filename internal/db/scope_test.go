// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"slices"
	"testing"
)

// TestScope: a scope's files are those whose top name it holds, matched
// whole, whatever characters the names hold; a root read whole has all its
// files; a root not in it has none; and a build of a scope reads and
// writes nothing outside it.
func TestScope(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	lib, err := d.CreateLibrary(ctx, "Films", "movies", "default", []string{"/a", "/b"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := lib.Roots[0].ID, lib.Roots[1].ID
	addFilms(t, d, lib.ID, ra, "50% Off", "A_B", "AxB", "It's \"Here\"", "Ünder", "Ab")
	addFilms(t, d, lib.ID, rb, "Elsewhere")
	paths := func(s Scope) []string {
		t.Helper()
		files, err := d.LibraryFilesIn(ctx, lib.ID, s)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, f := range files {
			out = append(out, f.RelPath)
		}
		slices.Sort(out)
		return out
	}
	for _, c := range []struct {
		scope Scope
		want  []string
	}{
		{Scope{ra: {"A_B"}}, []string{"A_B/A_B.mkv"}},
		{Scope{ra: {"50% Off", "It's \"Here\""}}, []string{"50% Off/50% Off.mkv", "It's \"Here\"/It's \"Here\".mkv"}},
		{Scope{ra: {"Ünder"}, rb: nil}, []string{"Elsewhere/Elsewhere.mkv", "Ünder/Ünder.mkv"}},
		{Scope{ra: {"A"}}, nil},
		{Scope{ra: {}}, nil},
		{Scope{}, nil},
	} {
		if got := paths(c.scope); !slices.Equal(got, c.want) {
			t.Errorf("%v: %q, want %q", c.scope, got, c.want)
		}
	}
	if got := paths(nil); len(got) != 7 {
		t.Errorf("no scope: %q", got)
	}

	// The items that play files, by their tops.
	files, _ := d.LibraryFilesIn(ctx, lib.ID, Scope{ra: {"AxB"}, rb: nil})
	owners, err := d.ItemsPlaying(ctx, []int64{files[0].ID, files[1].ID})
	if err != nil || len(owners) != 2 || !owners.Has(ra, "AxB") || !owners.Has(rb, "Elsewhere") || owners.Has(ra, "A_B") {
		t.Fatalf("items playing: %v %v", owners, err)
	}

	// A build of one folder that wants nothing there takes its film away,
	// and leaves the rest as they were.
	before, _ := d.LibraryItems(ctx, lib.ID)
	res, err := d.BuildItemsIn(ctx, lib.ID, Scope{ra: {"AxB"}}, nil, nil, func(s string) string { return s }, 5)
	if err != nil || res != (BuildResult{Removed: 1}) {
		t.Fatalf("a build of one folder: %+v %v", res, err)
	}
	after, _ := d.LibraryItems(ctx, lib.ID)
	if len(after) != len(before)-1 {
		t.Fatalf("%d items left of %d", len(after), len(before))
	}
	for _, it := range after {
		if it.PathKey == "AxB" {
			t.Fatal("the film of the folder built is still there")
		}
	}

	var s Scope = Scope{}
	s.Add(ra, "x", "y", "x")
	s.Whole(rb)
	s.Add(rb, "z")
	if !slices.Equal(s[ra], []string{"x", "y"}) || s[rb] != nil || !s.Has(rb, "anything") || s.Has(ra, "z") {
		t.Fatalf("%v", s)
	}
	if Top("a/b/c.mkv") != "a" || Top("c.mkv") != "c.mkv" {
		t.Fatal("Top")
	}
}
