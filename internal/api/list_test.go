// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
	"time"
)

func TestMyList(t *testing.T) {
	b := newBrowse(t)
	film := b.film(t, "Paper Comets")
	other := b.film(t, "The Salt Road")
	series := b.series(t)
	list := func(query string) itemsPage {
		t.Helper()
		var page itemsPage
		if rec := b.call(t, "GET", "/api/v1/items?list=1"+query, b.token, "", nil, &page); rec.Code != 200 {
			t.Fatalf("list%s: %d", query, rec.Code)
		}
		return page
	}
	titles := func(p itemsPage) string {
		var out []string
		for _, c := range p.Items {
			out = append(out, c.Title)
		}
		return string(mustJSON(out))
	}
	if p := list(""); len(p.Items) != 0 || *p.Total != 0 {
		t.Fatalf("an empty list %+v", p)
	}

	put := func(id int64) {
		t.Helper()
		if code := b.call(t, "PUT", "/api/v1/items/"+idStr(id)+"/list", b.token, "", nil, nil).Code; code != 204 {
			t.Fatalf("put %d: %d", id, code)
		}
		b.advance(time.Second)
	}
	put(film.ID)
	put(series.ID)
	put(other.ID)
	put(film.ID) // again: it keeps its time

	p := list("&limit=2")
	if titles(p) != `["The Salt Road","Harbor Lantern"]` || *p.Total != 3 || p.Next == nil || p.Items[0].ListedAt == nil {
		t.Fatalf("newest first %s %+v", titles(p), p)
	}
	p2 := list("&limit=2&cursor=" + *p.Next)
	if titles(p2) != `["Paper Comets"]` || p2.Next != nil || p2.Total != nil {
		t.Fatalf("second page %s", titles(p2))
	}
	if p := list("&sort=title"); titles(p) != `["Harbor Lantern","Paper Comets","The Salt Road"]` {
		t.Fatalf("by title %s", titles(p))
	}
	if p := list("&type=series"); titles(p) != `["Harbor Lantern"]` || *p.Total != 1 || p.Items[0].Counts == nil {
		t.Fatalf("series only %s", titles(p))
	}

	// A listed card says when, in every list it is in; others do not.
	var films itemsPage
	b.call(t, "GET", "/api/v1/items?library=1", b.token, "", nil, &films)
	listed := 0
	for _, c := range films.Items {
		if c.ListedAt != nil {
			listed++
		}
	}
	if listed != 2 || b.detail(t, b.token, film.ID).ListedAt == nil {
		t.Fatalf("%d listed films", listed)
	}

	// Taken off, and another user's list apart.
	if code := b.call(t, "DELETE", "/api/v1/items/"+idStr(other.ID)+"/list", b.token, "", nil, nil).Code; code != 204 {
		t.Fatalf("take off: %d", code)
	}
	if code := b.call(t, "DELETE", "/api/v1/items/"+idStr(other.ID)+"/list", b.token, "", nil, nil).Code; code != 204 {
		t.Fatalf("take off again: %d", code)
	}
	if p := list(""); titles(p) != `["Harbor Lantern","Paper Comets"]` {
		t.Fatalf("after taking off %s", titles(p))
	}
	b.addUser(t, "bo", "battery staple", false)
	var bo loginAnswer
	b.call(t, "POST", "/api/v1/auth/login", "", "", login("bo", "battery staple", "d2"), &bo)
	var theirs itemsPage
	b.call(t, "GET", "/api/v1/items?list=1", bo.Token, "", nil, &theirs)
	if len(theirs.Items) != 0 {
		t.Fatalf("another user's list %+v", theirs)
	}

	ep := b.episodes(t)[0]
	for _, c := range []struct {
		method, target string
		want           int
	}{
		{"PUT", "/api/v1/items/" + idStr(ep.ID) + "/list", 400},
		{"PUT", "/api/v1/items/99999/list", 404},
		{"DELETE", "/api/v1/items/abc/list", 404},
		{"GET", "/api/v1/items?list=2", 400},
		{"GET", "/api/v1/items?list=1&library=1", 400},
		{"GET", "/api/v1/items?list=1&type=episode", 400},
		{"GET", "/api/v1/items?list=1&sort=views", 400},
		{"GET", "/api/v1/items?list=1&cursor=junk", 400},
		{"GET", "/api/v1/items?library=1&sort=listed", 400},
	} {
		var e apiError
		if rec := b.call(t, c.method, c.target, b.token, "", nil, &e); rec.Code != c.want {
			t.Errorf("%s %s: %d %+v, want %d", c.method, c.target, rec.Code, e, c.want)
		}
	}
}
