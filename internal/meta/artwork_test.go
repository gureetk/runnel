// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/scan"
	"github.com/gureetk/runnel/internal/tasks"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

type env struct {
	t    *testing.T
	db   *db.DB
	art  *Artwork
	s    *scan.Scanner
	q    *tasks.Queue
	root string
	lib  model.Library
}

func newEnv(t *testing.T, kind string) *env {
	t.Helper()
	ffmpeg, ffprobe := tm.Tools(t)
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	root := filepath.Join(t.TempDir(), "media")
	os.MkdirAll(root, 0o755)
	lib, err := d.CreateLibrary(ctx, "Test", kind, "default", []string{root}, 1)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &env{t: t, db: d, root: root, lib: lib}
	e.art = NewArtwork(d, t.TempDir(), ffmpeg, ffprobe, log)
	e.q = tasks.New(d, tasks.NewGate(), log)
	e.q.Register(e.art.Kind())
	e.s = scan.New(d, log)
	e.s.Built = func(ctx context.Context, id int64, scope db.Scope) error {
		_, err := library.BuildIn(ctx, d, id, scope, 1)
		return err
	}
	e.s.Images = e.art.Sync
	return e
}

// video writes a file the scanner takes for a video; it is not probed.
func (e *env) video(rel string) {
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(rel), 0o644)
}

// image makes a picture of the given size with ffmpeg's test source.
func (e *env) image(rel string, w, h int) {
	p := filepath.Join(e.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	tm.Make(e.t, p, "-f", "lavfi", "-i", "testsrc2=size="+strconv.Itoa(w)+"x"+strconv.Itoa(h)+":rate=1", "-frames:v", "1")
}

func (e *env) scan() {
	e.t.Helper()
	tm.Settle(e.t, e.root)
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if _, err := e.s.ScanLibrary(context.Background(), lib); err != nil {
		e.t.Fatal(err)
	}
	if err := e.q.Drain(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

// images lists the stored images by item title and kind.
func (e *env) images() map[string]db.CardImage {
	e.t.Helper()
	out := map[string]db.CardImage{}
	items, _ := e.db.LibraryItems(context.Background(), e.lib.ID)
	for _, it := range items {
		c, err := e.db.Item(context.Background(), it.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		for kind, im := range c.Images {
			out[c.Title+"/"+kind] = im
		}
	}
	return out
}

func TestFilmImages(t *testing.T) {
	e := newEnv(t, "movies")
	e.video("Paper Comets (2019)/Paper Comets (2019).mkv")
	e.image("Paper Comets (2019)/folder.jpg", 600, 900) // a poster less preferred than poster.png
	e.image("Paper Comets (2019)/poster.png", 1000, 1500)
	e.image("Paper Comets (2019)/fanart.jpg", 1920, 1080)
	e.image("Paper Comets (2019)/Paper Comets (2019)-clearlogo.png", 800, 300)
	e.image("Paper Comets (2019)/extras/poster.jpg", 600, 900) // an extra's: not the film's
	e.image("Paper Comets (2019)/landscape.jpg", 1280, 720)    // a kind not kept
	e.video("Lone Lantern (1999).mkv")
	e.image("Lone Lantern (1999)-poster.webp", 200, 300) // smaller than either width
	e.image("poster.jpg", 600, 900)                      // at the top, nobody's
	e.scan()

	im := e.images()
	poster, backdrop, logo := im["Paper Comets/poster"], im["Paper Comets/backdrop"], im["Paper Comets/logo"]
	if !slices.Equal(poster.W, []int{342, 500}) || poster.Aspect == nil || *poster.Aspect < 0.66 || *poster.Aspect > 0.67 ||
		poster.Color == nil || len(*poster.Color) != 7 || len(poster.Key) != 32 {
		t.Fatalf("poster %+v", poster)
	}
	if !slices.Equal(backdrop.W, []int{1280}) || !slices.Equal(logo.W, []int{500}) {
		t.Fatalf("backdrop %+v, logo %+v", backdrop, logo)
	}
	if lone := im["Lone Lantern/poster"]; !slices.Equal(lone.W, []int{200}) {
		t.Fatalf("a lone film's poster: %+v", lone)
	}
	if len(im) != 4 {
		t.Fatalf("images %v", im)
	}
	for _, c := range []struct {
		im   db.CardImage
		kind string
	}{{poster, "poster"}, {backdrop, "backdrop"}, {logo, "logo"}} {
		for _, w := range c.im.W {
			if _, err := os.Stat(e.art.Path(c.im.Key, c.kind, w)); err != nil {
				t.Errorf("%s at %d: %v", c.kind, w, err)
			}
		}
	}
	if filepath.Ext(e.art.Path(logo.Key, "logo", 500)) != ".png" {
		t.Fatal("a logo is not kept with its transparency")
	}

	// Nothing changed: nothing to do.
	e.scan()
	if n, _ := e.db.CountTasks(context.Background(), "image", "queued", "running", "done", "failed"); n != 0 {
		t.Fatalf("%d image tasks after a scan that found nothing new", n)
	}
	// A new poster has a new key, and the old one's files go. A backdrop
	// that goes takes its files with it. The images folder deleted is made
	// again.
	oldPoster := e.art.Path(poster.Key, "poster", 342)
	e.image("Paper Comets (2019)/poster.png", 1000, 1600)
	os.Remove(filepath.Join(e.root, "Paper Comets (2019)/fanart.jpg"))
	os.RemoveAll(filepath.Dir(e.art.Path(logo.Key, "logo", 500)))
	e.scan()
	im = e.images()
	if im["Paper Comets/poster"].Key == poster.Key || im["Paper Comets/backdrop"].Key != "" {
		t.Fatalf("after the changes: %v", im)
	}
	if _, err := os.Stat(oldPoster); err == nil {
		t.Fatal("the old poster's files stayed")
	}
	if _, err := os.Stat(e.art.Path(backdrop.Key, "backdrop", 1280)); err == nil {
		t.Fatal("a removed backdrop's file stayed")
	}
	if _, err := os.Stat(e.art.Path(logo.Key, "logo", 500)); err != nil {
		t.Fatal("the logo's files were not made again")
	}
}

func TestSeriesImages(t *testing.T) {
	e := newEnv(t, "shows")
	e.video("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv")
	e.video("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E02.mkv")
	e.image("Harbor Lantern (2021)/poster.jpg", 600, 900)
	e.image("Harbor Lantern (2021)/backdrop.jpg", 1280, 720)
	e.image("Harbor Lantern (2021)/Season 01/cover.jpg", 600, 900)
	e.image("Harbor Lantern (2021)/Season 01/backdrop.jpg", 1280, 720) // a season keeps a poster only
	e.image("Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01-thumb.jpg", 640, 360)
	e.image("Harbor Lantern (2021)/Season 01/thumb.jpg", 640, 360) // named for no episode
	e.scan()
	im := e.images()
	if len(im) != 4 || im["Harbor Lantern/poster"].Key == "" || im["Harbor Lantern/backdrop"].Key == "" ||
		im["Season 1/poster"].Key == "" || !slices.Equal(im["Episode 1/thumb"].W, []int{300}) {
		t.Fatalf("images %v", im)
	}
}

// TestSweep: the files of images whose items went are deleted, and those
// of images that stay and of files written in the last hour are not.
func TestSweep(t *testing.T) {
	e := newEnv(t, "movies")
	ctx := context.Background()
	e.video("Paper Comets (2019)/Paper Comets (2019).mkv")
	e.image("Paper Comets (2019)/poster.jpg", 600, 900)
	e.scan()
	other := filepath.Join(t.TempDir(), "other")
	os.MkdirAll(filepath.Join(other, "Lone Lantern (1999)"), 0o755)
	os.WriteFile(filepath.Join(other, "Lone Lantern (1999)", "Lone Lantern (1999).mkv"), []byte("x"), 0o644)
	tm.Make(t, filepath.Join(other, "Lone Lantern (1999)", "poster.jpg"), "-f", "lavfi", "-i", "testsrc2=size=600x800:rate=1", "-frames:v", "1")
	lib, err := e.db.CreateLibrary(ctx, "Other", "movies", "default", []string{other}, 1)
	if err != nil {
		t.Fatal(err)
	}
	tm.Settle(t, other)
	if _, err := e.s.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if err := e.q.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	kept := e.images()["Paper Comets/poster"]
	items, _ := e.db.LibraryItems(ctx, lib.ID)
	gone, err := e.db.Item(ctx, items[0].ID)
	if err != nil || gone.Images["poster"].Key == "" {
		t.Fatalf("%+v %v", gone, err)
	}
	// A file that is no image's, such as one left by a task that stopped.
	stray := strings.Repeat("ab", 16)
	os.MkdirAll(filepath.Dir(e.art.Path(stray, "poster", 342)), 0o755)
	os.WriteFile(e.art.Path(stray, "poster", 342), []byte("x"), 0o644)
	if _, err := e.db.DeleteLibrary(ctx, lib.ID); err != nil {
		t.Fatal(err)
	}

	// Within the hour nothing goes.
	if n, err := e.art.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("swept %d at once: %v", n, err)
	}
	e.art.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if n, err := e.art.Sweep(ctx); err != nil || n != 3 {
		t.Fatalf("swept %d: %v", n, err)
	}
	for _, w := range []int{342, 500} {
		if _, err := os.Stat(e.art.Path(gone.Images["poster"].Key, "poster", w)); err == nil {
			t.Errorf("the removed film's poster at %d stayed", w)
		}
		if _, err := os.Stat(e.art.Path(kept.Key, "poster", w)); err != nil {
			t.Errorf("the poster that stays, at %d: %v", w, err)
		}
	}
	if _, err := os.Stat(e.art.Path(stray, "poster", 342)); err == nil {
		t.Error("a file that is no image's stayed")
	}
}

// A scan of some folders gives their items their images, and leaves the
// images of every other item as they are, unread: it reads only the items
// of the top folders whose images it found.
func TestImagesOfSomeFolders(t *testing.T) {
	e := newEnv(t, "shows")
	e.video("Glass Harbor (2020)/Season 01/Glass Harbor - S01E01.mkv")
	e.image("Glass Harbor (2020)/poster.jpg", 600, 900)
	e.video("Quiet Station (2012)/Season 01/Quiet Station - S01E01.mkv")
	e.image("Quiet Station (2012)/poster.jpg", 640, 960) // another picture: two of one have one key
	e.scan()
	before := e.images()
	if len(before) != 2 {
		t.Fatalf("images %v", before)
	}

	e.image("Glass Harbor (2020)/Season 01/poster.jpg", 680, 1020)
	e.image("Glass Harbor (2020)/Season 01/Glass Harbor - S01E01-thumb.jpg", 640, 360)
	tm.Settle(e.t, e.root)
	lib, _ := e.db.Library(context.Background(), e.lib.ID)
	if err := e.s.ScanFolders(context.Background(), lib, map[int64][]string{lib.Roots[0].ID: {"Glass Harbor (2020)/Season 01"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.q.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := e.images()
	if len(after) != 4 || after["Season 1/poster"].Key == "" || after["Episode 1/thumb"].Key == "" {
		t.Fatalf("after a scan of the season's folder: %v", after)
	}
	for k, im := range before {
		if after[k].Key != im.Key {
			t.Errorf("%s changed: %+v, was %+v", k, after[k], im)
		}
	}
	// The poster of the folder not scanned stays when its file goes: only
	// what a scan looked at is removed.
	os.Remove(filepath.Join(e.root, "Quiet Station (2012)", "poster.jpg"))
	if err := e.s.ScanFolders(context.Background(), lib, map[int64][]string{lib.Roots[0].ID: {"Glass Harbor (2020)"}}); err != nil {
		t.Fatal(err)
	}
	if im := e.images(); im["Quiet Station/poster"].Key == "" || len(im) != 4 {
		t.Fatalf("a folder not scanned lost its image: %v", im)
	}
}

// Two items with one picture, byte for byte, as a placeholder poster
// copied into several folders is: both image tasks make the same key at
// once, and both items get it, with no task failed.
func TestOnePictureForTwoItems(t *testing.T) {
	e := newEnv(t, "shows")
	e.video("Glass Harbor (2020)/Season 01/Glass Harbor - S01E01.mkv")
	e.image("Glass Harbor (2020)/poster.jpg", 600, 900)
	e.video("Quiet Station (2012)/Season 01/Quiet Station - S01E01.mkv")
	e.image("Quiet Station (2012)/poster.jpg", 600, 900)
	e.scan()
	im := e.images()
	if im["Glass Harbor/poster"].Key == "" || im["Glass Harbor/poster"].Key != im["Quiet Station/poster"].Key {
		t.Fatalf("images %v", im)
	}
	if n, _ := e.db.CountTasks(context.Background(), "image", "failed", "queued"); n != 0 {
		t.Fatalf("%d image tasks failed or wait to be tried again", n)
	}
}
