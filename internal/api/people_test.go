// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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
	if !strings.Contains(raw, `"credits":{"cast":[{"person_id":`+idStr(person)+`,"name":"Ilse Varga","role":"Edda","episodes":12,`+
		`"image":{"key":"7e8a2b9d0f1e3c5a7b5b1c0e9a3f6d4c","w":[240,420],"ar":0.6666666666666666,"color":null}}],"crew":[]}`) {
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

// fakePhotos stands in for the photos' fetcher: it writes a photo's files
// the first time it is asked, or answers what it was told to.
type fakePhotos struct {
	mu    sync.Mutex
	dir   dirImages
	asked []string
	wait  time.Duration
	none  bool
}

func (f *fakePhotos) Photo(ctx context.Context, key string) (bool, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, key)
	if f.wait > 0 || f.none {
		return false, f.wait
	}
	for _, w := range db.PhotoWidths {
		os.WriteFile(f.dir.Path(key, "photo", w), []byte("photo "+idStr(int64(w))), 0o644)
	}
	return true, 0
}

// TestPersonPhotos: credits and a person carry the photo's image, its
// widths and shape known before it is fetched and its colour once it is;
// GET /img for a person's key asks for the photo and serves it, needs no
// token, answers 404 for a photo the provider cannot give and 503 with
// Retry-After while photos cannot be fetched (docs/04-api.md, sections
// 2.4 and 6).
func TestPersonPhotos(t *testing.T) {
	b := newBrowse(t)
	ctx := context.Background()
	dir := dirImages(t.TempDir())
	photos := &fakePhotos{dir: dir}
	b.Server.images, b.Server.photos = dir, photos
	film := b.film(t, "Paper Comets")
	if err := b.db.ApplyMetadata(ctx, film.ID, db.Metadata{Title: "Paper Comets", SortTitle: "paper comets", Date: "2019-03-08"}, 1); err != nil {
		t.Fatal(err)
	}
	const key = "7e8a2b9d0f1e3c5a7b5b1c0e9a3f6d4c"
	if err := b.db.ApplyCredits(ctx, film.ID, "tmdb", []db.CreditIn{
		{ProviderID: "811", Name: "Ilse Varga", KnownFor: "Acting", PhotoRef: "/ilse.jpg", PhotoKey: key, Kind: "cast", Role: "Ida"},
		{ProviderID: "902", Name: "Tomas Reinholt", KnownFor: "Directing", Kind: "crew", Role: "Director", Department: "Directing"},
	}); err != nil {
		t.Fatal(err)
	}
	raw := b.call(t, "GET", "/api/v1/items/"+idStr(film.ID), b.token, "", nil, nil).Body.String()
	if !strings.Contains(raw, `"role":"Ida","episodes":null,"image":{"key":"`+key+`","w":[240,420],"ar":0.6666666666666666,"color":null}}`) ||
		!strings.Contains(raw, `"role":"Director","department":"Directing"}`) {
		t.Fatalf("credits: %s", raw)
	}
	if len(photos.asked) != 0 {
		t.Fatalf("a detail asked for photos: %v", photos.asked)
	}

	rec := b.call(t, "GET", "/api/v1/img/"+key+"/300", "", "", nil, nil)
	if rec.Code != 200 || rec.Body.String() != "photo 420" || rec.Header().Get("Content-Type") != "image/jpeg" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || len(photos.asked) != 1 {
		t.Fatalf("the photo: %d %q %v %v", rec.Code, rec.Body.String(), rec.Header(), photos.asked)
	}
	if rec := b.call(t, "GET", "/api/v1/img/"+key+"/100", "", "", nil, nil); rec.Body.String() != "photo 240" {
		t.Errorf("the small width: %q", rec.Body.String())
	}

	if err := b.db.SetPhoto(ctx, key, "/ilse.jpg", "#3a2f2a", 5); err != nil {
		t.Fatal(err)
	}
	var p personBody
	var people struct {
		Credits struct {
			Cast []castBody `json:"cast"`
		} `json:"credits"`
	}
	b.call(t, "GET", "/api/v1/items/"+idStr(film.ID), b.token, "", nil, &people)
	b.call(t, "GET", "/api/v1/people/"+idStr(people.Credits.Cast[0].PersonID), b.token, "", nil, &p)
	if p.Image == nil || p.Image.Key != key || p.Image.Color == nil || *p.Image.Color != "#3a2f2a" ||
		len(people.Credits.Cast) != 1 || people.Credits.Cast[0].Image == nil || *people.Credits.Cast[0].Image.Color != "#3a2f2a" {
		t.Errorf("once fetched: %+v %+v", p, people.Credits.Cast)
	}

	other := "0123456789abcdef0123456789abcdef"
	photos.none = true
	if rec := b.call(t, "GET", "/api/v1/img/"+other+"/240", "", "", nil, nil); rec.Code != 404 {
		t.Errorf("no such photo: %d", rec.Code)
	}
	photos.none, photos.wait = false, 61*time.Second+time.Millisecond
	var e errorBody
	if rec := b.call(t, "GET", "/api/v1/img/"+other+"/240", "", "", nil, &e); rec.Code != 503 || e.Error.Code != "busy" ||
		rec.Header().Get("Retry-After") != "62" {
		t.Errorf("while photos wait: %d %+v %v", rec.Code, e, rec.Header())
	}
	if rec := b.call(t, "GET", "/api/v1/img/NOTAKEY/240", "", "", nil, nil); rec.Code != 404 || len(photos.asked) != 4 {
		t.Errorf("a malformed key: %d, asked %v", rec.Code, photos.asked)
	}
}
