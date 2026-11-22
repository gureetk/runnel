// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// dirImages keeps images' files in one folder, as the artwork does.
type dirImages string

func (d dirImages) Path(key, kind string, width int) string {
	return filepath.Join(string(d), key+"_"+strconv.Itoa(width)+map[bool]string{true: ".png", false: ".jpg"}[kind == "logo"])
}

func TestImages(t *testing.T) {
	b := newBrowse(t)
	dir := dirImages(t.TempDir())
	b.Server.images = dir
	film := b.film(t, "Paper Comets")
	key := "0e9a3f6d4c7e8a2b9d0f1e3c5a7b5b1c"
	for _, w := range []int{342, 500} {
		os.WriteFile(dir.Path(key, "poster", w), []byte("poster at "+strconv.Itoa(w)), 0o644)
	}
	set := db.ImageSet{Key: key, Widths: []int{342, 500}, Aspect: 0.667, Color: "#22313f"}
	if _, err := b.db.SetLocalImage(context.Background(), film.ID, "poster", set, db.LocalSource{Root: 1, Path: "Paper Comets (2019)/poster.jpg"}); err != nil {
		t.Fatal(err)
	}

	// The card carries it, and its sequence value moved.
	c := b.film(t, "Paper Comets")
	p, ok := c.Images["poster"]
	if !ok || p.Key != key || len(p.W) != 2 || *p.Aspect != 0.667 || *p.Color != "#22313f" || c.Seq <= film.Seq {
		t.Fatalf("card %+v, images %+v", c, c.Images)
	}
	if other := b.film(t, "The Salt Road"); other.Images != nil {
		t.Fatalf("a film without images: %+v", other.Images)
	}

	// The width asked for, or the nearest above it, or the widest; no
	// token needed.
	for w, want := range map[string]string{"342": "poster at 342", "100": "poster at 342", "400": "poster at 500", "9999": "poster at 500"} {
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/img/"+key+"/"+w, nil))
		if rec.Code != 200 || rec.Body.String() != want || rec.Header().Get("Content-Type") != "image/jpeg" ||
			rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("width %s: %d %q %v", w, rec.Code, rec.Body.String(), rec.Header())
		}
	}
	for _, target := range []string{
		"/api/v1/img/" + key + "/0", "/api/v1/img/" + key + "/abc", "/api/v1/img/1f9a3f6d4c7e8a2b9d0f1e3c5a7b5b1c/342",
		"/api/v1/img/0E9A3F6D4C7E8A2B9D0F1E3C5A7B5B1C/342", "/api/v1/img/../342",
	} {
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != 404 {
			t.Errorf("%s: %d", target, rec.Code)
		}
	}
	// A file gone from the images folder: 404 until a scan makes it again.
	os.Remove(dir.Path(key, "poster", 500))
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/img/"+key+"/500", nil))
	if rec.Code != 404 {
		t.Fatalf("a file that went: %d", rec.Code)
	}
}
