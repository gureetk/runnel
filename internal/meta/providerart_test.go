// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

// imageServer stands in for TMDB's images: it serves pictures made with
// ffmpeg's test source by their size and path, and counts what was asked.
type imageServer struct {
	mu    sync.Mutex
	files map[string]string // "/w500/p.jpg" -> file
	asked []string
}

func (s *imageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.asked = append(s.asked, r.URL.Path)
	f, ok := s.files[r.URL.Path]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, f)
}

func (s *imageServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

// withArtwork gives a fetch environment the artwork, which downloads from
// a stand-in image server, as serve.go wires them.
func (e *fetchEnv) withArtwork(t *testing.T) (*Artwork, *imageServer) {
	ffmpeg, ffprobe := tm.Tools(t)
	art := NewArtwork(e.db, t.TempDir(), ffmpeg, ffprobe, slog.New(slog.NewTextHandler(io.Discard, nil)))
	imgs := &imageServer{files: map[string]string{}}
	srv := httptest.NewServer(imgs)
	t.Cleanup(srv.Close)
	art.SetImageBase(srv.URL + "/")
	art.Queued = e.q.Wake
	e.q.Register(art.Kind())
	e.f.Images = art
	art.Lost = e.f.Again
	e.s.Images = art.Sync
	dir := t.TempDir()
	for name, size := range map[string]string{
		"/w500/p.jpg": "500x750", "/w1280/b.jpg": "1280x720", "/w500/l.png": "500x200",
		"/w500/s1.jpg": "500x750", "/original/e1.jpg": "1920x1080", "/original/e2.jpg": "1920x1080",
	} {
		f := filepath.Join(dir, strings.ReplaceAll(name[1:], "/", "_"))
		tm.Make(t, f, "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=1", "-frames:v", "1")
		imgs.files[name] = f
	}
	return art, imgs
}

// images reads an item's images as its card has them.
func (e *fetchEnv) images(id int64) map[string]db.CardImage {
	e.t.Helper()
	c, err := e.db.Item(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c.Images
}

// TestProviderArtwork: a film's poster, backdrop and logo come from its
// details, downloaded once at TMDB's sizes and scaled to the widths of 06
// §8; a refresh downloads nothing again; a local image comes first, and
// when it goes the provider's takes its place; D53's drop takes the
// provider's images.
func TestProviderArtwork(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "movies")
	art, imgs := e.withArtwork(t)
	film := func(id int, poster, backdrop string) string {
		var m map[string]any
		json.Unmarshal([]byte(movieJSON(id, "Paper Comets", "Two sisters chart a comet.", "2019-03-08")), &m)
		m["poster_path"], m["backdrop_path"] = poster, backdrop
		m["images"] = map[string]any{"logos": []any{
			map[string]any{"file_path": "/l.svg", "iso_639_1": "en", "vote_average": 9.0},
			map[string]any{"file_path": "/l.png", "iso_639_1": "en", "vote_average": 5.0},
			map[string]any{"file_path": "/other.png", "iso_639_1": "fr", "vote_average": 9.0},
		}}
		b, _ := json.Marshal(m)
		return string(b)
	}
	e.page("/movie/101", tmdb.MovieQuery(), film(101, "/p.jpg", "/b.jpg"))
	e.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019).mkv")
	e.scan()

	c := e.item("movie", "Paper Comets")
	got := e.images(c.ID)
	for kind, widths := range map[string][]int{"poster": {342, 500}, "backdrop": {780, 1280}, "logo": {500}} {
		im, ok := got[kind]
		if !ok || !slices.Equal(im.W, widths) || im.Color == nil {
			t.Errorf("%s: %+v, want widths %v and a colour", kind, im, widths)
			continue
		}
		for _, w := range widths {
			if fi, err := os.Stat(art.Path(im.Key, kind, w)); err != nil || fi.Size() == 0 {
				t.Errorf("%s at %d: %v", kind, w, err)
			}
		}
	}
	if got["poster"].Key != providerKey("tmdb", "/p.jpg") {
		t.Errorf("the poster's key is %q, want the provider's path's", got["poster"].Key)
	}
	slices.Sort(imgs.asked)
	if strings.Join(imgs.asked, " ") != "/w1280/b.jpg /w500/l.png /w500/p.jpg" {
		t.Errorf("downloaded %v, want each image once at TMDB's size, the PNG logo in English", imgs.asked)
	}

	// A refresh that asks TMDB again finds the same paths: nothing is
	// downloaded again.
	if err := e.f.Refresh(ctx, c.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if n := imgs.count(); n != 3 {
		t.Errorf("a refresh downloaded %d images", n-3)
	}

	// A local poster comes first; when it goes, the provider's comes back.
	posterFile := filepath.Join(e.root, "Paper Comets (2019) [tmdbid-101]", "poster.jpg")
	tm.Make(t, posterFile, "-f", "lavfi", "-i", "testsrc2=size=600x900:rate=1", "-frames:v", "1")
	tm.Settle(t, e.root)
	e.scan()
	if im := e.images(c.ID)["poster"]; im.Key == providerKey("tmdb", "/p.jpg") || im.Key == "" {
		t.Errorf("with a local poster the card shows %+v", im)
	}
	if err := e.f.Refresh(ctx, c.ID, e.lib.ID); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if im := e.images(c.ID)["poster"]; im.Key == providerKey("tmdb", "/p.jpg") {
		t.Errorf("a refresh put the provider's poster over the local one")
	}
	os.Remove(posterFile)
	e.scan()
	if im := e.images(c.ID)["poster"]; im.Key != providerKey("tmdb", "/p.jpg") {
		t.Errorf("after the local poster went the card shows %+v, want the provider's", im)
	}

	// D53: not refreshed for 180 days, the provider's images go.
	if _, err := e.db.DropStale(ctx, e.f.now().Add(1e9).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if got := e.images(c.ID); len(got) != 0 {
		t.Errorf("after the drop: %+v", got)
	}
}

// TestProviderArtworkOfASeries: a series' poster and backdrop, a season's
// poster and its episodes' thumbs, the thumbs scaled from TMDB's original.
func TestProviderArtworkOfASeries(t *testing.T) {
	e := newFetchEnv(t, "shows")
	e.withArtwork(t)
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 01/Harbor Lantern - S01E01.mkv")
	e.put("Harbor Lantern (2021) [tmdbid-500]/Season 01/Harbor Lantern - S01E02.mkv")
	e.page("/tv/500", tmdb.SeriesQuery(), `{"id":500,"name":"Harbor Lantern","poster_path":"/p.jpg","backdrop_path":"/b.jpg",
		"seasons":[{"season_number":1,"episode_count":2}],"images":{"logos":[]}}`)
	e.page("/tv/500/season/1", tmdb.SeasonQuery(), `{"season_number":1,"name":"Season 1","poster_path":"/s1.jpg",
		"episodes":[{"episode_number":1,"name":"The Quiet Morning","still_path":"/e1.jpg"},
		{"episode_number":2,"name":"Low Tide","still_path":"/e2.jpg"}]}`)

	e.scan()
	s := e.item("series", "Harbor Lantern")
	season := e.item("season", "Season 1")
	ep := e.item("episode", "The Quiet Morning")
	if im := e.images(season.ID)["poster"]; !slices.Equal(im.W, []int{342, 500}) {
		t.Errorf("the season's poster: %+v", im)
	}
	if im := e.images(ep.ID)["thumb"]; !slices.Equal(im.W, []int{440, 780}) || im.Aspect == nil || *im.Aspect < 1.7 {
		t.Errorf("an episode's thumb: %+v, want 440 and 780 scaled from the original", im)
	}
	if len(e.images(s.ID)) != 2 {
		t.Errorf("the series' images: %+v", e.images(s.ID))
	}
}

// TestThumbsWait: thumbs are queued behind every other image, and a
// season's listing moves its own to the front.
func TestThumbsWait(t *testing.T) {
	ctx := context.Background()
	e := newFetchEnv(t, "shows")
	art := NewArtwork(e.db, t.TempDir(), "", "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.put("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	e.put("Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E01.mkv")
	lib, _ := e.db.Library(ctx, e.lib.ID)
	if _, err := e.s.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	s1 := e.item("season", "Season 1")
	e1, e2 := e.item("episode", "Episode 1"), int64(0)
	for _, it := range must(e.db.LibraryItems(ctx, e.lib.ID)) {
		if it.Type == "episode" && it.ID != e1.ID {
			e2 = it.ID
		}
	}
	for _, w := range []struct {
		item       int64
		kind, path string
	}{{e1.ID, "thumb", "/e1.jpg"}, {e2, "thumb", "/e2.jpg"}, {s1.ID, "poster", "/s1.jpg"}} {
		if err := art.Want(ctx, e.lib.ID, w.item, w.kind, w.path); err != nil {
			t.Fatal(err)
		}
	}
	order := func() []string {
		var keys []string
		all, _ := e.db.Tasks(ctx)
		slices.SortFunc(all, func(a, b db.TaskRow) int {
			if a.RunAfter != b.RunAfter {
				return int(a.RunAfter - b.RunAfter)
			}
			return int(a.ID - b.ID)
		})
		for _, tk := range all {
			if tk.Kind == "image" {
				keys = append(keys, strings.TrimPrefix(tk.Key, "tmdb:"))
			}
		}
		return keys
	}
	ep := func(id int64) string { return strings.TrimPrefix(db.ItemKey(id), "item:") }
	if got, want := order(), []string{ep(s1.ID) + ":poster", ep(e1.ID) + ":thumb", ep(e2) + ":thumb"}; !slices.Equal(got, want) {
		t.Errorf("queued %v, want the poster first", got)
	}
	art.Hurry(ctx, s1.ID)
	if got, want := order(), []string{ep(e1.ID) + ":thumb", ep(s1.ID) + ":poster", ep(e2) + ":thumb"}; !slices.Equal(got, want) {
		t.Errorf("after season 1 was opened: %v, want its thumb first", got)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
