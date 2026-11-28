// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEpisodesWithoutANumber follows D51 through the API: a decimal and a
// name without a number make episodes, listed in their season after the
// number they follow or after every number, with a null episode on their
// cards; prev and next step through them; Next up and resume step over them.
func TestEpisodesWithoutANumber(t *testing.T) {
	b := newBrowseWith(t,
		"Copper Tower (2007)/Season 01/Copper Tower - S01E08.mkv",
		"Copper Tower (2007)/Season 01/[Grp]Copper Tower_-_08.5_(1080p).mkv",
		"Copper Tower (2007)/Season 01/Copper Tower - S01E09.mkv",
		"Copper Tower (2007)/Season 01/Copper Tower - Behind the Lighthouse.mkv",
		"Copper Tower (2007)/Season 00/[Grp] Copper Tower S1 - S02.mkv")
	series := b.series(t)
	var d detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(series.ID), b.token, "", nil, &d)
	if d.Seasons == nil || len(*d.Seasons) != 2 {
		t.Fatalf("seasons %+v", d.Seasons)
	}
	season1, specials := (*d.Seasons)[0], (*d.Seasons)[1]
	if *season1.Counts.Episodes != 4 || *specials.Counts.Episodes != 1 {
		t.Fatalf("counts: season 1 %+v, specials %+v", season1.Counts, specials.Counts)
	}

	// The season's list, one at a time to cross the cursor: 8, 8.5, 9,
	// then the one without any number.
	var order []string
	var eps []cardBody
	cursor := ""
	for range 10 {
		var page itemsPage
		b.call(t, "GET", "/api/v1/items?parent="+idStr(season1.ID)+"&limit=1"+cursor, b.token, "", nil, &page)
		eps = append(eps, page.Items...)
		if page.Next == nil {
			break
		}
		cursor = "&cursor=" + *page.Next
	}
	for _, e := range eps {
		n := "-"
		if e.Episode != nil && !e.Episode.None {
			n = idStr(int64(e.Episode.N))
		}
		order = append(order, n+":"+e.Title)
	}
	if got := strings.Join(order, " | "); got != "8:Episode 8 | -:Copper Tower - 08.5 | 9:Episode 9 | -:Copper Tower - Behind the Lighthouse" {
		t.Fatalf("season 1: %s", got)
	}

	// On the wire, an episode without a number has "episode": null.
	var rawPage struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	rec := b.call(t, "GET", "/api/v1/items?parent="+idStr(season1.ID), b.token, "", nil, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &rawPage); err != nil {
		t.Fatal(err)
	}
	if e, ok := rawPage.Items[1]["episode"]; !ok || string(e) != "null" {
		t.Fatalf("an episode without a number: %s", rec.Body.String())
	}
	if string(rawPage.Items[0]["episode"]) != "8" {
		t.Fatalf("a numbered episode: %s", rawPage.Items[0]["episode"])
	}

	// prev and next step through every episode of the season in order.
	var e85 detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(eps[1].ID), b.token, "", nil, &e85)
	if e85.Prev == nil || e85.Prev.ID != eps[0].ID || e85.Next == nil || e85.Next.ID != eps[2].ID {
		t.Fatalf("8.5: prev %+v next %+v", e85.Prev, e85.Next)
	}
	var e8 detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(eps[0].ID), b.token, "", nil, &e8)
	// JSON null decodes to a nil pointer; the wire's null is checked above.
	if e8.Next == nil || e8.Next.ID != eps[1].ID || (e8.Next.Episode != nil && !e8.Next.Episode.None) {
		t.Fatalf("8's next: %+v", e8.Next)
	}

	// Played 8: Next up and resume go to 9, stepping over 8.5.
	b.call(t, "PUT", "/api/v1/items/"+idStr(eps[0].ID)+"/played", b.token, "", nil, nil)
	if r := b.resumeOf(t, series.ID); r.ID != eps[2].ID {
		t.Fatalf("resume after 8: %+v, want 9 (%d)", r, eps[2].ID)
	}
	var home struct {
		NextUp []cardBody `json:"next_up"`
	}
	b.call(t, "GET", "/api/v1/home", b.token, "", nil, &home)
	if len(home.NextUp) != 1 || home.NextUp[0].ID != eps[2].ID {
		t.Fatalf("next up after 8: %+v", home.NextUp)
	}
	// Played 8.5 as well, later: it is not where Next up counts from.
	b.call(t, "PUT", "/api/v1/items/"+idStr(eps[1].ID)+"/played", b.token, "", nil, nil)
	b.call(t, "GET", "/api/v1/home", b.token, "", nil, &home)
	if len(home.NextUp) != 1 || home.NextUp[0].ID != eps[2].ID {
		t.Fatalf("next up after 8.5: %+v", home.NextUp)
	}
}

func (b *browse) resumeOf(t *testing.T, series int64) resumeBody {
	t.Helper()
	var d detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(series), b.token, "", nil, &d)
	if d.Resume == nil {
		t.Fatalf("no resume on %+v", d)
	}
	return *d.Resume
}
