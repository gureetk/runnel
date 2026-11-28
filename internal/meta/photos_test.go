// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/meta/tmdb"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

// photoEnv is a film whose credits name people with photos, fetched as
// serve.go wires it, and the photos fetched on request.
type photoEnv struct {
	*fetchEnv
	art   *Artwork
	imgs  *imageServer
	p     *Photos
	clock time.Time
	film  int64
}

func newPhotoEnv(t *testing.T, people int) *photoEnv {
	t.Helper()
	e := &photoEnv{fetchEnv: newFetchEnv(t, "movies"), clock: time.UnixMilli(1_800_000_000_000)}
	e.art, e.imgs = e.withArtwork(t)
	dir := t.TempDir()
	var cast []any
	for i := 1; i <= people; i++ {
		cast = append(cast, invented(i, map[string]any{"character": fmt.Sprintf("Role %d", i), "order": i - 1}))
		// Most photos are 2:3 at h632; one is square, which is cropped.
		size := "421x632"
		if i == 2 {
			size = "500x500"
		}
		f := filepath.Join(dir, fmt.Sprintf("person%d.jpg", i))
		tm.Make(t, f, "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=1", "-frames:v", "1")
		e.imgs.files[fmt.Sprintf("/h632/person%d.jpg", i)] = f
	}
	var film map[string]any
	json.Unmarshal([]byte(movieJSON(101, "Paper Comets", "Two sisters chart a comet.", "2019-03-08")), &film)
	film["credits"] = map[string]any{"cast": cast, "crew": []any{}}
	b, _ := json.Marshal(film)
	e.page("/movie/101", tmdb.MovieQuery(), string(b))
	e.put("Paper Comets (2019) [tmdbid-101]/Paper Comets (2019).mkv")
	e.scan()
	e.film = e.item("movie", "Paper Comets").ID
	keys := func(context.Context) (string, error) { return e.key, nil }
	e.p = NewPhotos(e.art, keys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.p.now = func() time.Time { return e.clock }
	return e
}

// photoKey is the key of person i's photo.
func photoKey(i int) string { return providerKey("tmdb", fmt.Sprintf("/person%d.jpg", i)) }

// downloads are the photos the image server was asked for.
func (e *photoEnv) downloads() []string {
	e.imgs.mu.Lock()
	defer e.imgs.mu.Unlock()
	var out []string
	for _, p := range e.imgs.asked {
		if strings.HasPrefix(p, "/h632/") {
			out = append(out, p)
		}
	}
	return out
}

func (e *photoEnv) photo(t *testing.T, i int) db.Photo {
	t.Helper()
	ph, err := e.db.PhotoOf(context.Background(), photoKey(i))
	if err != nil {
		t.Fatal(err)
	}
	return ph
}

// TestPhotosOnFirstRequest: matching a library downloads no person's
// photo; the first request for one downloads it at TMDB's h632, crops it
// to 2:3 and keeps it at 240 and 420, with its colour; a second request
// downloads nothing; requests at once for one photo make one download
// (01 §6, criterion 8; 06 §8; 04 §6). After 180 days the photo is
// dropped, and the next request fetches it again (D53).
func TestPhotosOnFirstRequest(t *testing.T) {
	ctx := context.Background()
	e := newPhotoEnv(t, 3)
	if got := e.downloads(); len(got) != 0 {
		t.Fatalf("matching downloaded photos: %v", got)
	}
	credits, err := e.db.Credits(ctx, e.film)
	if err != nil || len(credits) != 3 || credits[0].PhotoKey != photoKey(1) || credits[0].PhotoColor != "" {
		t.Fatalf("credits before any photo: %+v %v", credits, err)
	}

	for _, i := range []int{1, 2} {
		if ok, wait := e.p.Photo(ctx, photoKey(i)); !ok || wait != 0 {
			t.Fatalf("person %d: %v %v", i, ok, wait)
		}
		for _, w := range db.PhotoWidths {
			out, err := exec.Command(e.art.ffprobe, "-v", "error", "-show_entries", "stream=width,height", "-of", "csv=p=0",
				e.art.Path(photoKey(i), "photo", w)).Output()
			if want := fmt.Sprintf("%d,%d", w, w*3/2); err != nil || strings.TrimSpace(string(out)) != want {
				t.Errorf("person %d at %d: %q %v, want %s", i, w, out, err, want)
			}
		}
		if ph := e.photo(t, i); ph.FetchedAt != e.clock.UnixMilli() || ph.TriedAt != 0 {
			t.Errorf("person %d: %+v", i, ph)
		}
	}
	if got := e.downloads(); strings.Join(got, " ") != "/h632/person1.jpg /h632/person2.jpg" {
		t.Errorf("downloads: %v", got)
	}
	credits, _ = e.db.Credits(ctx, e.film)
	if !strings.HasPrefix(credits[0].PhotoColor, "#") || credits[2].PhotoColor != "" {
		t.Errorf("colours: %+v", credits)
	}
	if ok, _ := e.p.Photo(ctx, photoKey(1)); !ok || len(e.downloads()) != 2 {
		t.Errorf("a second request: %v, %v", ok, e.downloads())
	}

	// Ten requests at once for the third photo: one download.
	var wg sync.WaitGroup
	oks := make([]bool, 10)
	for i := range oks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			oks[i], _ = e.p.Photo(ctx, photoKey(3))
		}()
	}
	wg.Wait()
	for i, ok := range oks {
		if !ok {
			t.Errorf("request %d at once: not served", i)
		}
	}
	if got := e.downloads(); len(got) != 3 {
		t.Errorf("ten requests at once made %d downloads: %v", len(got)-2, got)
	}

	// A photo whose files were deleted is fetched again.
	os.Remove(e.art.Path(photoKey(1), "photo", 420))
	if ok, _ := e.p.Photo(ctx, photoKey(1)); !ok || len(e.downloads()) != 4 {
		t.Errorf("a photo whose file went: %v, %v", ok, e.downloads())
	}

	// D53: a photo fetched before the cut-off goes, files and all, though
	// it was served since; its key stays.
	e.clock = e.clock.Add(10 * 24 * time.Hour)
	if ok, _ := e.p.Photo(ctx, photoKey(2)); !ok {
		t.Fatal("person 2 again")
	}
	if n, err := e.p.Drop(ctx, time.UnixMilli(1_800_000_000_000).Add(24*time.Hour)); err != nil || n != 3 {
		t.Fatalf("dropped %d %v", n, err)
	}
	for i := 1; i <= 3; i++ {
		if ph := e.photo(t, i); ph.FetchedAt != 0 {
			t.Errorf("person %d after the drop: %+v", i, ph)
		}
		if _, err := os.Stat(e.art.Path(photoKey(i), "photo", 240)); err == nil {
			t.Errorf("person %d's file stayed", i)
		}
	}
	credits, _ = e.db.Credits(ctx, e.film)
	if credits[0].PhotoKey != photoKey(1) || credits[0].PhotoColor != "" {
		t.Errorf("after the drop: %+v", credits[0])
	}
	before := len(e.downloads())
	if ok, _ := e.p.Photo(ctx, photoKey(1)); !ok || len(e.downloads()) != before+1 {
		t.Errorf("after the drop: %v, %d downloads", ok, len(e.downloads())-before)
	}
	// The sweep keeps a person's photo.
	for _, f := range []string{e.art.Path(photoKey(1), "photo", 240), e.art.Path(photoKey(1), "photo", 420)} {
		old := time.Now().Add(-2 * time.Hour)
		os.Chtimes(f, old, old)
	}
	if n, err := e.art.Sweep(ctx); err != nil || n != 0 || !e.p.there(photoKey(1)) {
		t.Errorf("the sweep: %d %v", n, err)
	}
}

// TestPhotosThatFail: a key that is no person's is fetched for nothing; a
// photo the provider does not have is 404 and not tried again for a day
// (P7); a provider that fails, or answers 429, stops every download for a
// minute or for what it asks, and a request meanwhile is told to come
// back; nothing is fetched without a key (04 §6).
func TestPhotosThatFail(t *testing.T) {
	ctx := context.Background()
	e := newPhotoEnv(t, 5)

	if ok, wait := e.p.Photo(ctx, strings.Repeat("ab", 16)); ok || wait != 0 || len(e.downloads()) != 0 {
		t.Errorf("a key that is no person's: %v %v %v", ok, wait, e.downloads())
	}

	e.imgs.answer("/h632/person1.jpg", http.StatusNotFound, nil)
	if ok, wait := e.p.Photo(ctx, photoKey(1)); ok || wait != 0 {
		t.Errorf("a photo the provider does not have: %v %v", ok, wait)
	}
	if ph := e.photo(t, 1); ph.TriedAt != e.clock.UnixMilli() || ph.FetchedAt != 0 {
		t.Errorf("after the try: %+v", ph)
	}
	e.imgs.answer("/h632/person1.jpg", 0, nil)
	e.clock = e.clock.Add(23 * time.Hour)
	if ok, _ := e.p.Photo(ctx, photoKey(1)); ok || len(e.downloads()) != 1 {
		t.Errorf("within a day: %v, %v", ok, e.downloads())
	}
	e.clock = e.clock.Add(2 * time.Hour)
	if ok, _ := e.p.Photo(ctx, photoKey(1)); !ok || len(e.downloads()) != 2 {
		t.Errorf("a day later: %v, %v", ok, e.downloads())
	}
	if ph := e.photo(t, 1); ph.TriedAt != 0 || ph.FetchedAt == 0 {
		t.Errorf("after it was fetched: %+v", ph)
	}

	// A provider that fails: that photo waits a day, every other a minute.
	e.imgs.answer("/h632/person2.jpg", http.StatusBadGateway, nil)
	if ok, wait := e.p.Photo(ctx, photoKey(2)); ok || wait != 0 {
		t.Errorf("a provider that fails: %v %v", ok, wait)
	}
	if ok, wait := e.p.Photo(ctx, photoKey(3)); ok || wait != time.Minute || len(e.downloads()) != 3 {
		t.Errorf("while downloads wait: %v %v %v", ok, wait, e.downloads())
	}
	e.clock = e.clock.Add(time.Minute)
	if ok, _ := e.p.Photo(ctx, photoKey(3)); !ok || len(e.downloads()) != 4 {
		t.Errorf("a minute later: %v %v", ok, e.downloads())
	}

	// A 429 with a time to wait.
	e.imgs.answer("/h632/person4.jpg", http.StatusTooManyRequests, http.Header{"Retry-After": {"120"}})
	if ok, _ := e.p.Photo(ctx, photoKey(4)); ok {
		t.Error("a 429 served a photo")
	}
	e.imgs.answer("/h632/person4.jpg", 0, nil)
	if ok, wait := e.p.Photo(ctx, photoKey(5)); ok || wait != 2*time.Minute {
		t.Errorf("after a 429: %v %v", ok, wait)
	}

	// Without a key nothing is fetched.
	e.clock = e.clock.Add(3 * time.Minute)
	e.key = ""
	n := len(e.downloads())
	if ok, wait := e.p.Photo(ctx, photoKey(5)); ok || wait != 0 || len(e.downloads()) != n {
		t.Errorf("without a key: %v %v %v", ok, wait, e.downloads())
	}
	if ph := e.photo(t, 5); ph.TriedAt != 0 {
		t.Errorf("without a key, a try was recorded: %+v", ph)
	}
	// A photo already there is still served.
	if ok, _ := e.p.Photo(ctx, photoKey(1)); !ok {
		t.Error("a photo on disk, without a key")
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for h, want := range map[string]time.Duration{
		"": time.Minute, "30": 30 * time.Second, "0": time.Second, "99999": time.Hour, "soon": time.Minute,
		now.Add(90 * time.Second).UTC().Format(http.TimeFormat): 90 * time.Second,
	} {
		if got := retryAfter(h, now); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
	}
}
