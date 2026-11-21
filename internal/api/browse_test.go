// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/scan"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

type browse struct {
	*testServer
	token  string
	films  string // root folders
	shows  string
	filmID int64
}

// newBrowse makes a films and a shows library from real files, scanned,
// probed and built, and signs a user in.
func newBrowse(t *testing.T) *browse {
	t.Helper()
	_, ffprobe := tm.Tools(t)
	ts := newTestServer(t)
	b := &browse{testServer: ts, films: filepath.Join(t.TempDir(), "films"), shows: filepath.Join(t.TempDir(), "shows")}
	mk := func(root, rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		tm.Make(t, p, tm.Join(tm.Video("1"), tm.Tone("440", "1"),
			[]string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-metadata:s:a:0", "language=eng", "-metadata", "title=" + rel})...)
	}
	mk(b.films, "Paper Comets (2019)/Paper Comets (2019).mkv")
	mk(b.films, "The Salt Road (2005)/The Salt Road (2005).mkv")
	mk(b.films, "An Echo Garden (2011).mp4")
	os.WriteFile(filepath.Join(b.films, "Broken Lantern (2001).mkv"), []byte("not a video at all"), 0o644)
	mk(b.shows, "Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E02.mkv")
	mk(b.shows, "Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	mk(b.shows, "Harbor Lantern (2021)/Season 02/Harbor Lantern - S02E01.mkv")
	mk(b.shows, "Harbor Lantern (2021)/Specials/Harbor Lantern - S00E01.mkv")

	ctx := context.Background()
	s := scan.New(ts.db, probe.New(ffprobe, 2), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Built = func(ctx context.Context, id int64) error { _, err := library.Build(ctx, ts.db, id, 1); return err }
	for i, root := range []string{b.films, b.shows} {
		lib, err := ts.db.CreateLibrary(ctx, []string{"Films", "Shows"}[i], []string{"movies", "shows"}[i], "default", []string{root}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ScanLibrary(ctx, lib); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ProbePending(ctx); err != nil {
		t.Fatal(err)
	}
	ts.addUser(t, "ada", "correct horse", true)
	var a loginAnswer
	ts.call(t, "POST", "/api/v1/auth/login", "", "", login("ada", "correct horse", "d"), &a)
	b.token = a.Token
	return b
}

type itemsPage struct {
	Items []cardBody `json:"items"`
	Next  *string    `json:"next"`
	Total *int       `json:"total"`
}

func TestLibrariesAndItems(t *testing.T) {
	b := newBrowse(t)
	var libs struct{ Libraries []libraryBody }
	if rec := b.call(t, "GET", "/api/v1/libraries", b.token, "", nil, &libs); rec.Code != 200 || len(libs.Libraries) != 2 {
		t.Fatalf("%d %+v", rec.Code, libs)
	}
	f, s := libs.Libraries[0], libs.Libraries[1]
	if f.Items != 4 || s.Items != 1 || len(f.Roots) != 1 || !f.Roots[0].Available || f.Kind != "movies" || f.Seq == 0 {
		t.Fatalf("libraries %+v", libs.Libraries)
	}

	// The films by title, without their leading article, a page at a time.
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=1&limit=2", b.token, "", nil, &page)
	if page.Total == nil || *page.Total != 4 || len(page.Items) != 2 || page.Next == nil ||
		page.Items[0].Title != "Broken Lantern" || page.Items[1].Title != "An Echo Garden" {
		t.Fatalf("first page %+v", page)
	}
	var page2 itemsPage
	b.call(t, "GET", "/api/v1/items?library=1&limit=2&cursor="+*page.Next, b.token, "", nil, &page2)
	if page2.Total != nil || len(page2.Items) != 2 || page2.Next != nil || page2.Items[0].Title != "Paper Comets" ||
		page2.Items[1].Title != "The Salt Road" || *page2.Items[1].Year != 2005 || page2.Items[1].RootID == nil {
		t.Fatalf("second page %+v", page2)
	}

	// A series' episodes, in order, specials last.
	var series itemsPage
	b.call(t, "GET", "/api/v1/items?library=2", b.token, "", nil, &series)
	if len(series.Items) != 1 || series.Items[0].Type != "series" {
		t.Fatalf("series %+v", series)
	}
	var eps itemsPage
	b.call(t, "GET", "/api/v1/items?series="+strconv.FormatInt(series.Items[0].ID, 10)+"&type=episode", b.token, "", nil, &eps)
	var order []string
	for _, e := range eps.Items {
		order = append(order, strconv.Itoa(*e.Season)+"x"+strconv.Itoa(*e.Episode))
		if e.SeriesTitle != "Harbor Lantern" || e.Title != "Episode "+strconv.Itoa(*e.Episode) {
			t.Fatalf("episode %+v", e)
		}
	}
	if got := strconv.Quote(string(mustJSON(order))); got != strconv.Quote(`["1x1","1x2","2x1","0x1"]`) || *eps.Total != 4 {
		t.Fatalf("episode order %v (total %v)", order, eps.Total)
	}

	var e apiError
	for target, want := range map[string]int{
		"/api/v1/items?library=1&played=0":    400,
		"/api/v1/items?library=1&sort=added":  400,
		"/api/v1/items?library=9":             404,
		"/api/v1/items":                       400,
		"/api/v1/items?library=1&type=season": 400,
		"/api/v1/items?series=1":              404, // a film, not a series
		"/api/v1/items?library=1&cursor=junk": 400,
		"/api/v1/items?library=1&limit=1001":  400,
		"/api/v1/items/99999":                 404,
		"/api/v1/items/abc":                   404,
	} {
		e = apiError{}
		if rec := b.call(t, "GET", target, b.token, "", nil, &e); rec.Code != want {
			t.Errorf("%s: %d %+v, want %d", target, rec.Code, e, want)
		}
	}
	if rec := b.call(t, "GET", "/api/v1/items?library=1", "", "", nil, nil); rec.Code != 401 {
		t.Fatalf("without a token: %d", rec.Code)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func (b *browse) film(t *testing.T, title string) cardBody {
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=1", b.token, "", nil, &page)
	for _, c := range page.Items {
		if c.Title == title {
			return c
		}
	}
	t.Fatalf("no film %q", title)
	return cardBody{}
}

func TestItemDetail(t *testing.T) {
	b := newBrowse(t)
	var d struct {
		cardBody
		Files *[]fileBody `json:"files"`
	}
	b.call(t, "GET", "/api/v1/items/"+strconv.FormatInt(b.film(t, "Paper Comets").ID, 10), b.token, "", nil, &d)
	if d.Files == nil || len(*d.Files) != 1 {
		t.Fatalf("detail %+v", d)
	}
	f := (*d.Files)[0]
	if f.Container != "matroska" || !f.Available || f.Size <= 0 || f.DurationMS == nil || f.Video == nil ||
		f.Video.Codec != "h264" || f.Video.Width != 320 || f.Video.FPS != 25 || f.Video.BitDepth != 8 ||
		len(f.Audio) != 1 || f.Audio[0].Lang == nil || *f.Audio[0].Lang != "eng" || f.Part != 0 || f.Label != nil {
		t.Fatalf("file %+v video %+v audio %+v", f, f.Video, f.Audio)
	}
	// A file ffprobe could not read is not offered: the film shows no file.
	b.call(t, "GET", "/api/v1/items/"+strconv.FormatInt(b.film(t, "Broken Lantern").ID, 10), b.token, "", nil, &d)
	if d.Files == nil || len(*d.Files) != 0 {
		t.Fatalf("an unreadable file is listed: %+v", d.Files)
	}
	// A series has no files of its own.
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?library=2", b.token, "", nil, &page)
	var raw map[string]any
	b.call(t, "GET", "/api/v1/items/"+strconv.FormatInt(page.Items[0].ID, 10), b.token, "", nil, &raw)
	if _, ok := raw["files"]; ok || raw["type"] != "series" {
		t.Fatalf("series detail %v", raw)
	}
}

// fileOf finds the first file of a film through its detail.
func (b *browse) fileOf(t *testing.T, title string) fileBody {
	var d struct {
		Files []fileBody `json:"files"`
	}
	b.call(t, "GET", "/api/v1/items/"+strconv.FormatInt(b.film(t, title).ID, 10), b.token, "", nil, &d)
	if len(d.Files) == 0 {
		t.Fatalf("%s has no file", title)
	}
	return d.Files[0]
}

func TestRaw(t *testing.T) {
	b := newBrowse(t)
	f := b.fileOf(t, "Paper Comets")
	path := filepath.Join(b.films, "Paper Comets (2019)/Paper Comets (2019).mkv")
	want, _ := os.ReadFile(path)
	fp, _ := scan.Fingerprint(path)
	url := "/api/v1/files/" + strconv.FormatInt(f.ID, 10) + "/raw"

	get := func(headers map[string]string, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, req)
		return rec
	}
	rec := get(nil, url+"?t="+b.token)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want) || rec.Header().Get("Content-Type") != "video/x-matroska" ||
		rec.Header().Get("Accept-Ranges") != "bytes" || rec.Header().Get("ETag") != `"`+fp+`"` {
		t.Fatalf("whole file: %d, %d bytes, headers %v", rec.Code, rec.Body.Len(), rec.Header())
	}
	rec = get(map[string]string{"Authorization": "Bearer " + b.token, "Range": "bytes=100-199"}, url)
	if rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), want[100:200]) {
		t.Fatalf("a range: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	rec = get(map[string]string{"Authorization": "Bearer " + b.token, "If-None-Match": `"` + fp + `"`}, url)
	if rec.Code != 304 {
		t.Fatalf("If-None-Match with the fingerprint: %d", rec.Code)
	}
	if rec := get(nil, url); rec.Code != 401 {
		t.Fatalf("without a token: %d", rec.Code)
	}
	if rec := get(nil, "/api/v1/files/99999/raw?t="+b.token); rec.Code != 404 {
		t.Fatalf("an unknown file: %d", rec.Code)
	}

	// Missing: 503, without touching the disk.
	os.Rename(path, path+".away")
	if rec := get(nil, url+"?t="+b.token); rec.Code != 503 {
		t.Fatalf("a file gone but not yet marked missing: %d", rec.Code)
	}
	ctx := context.Background()
	s := scan.New(b.db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lib, _ := b.db.Library(ctx, 1)
	s.ScanLibrary(ctx, lib)
	os.Rename(path+".away", path) // back on disk, but still marked missing until the next scan
	var e apiError
	rec = get(nil, url+"?t="+b.token)
	json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != 503 || e.Error.Code != "unavailable" {
		t.Fatalf("a file marked missing: %d %+v", rec.Code, e)
	}
}

func TestGzip(t *testing.T) {
	b := newBrowse(t)
	req := httptest.NewRequest("GET", "/api/v1/items?library=1", nil)
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var page itemsPage
	if err := json.NewDecoder(zr).Decode(&page); err != nil || len(page.Items) != 4 {
		t.Fatalf("%v %+v", err, page)
	}
	// Media is never compressed.
	f := b.fileOf(t, "Paper Comets")
	req = httptest.NewRequest("GET", "/api/v1/files/"+strconv.FormatInt(f.ID, 10)+"/raw?t="+b.token, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("raw with gzip asked for: %d %v", rec.Code, rec.Header())
	}
}

// TestStreamOutlivesTheJSONDeadline: on one kept-alive connection, a JSON
// call and then a stream that lasts longer than the JSON deadline. The
// stream must not be cut.
func TestStreamOutlivesTheJSONDeadline(t *testing.T) {
	b := newBrowse(t)
	b.deadline = 100 * time.Millisecond
	big := filepath.Join(b.films, "Paper Comets (2019)/Paper Comets (2019).mkv")
	data := make([]byte, 16<<20)
	rand.Read(data)
	os.WriteFile(big, data, 0o644) // a large file at the same path; served as it is now
	f := b.fileOf(t, "Paper Comets")

	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	client := srv.Client()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/items?library=1", nil)
	req.Header.Set("Authorization", "Bearer "+b.token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// The file changed on disk after its scan, which the server does not
	// know yet; it serves what is there.
	resp, err = client.Get(srv.URL + "/api/v1/files/" + strconv.FormatInt(f.ID, 10) + "/raw?t=" + b.token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	n := int64(0)
	buf := make([]byte, 256<<10)
	for {
		k, err := resp.Body.Read(buf)
		n += int64(k)
		if err != nil {
			break
		}
		time.Sleep(5 * time.Millisecond) // read slowly: about 50 MB/s
	}
	if n != int64(len(data)) || time.Since(start) < 2*b.deadline {
		t.Fatalf("read %d of %d bytes in %v", n, len(data), time.Since(start))
	}
}
