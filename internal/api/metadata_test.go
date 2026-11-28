// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// TestCardsCarryMetadata: what a provider says of a film is on its card in
// every list, in /changes and on its detail, with the original title on
// the detail only (docs/04-api.md, sections 2.1 and 2.2).
func TestCardsCarryMetadata(t *testing.T) {
	b := newBrowse(t)
	film := b.film(t, "Paper Comets")
	if film.Overview != "" || film.Date != "" || film.Rating != nil || film.Genres != nil || film.RuntimeMS != nil {
		t.Fatalf("a card before metadata: %+v", film)
	}
	if err := b.db.ApplyMetadata(context.Background(), film.ID, db.Metadata{Title: "Paper Comets", SortTitle: "paper comets",
		OriginalTitle: "Papierkometen", Overview: "Two sisters chart a comet from a rooftop.", Date: "2019-03-08",
		RuntimeMS: 6240000, Rating: 7.2, ContentRating: "PG", Genres: []string{"Family", "Drama"}}, 1); err != nil {
		t.Fatal(err)
	}
	check := func(where string, c cardBody) {
		t.Helper()
		if c.Overview != "Two sisters chart a comet from a rooftop." || c.Date != "2019-03-08" || c.RuntimeMS == nil ||
			*c.RuntimeMS != 6240000 || c.Rating == nil || *c.Rating != 7.2 || c.ContentRating != "PG" ||
			!slices.Equal(c.Genres, []string{"Drama", "Family"}) {
			t.Errorf("%s: %+v", where, c)
		}
	}
	check("GET /items", b.film(t, "Paper Comets"))
	var changes struct {
		Items []cardBody `json:"items"`
	}
	b.call(t, "GET", "/api/v1/changes?since=0", b.token, "", nil, &changes)
	for _, c := range changes.Items {
		if c.ID == film.ID {
			check("GET /changes", c)
		}
	}
	var d detailBody
	var raw string
	rec := b.call(t, "GET", "/api/v1/items/"+strconv.FormatInt(film.ID, 10), b.token, "", nil, &d)
	raw = rec.Body.String()
	check("GET /items/{id}", d.cardBody)
	if d.OriginalTitle == nil || *d.OriginalTitle != "Papierkometen" {
		t.Errorf("original_title %v", d.OriginalTitle)
	}
	if strings.Contains(b.call(t, "GET", "/api/v1/items?library=1", b.token, "", nil, nil).Body.String(), "original_title") {
		t.Errorf("a card carries original_title")
	}
	if !strings.Contains(raw, `"overview":"Two sisters`) {
		t.Errorf("detail %s", raw)
	}
}
