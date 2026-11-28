// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
)

// shown is an item's credits as one line each: kind, name, role,
// department and episodes.
func shown(t *testing.T, d *db.DB, id int64) []string {
	t.Helper()
	list, err := d.Credits(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range list {
		out = append(out, fmt.Sprintf("%s %s: %s %s %d", c.Kind, c.Name, c.Role, c.Department, c.Episodes))
	}
	return out
}

func invented(id int, role map[string]any) map[string]any {
	p := map[string]any{"id": id, "name": fmt.Sprintf("Invented Person %d", id), "known_for_department": "Acting",
		"profile_path": fmt.Sprintf("/person%d.jpg", id)}
	for k, v := range role {
		p[k] = v
	}
	return p
}

// TestFilmCredits: a film keeps the first 20 of its cast in billing
// order, then its directors, then its writers, each once with their jobs
// joined (docs/06-library-and-metadata.md, section 6.3; P7), read from the
// answer the details already brought. A refresh that changes nothing
// leaves the film's sequence value alone.
func TestFilmCredits(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "movies")
	e.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019).mkv")
	var film map[string]any
	json.Unmarshal([]byte(movieJSON(101, "Paper Comets", "Two sisters.", "2019-03-08")), &film)
	var cast []any
	for i := 25; i >= 1; i-- { // listed out of billing order
		cast = append(cast, invented(i, map[string]any{"character": fmt.Sprintf("Role %d", i), "order": i - 1}))
	}
	crew := []any{
		invented(90, map[string]any{"job": "Producer", "department": "Production"}),
		invented(91, map[string]any{"job": "Screenplay", "department": "Writing"}),
		invented(92, map[string]any{"job": "Director", "department": "Directing"}),
		invented(92, map[string]any{"job": "Assistant Director", "department": "Directing"}),
		invented(91, map[string]any{"job": "Story", "department": "Writing"}),
		invented(92, map[string]any{"job": "Writer", "department": "Writing"}),
	}
	film["credits"] = map[string]any{"cast": cast, "crew": crew}
	b, _ := json.Marshal(film)
	e.page("/movie/101", tmdb.MovieQuery(), string(b))
	e.scan()
	if n := e.requests(); n != 1 {
		t.Errorf("%d requests, want the details' one", n)
	}
	got := shown(t, e.db, e.item("movie", "Paper Comets").ID)
	if len(got) != 23 || got[0] != "cast Invented Person 1: Role 1  0" || got[19] != "cast Invented Person 20: Role 20  0" ||
		got[20] != "crew Invented Person 92: Director Directing 0" || got[21] != "crew Invented Person 91: Screenplay, Story Writing 0" ||
		got[22] != "crew Invented Person 92: Writer Writing 0" {
		t.Errorf("credits:\n%s", strings.Join(got, "\n"))
	}
	before := e.item("movie", "Paper Comets")
	if err := e.f.Refresh(ctx, before.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if after := e.item("movie", "Paper Comets"); after.Seq != before.Seq {
		t.Errorf("a refresh that changed nothing moved the film from %d to %d", before.Seq, after.Seq)
	}

	// A new photo path is a new key; a person no longer credited goes
	// at the nightly clean-up.
	cast[0] = invented(25, map[string]any{"character": "Role 25", "order": 24, "profile_path": "/person25-new.jpg"})
	film["credits"] = map[string]any{"cast": cast[:1], "crew": []any{}}
	b, _ = json.Marshal(film)
	e.page("/movie/101", tmdb.MovieQuery(), string(b))
	if err := e.f.Refresh(ctx, before.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	list, _ := e.db.Credits(ctx, before.ID)
	if len(list) != 1 || list[0].PhotoKey != providerKey("tmdb", "/person25-new.jpg") {
		t.Errorf("after the change: %+v", list)
	}
	if after := e.item("movie", "Paper Comets"); after.Seq == before.Seq {
		t.Errorf("changed credits did not move the film")
	}
	if n, err := e.db.DropUncredited(ctx); err != nil || n != 22 {
		t.Errorf("dropped %d people, %v; want the 22 credited before", n, err)
	}
	if p, err := e.db.PersonOf(ctx, list[0].PersonID); err != nil || p.Titles != 1 || p.Department != "Acting" {
		t.Errorf("the person left: %+v, %v", p, err)
	}
}

// TestSeriesCredits: a series' answer without aggregate_credits asks for
// them by themselves, once; its cast carry their episodes and the role
// they play most; its creators come first among the writers, and the crew
// go by episodes, most first.
func TestSeriesCredits(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "shows")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 01/Harbor Lantern - S01E01.mkv")
	e.page("/tv/500", tmdb.SeriesQuery(), `{"id":500,"name":"Harbor Lantern","first_air_date":"2021-04-05",
		"seasons":[{"season_number":1,"episode_count":1}],"created_by":[{"id":70,"name":"Invented Person 70","profile_path":null}],
		"images":{}}`)
	e.page("/tv/500/season/1", tmdb.SeasonQuery(), `{"season_number":1,"name":"Season 1","episodes":[{"episode_number":1,"name":"The Quiet Morning"}]}`)
	credits, _ := json.Marshal(map[string]any{
		"cast": []any{
			invented(2, map[string]any{"order": 1, "total_episode_count": 3, "roles": []any{
				map[string]any{"character": "Young Edda", "episode_count": 1}, map[string]any{"character": "Edda", "episode_count": 2}}}),
			invented(1, map[string]any{"order": 0, "total_episode_count": 12, "roles": []any{map[string]any{"character": "Ida", "episode_count": 12}}}),
		},
		"crew": []any{
			invented(80, map[string]any{"department": "Directing", "total_episode_count": 2, "jobs": []any{map[string]any{"job": "Director", "episode_count": 2}}}),
			invented(81, map[string]any{"department": "Directing", "total_episode_count": 9, "jobs": []any{map[string]any{"job": "Director", "episode_count": 9}}}),
			invented(70, map[string]any{"department": "Writing", "total_episode_count": 12, "jobs": []any{map[string]any{"job": "Writer", "episode_count": 12}}}),
			invented(82, map[string]any{"department": "Writing", "total_episode_count": 4, "jobs": []any{map[string]any{"job": "Story", "episode_count": 4}}}),
			invented(83, map[string]any{"department": "Camera", "total_episode_count": 12, "jobs": []any{map[string]any{"job": "Director of Photography", "episode_count": 12}}}),
		},
	})
	e.page("/tv/500/aggregate_credits", tmdb.AggregateCreditsQuery(), string(credits))
	e.scan()
	got := shown(t, e.db, e.item("series", "Harbor Lantern").ID)
	want := []string{
		"cast Invented Person 1: Ida  12", "cast Invented Person 2: Edda  3",
		"crew Invented Person 81: Director Directing 9", "crew Invented Person 80: Director Directing 2",
		"crew Invented Person 70: Creator Writing 0", "crew Invented Person 82: Story Writing 4",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("credits:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	asked := e.requests()
	if asked != 3 {
		t.Errorf("%d requests, want the details, the credits and a season", asked)
	}
	if _, err := e.f.Queue(ctx, e.lib.ID, true); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := e.requests(); n != asked {
		t.Errorf("a second full refresh made %d requests", n-asked)
	}
	var creator db.Credit
	for _, c := range must(e.db.Credits(ctx, e.item("series", "Harbor Lantern").ID)) {
		if c.Role == "Creator" {
			creator = c
		}
	}
	if creator.PhotoKey != "" {
		t.Errorf("a creator without a photo has the key %q", creator.PhotoKey)
	}
}
