// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// TestCreditsAndPeople: a film's and a series' details carry their
// credits, GET /people/{id} the person, and GET /items?person= their
// titles, newest first, each with the credit it shows first
// (docs/04-api.md, sections 2.4, 4.3 and 4.8). Credits go with the rest
// of what a provider gave, after 180 days without a fetch (D53).
func TestCreditsAndPeople(t *testing.T) {
	b := newBrowse(t)
	ctx := context.Background()
	film, series := b.film(t, "Paper Comets"), b.series(t)
	if err := b.db.ApplyMetadata(ctx, film.ID, db.Metadata{Title: "Paper Comets", SortTitle: "paper comets", Date: "2019-03-08"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := b.db.ApplyMetadata(ctx, series.ID, db.Metadata{Title: "Harbor Lantern", SortTitle: "harbor lantern", Date: "2021-04-05"}, 1); err != nil {
		t.Fatal(err)
	}
	ilse := db.CreditIn{ProviderID: "811", Name: "Ilse Varga", KnownFor: "Acting", PhotoRef: "/ilse.jpg", PhotoKey: "7e8a2b9d0f1e3c5a7b5b1c0e9a3f6d4c"}
	cast := func(c db.CreditIn, role string, episodes int) db.CreditIn {
		c.Kind, c.Role, c.Episodes = "cast", role, episodes
		return c
	}
	if err := b.db.ApplyCredits(ctx, film.ID, "tmdb", []db.CreditIn{cast(ilse, "Ida", 0),
		{ProviderID: "902", Name: "Tomas Reinholt", KnownFor: "Directing", Kind: "crew", Role: "Director", Department: "Directing"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.db.ApplyCredits(ctx, series.ID, "tmdb", []db.CreditIn{cast(ilse, "Edda", 12)}); err != nil {
		t.Fatal(err)
	}

	var d detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(film.ID), b.token, "", nil, &d)
	if d.Credits == nil || len(d.Credits.Cast) != 1 || len(d.Credits.Crew) != 1 || d.Credits.Cast[0].Name != "Ilse Varga" ||
		d.Credits.Cast[0].Role != "Ida" || d.Credits.Cast[0].Episodes != nil || d.Credits.Crew[0].Role != "Director" ||
		d.Credits.Crew[0].Department != "Directing" {
		t.Fatalf("a film's credits: %+v", d.Credits)
	}
	person := d.Credits.Cast[0].PersonID
	raw := b.call(t, "GET", "/api/v1/items/"+idStr(series.ID), b.token, "", nil, nil).Body.String()
	if !strings.Contains(raw, `"credits":{"cast":[{"person_id":`+idStr(person)+`,"name":"Ilse Varga","role":"Edda","episodes":12}],"crew":[]}`) {
		t.Errorf("a series' credits: %s", raw)
	}
	var season itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID), b.token, "", nil, &season)
	if raw := b.call(t, "GET", "/api/v1/items/"+idStr(season.Items[0].ID), b.token, "", nil, nil).Body.String(); strings.Contains(raw, `"credits"`) {
		t.Errorf("a season has credits: %s", raw)
	}

	var p personBody
	if rec := b.call(t, "GET", "/api/v1/people/"+idStr(person), b.token, "", nil, &p); rec.Code != 200 ||
		p.Name != "Ilse Varga" || p.Department != "Acting" || p.Titles != 2 {
		t.Errorf("%d %+v", rec.Code, p)
	}
	for _, path := range []string{"/api/v1/people/99999", "/api/v1/people/x"} {
		if rec := b.call(t, "GET", path, b.token, "", nil, nil); rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	if rec := b.call(t, "GET", "/api/v1/people/"+idStr(person), "", "", nil, nil); rec.Code != 401 {
		t.Errorf("without a token: %d", rec.Code)
	}

	var page itemsPage
	b.call(t, "GET", "/api/v1/items?person="+idStr(person), b.token, "", nil, &page)
	if len(page.Items) != 2 || page.Items[0].ID != series.ID || page.Items[1].ID != film.ID ||
		page.Items[0].Credit == nil || page.Items[0].Credit.Role != "Edda" || *page.Items[0].Credit.Episodes != 12 ||
		page.Items[1].Credit == nil || page.Items[1].Credit.Kind != "cast" || page.Items[1].Credit.Episodes != nil {
		t.Errorf("a person's titles: %+v", page.Items)
	}
	page = itemsPage{}
	b.call(t, "GET", "/api/v1/items?person="+idStr(person)+"&type=movie&sort=title", b.token, "", nil, &page)
	if len(page.Items) != 1 || page.Items[0].ID != film.ID {
		t.Errorf("films only: %+v", page.Items)
	}
	page = itemsPage{}
	b.call(t, "GET", "/api/v1/items?library=1", b.token, "", nil, &page)
	for _, c := range page.Items {
		if c.Credit != nil {
			t.Errorf("a library's card carries a credit: %+v", c)
		}
	}
	for _, query := range []string{"person=99999", "person=x"} {
		if rec := b.call(t, "GET", "/api/v1/items?"+query, b.token, "", nil, nil); rec.Code != 404 {
			t.Errorf("%s: %d", query, rec.Code)
		}
	}

	if _, err := b.db.DropStale(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.db.Credits(ctx, film.ID); len(list) != 0 {
		t.Errorf("credits kept after the drop: %+v", list)
	}
	if n, err := b.db.DropUncredited(ctx); err != nil || n != 2 {
		t.Errorf("dropped %d people, %v", n, err)
	}
}
