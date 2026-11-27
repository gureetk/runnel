// SPDX-License-Identifier: GPL-3.0-or-later

// Package meta brings what is not in the files themselves: artwork, and
// from stage 4 matching and metadata (docs/06-library-and-metadata.md,
// sections 6 to 8).
package meta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/parse"
	"github.com/gureetk/runnel/internal/scan"
	"github.com/gureetk/runnel/internal/tasks"
)

// Widths are the widths kept of each kind of image
// (docs/06-library-and-metadata.md, section 8).
var Widths = map[string][]int{"poster": {342, 500}, "backdrop": {1280}, "thumb": {300}, "logo": {500}}

// kindsFor are the kinds of local image each type of item takes.
var kindsFor = map[string][]string{
	"movie": {"poster", "backdrop", "logo"}, "series": {"poster", "backdrop", "logo"},
	"season": {"poster"}, "episode": {"thumb"},
}

// Artwork makes items' images from the image files in their folders: it
// finds them after a scan, and scales each once with an ffmpeg child.
type Artwork struct {
	db      *db.DB
	dir     string // images/ in the data directory
	ffmpeg  string // "" when it cannot be found: nothing is scaled
	ffprobe string
	log     *slog.Logger
	now     func() time.Time
	// Queued is called after image work was queued.
	Queued func()
}

// NewArtwork keeps images under dataDir/images, made by the ffmpeg and
// ffprobe at the paths given.
func NewArtwork(d *db.DB, dataDir, ffmpeg, ffprobe string, log *slog.Logger) *Artwork {
	return &Artwork{db: d, dir: filepath.Join(dataDir, "images"), ffmpeg: ffmpeg, ffprobe: ffprobe, log: log, now: time.Now}
}

// Path is where an image's file of one width is kept.
func (a *Artwork) Path(key, kind string, width int) string {
	return filepath.Join(a.dir, key[:2], key+"_"+strconv.Itoa(width)+extension(kind))
}

// extension is the format an image is kept in: PNG for a logo, which
// needs its transparency, JPEG for the rest.
func extension(kind string) string {
	if kind == "logo" {
		return ".png"
	}
	return ".jpg"
}

// slot is an item's image of one kind.
type slot struct {
	item int64
	kind string
}

// owner is an item whose images a folder holds, with the base names its
// images' names may begin with: "" for a name alone.
type owner struct {
	item  int64
	typ   string
	bases []string
}

// task is what an image task is about.
type task struct {
	Item int64  `json:"item"`
	Kind string `json:"kind"`
	Root int64  `json:"root"`
	Path string `json:"path"` // below the root
}

// Sync gives a library's items the local images that a scan of one of
// its roots found (docs/06-library-and-metadata.md, section 4.8): it
// queues a task for each image that is new or changed, and removes the
// images whose files went. Only images whose paths the scan looked at
// are removed.
func (a *Artwork) Sync(ctx context.Context, libraryID, rootID int64, inScope func(rel string) bool, found []db.LocalSource) error {
	want, err := a.match(ctx, libraryID, rootID, found)
	if err != nil {
		return err
	}
	have, err := a.db.LocalImages(ctx, libraryID)
	if err != nil {
		return err
	}
	kept := map[slot]bool{}
	var gone []int64
	for _, im := range have {
		s := slot{im.ItemID, im.Kind}
		w, wanted := want[s]
		switch {
		case wanted && w == im.Source && a.filesThere(db.ImageSet{Key: im.Key, Widths: im.Widths}, im.Kind):
			// Unchanged, and its files are there: a deleted images folder
			// is made again by the next scan.
			kept[s] = true
		case !wanted && im.Source.Root == rootID && inScope(im.Source.Path):
			gone = append(gone, im.ID)
		}
	}
	queued := false
	for s, src := range want {
		if kept[s] {
			continue
		}
		payload, _ := json.Marshal(task{Item: s.item, Kind: s.kind, Root: src.Root, Path: src.Path})
		key := "image:" + strconv.FormatInt(s.item, 10) + ":" + s.kind
		if err := a.db.AddTask(ctx, "image", key, libraryID, string(payload), a.now().UnixMilli()); err != nil {
			return err
		}
		queued = true
	}
	if len(gone) > 0 {
		keys, err := a.db.RemoveImages(ctx, gone)
		if err != nil {
			return err
		}
		a.removeFiles(keys)
	}
	if queued && a.Queued != nil {
		a.Queued()
	}
	return nil
}

// match finds, for each item of the library, the image file of each kind
// it takes: in a film's or a series' folder, by the names of section 4.8
// alone or after its video's base name and a dash; in a season's folder,
// its poster; beside an episode, the episode's base name and "-thumb".
// Where a folder holds several of one kind, the most preferred name is
// taken.
//
// Only the items of the top folders that hold the images found are read:
// an image belongs to an item of its own top folder, whose key begins
// with it. An image at the top of the root, beside a lone film, reads the
// whole root; a scan finds one only when it walks the whole root.
func (a *Artwork) match(ctx context.Context, libraryID, rootID int64, found []db.LocalSource) (map[slot]db.LocalSource, error) {
	scope := db.Scope{rootID: {}}
	for _, src := range found {
		if !strings.Contains(src.Path, "/") {
			scope.Whole(rootID)
			break
		}
		scope.Add(rootID, db.Top(src.Path))
	}
	items, err := a.db.LibraryItemsIn(ctx, libraryID, scope)
	if err != nil {
		return nil, err
	}
	files, err := a.db.LibraryFilesIn(ctx, libraryID, scope)
	if err != nil {
		return nil, err
	}
	relOf := map[int64]string{}
	for _, f := range files {
		relOf[f.ID] = f.RelPath
	}
	owners := map[string][]owner{}  // folder -> the items whose images it holds
	seasons := map[[2]int64]int64{} // series id, season -> season item
	seriesKey := map[int64]string{}
	for _, it := range items {
		switch it.Type {
		case "series":
			if it.RootID == rootID {
				seriesKey[it.ID] = it.PathKey
				owners[it.PathKey] = append(owners[it.PathKey], owner{it.ID, "series", []string{""}})
			}
		case "season":
			seasons[[2]int64{it.SeriesID, int64(it.Season)}] = it.ID
		}
	}
	for _, it := range items {
		switch it.Type {
		case "movie":
			if it.RootID != rootID {
				continue
			}
			var bases []string
			for _, f := range it.FileIDs {
				bases = append(bases, parse.Base(relOf[f]))
			}
			if dir := path.Dir(it.PathKey); it.PathKey == relOf[firstOf(it.FileIDs)] && dir == "." {
				owners[""] = append(owners[""], owner{it.ID, "movie", bases}) // a lone file: its base name, then a dash
			} else {
				owners[it.PathKey] = append(owners[it.PathKey], owner{it.ID, "movie", append([]string{""}, bases...)})
			}
		case "episode":
			if _, ok := seriesKey[it.SeriesID]; !ok {
				continue
			}
			for _, f := range it.FileIDs {
				rel := relOf[f]
				dir := path.Dir(rel)
				if dir == "." {
					dir = ""
				}
				owners[dir] = append(owners[dir], owner{it.ID, "episode", []string{parse.Base(rel)}})
			}
		}
	}
	// Season folders: a folder of a series whose name is a season's.
	for series, key := range seriesKey {
		for _, src := range found {
			dir := path.Dir(src.Path)
			if path.Dir(dir) != key {
				continue
			}
			if n, ok := parse.SeasonFolder(path.Base(dir)); ok {
				if id, ok := seasons[[2]int64{series, int64(n)}]; ok && !hasOwner(owners[dir], id) {
					owners[dir] = append(owners[dir], owner{id, "season", []string{""}})
				}
			}
		}
	}

	best := map[slot]db.LocalSource{}
	rank := map[slot]int{}
	for _, src := range found {
		dir := path.Dir(src.Path)
		if dir == "." {
			dir = ""
		}
		kind, prefix, r, ok := parse.LocalImage(path.Base(src.Path))
		if !ok {
			continue
		}
		for _, o := range owners[dir] {
			if !slices.Contains(kindsFor[o.typ], kind) || !slices.Contains(o.bases, prefix) {
				continue
			}
			if o.typ == "episode" && prefix == "" {
				continue // an episode's image is named for it
			}
			s := slot{o.item, kind}
			if old, ok := rank[s]; !ok || r < old || (r == old && src.Path < best[s].Path) {
				best[s], rank[s] = src, r
			}
		}
	}
	return best, nil
}

func firstOf(ids []int64) int64 {
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

func hasOwner(list []owner, id int64) bool {
	for _, o := range list {
		if o.item == id {
			return true
		}
	}
	return false
}

// Kind is the queue's kind for images.
func (a *Artwork) Kind() tasks.Kind {
	return tasks.Kind{Name: "image", Limit: 2, Run: a.run}
}

// noToolWait is how long image work waits after ffmpeg or ffprobe could
// not be run.
const noToolWait = time.Minute

// run makes one image: it reads the file's fingerprint for its key,
// scales the file to the kind's widths, works out its colour and aspect
// ratio, and stores it as the item's image of that kind. A file that is
// gone leaves nothing to do: the next scan removes its image.
func (a *Artwork) run(ctx context.Context, t db.Task) error {
	var job task
	if err := json.Unmarshal([]byte(t.Payload), &job); err != nil {
		return fmt.Errorf("image task %s: %w", t.Key, err)
	}
	root, err := a.db.RootPath(ctx, job.Root)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	file := filepath.Join(root, filepath.FromSlash(job.Path))
	if !strings.HasPrefix(file, strings.TrimSuffix(root, "/")+"/") {
		return nil
	}
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() {
		return nil
	}
	fp, err := scan.Fingerprint(file)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256([]byte("local:" + fp))
	key := hex.EncodeToString(sum[:16])
	src := db.LocalSource{Root: job.Root, Path: job.Path, Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano()}

	set, ok, err := a.db.ImageOfItem(ctx, job.Item, job.Kind, key)
	if err != nil {
		return err
	}
	if !ok || !a.filesThere(set, job.Kind) {
		if a.ffmpeg == "" || a.ffprobe == "" {
			return &tasks.Pause{Err: errors.New("ffmpeg and ffprobe are needed to scale images"), For: noToolWait}
		}
		if set, err = a.scale(ctx, file, key, job.Kind); err != nil {
			return err
		}
	}
	gone, err := a.db.SetLocalImage(ctx, job.Item, job.Kind, set, src)
	if err != nil {
		return err
	}
	a.removeFiles(gone)
	return nil
}

func (a *Artwork) filesThere(set db.ImageSet, kind string) bool {
	for _, w := range set.Widths {
		if _, err := os.Stat(a.Path(set.Key, kind, w)); err != nil {
			return false
		}
	}
	return len(set.Widths) > 0
}

// scale writes the image at each width of its kind, none wider than the
// file itself, and reads its colour from the smallest.
func (a *Artwork) scale(ctx context.Context, file, key, kind string) (db.ImageSet, error) {
	w, h, err := a.size(ctx, file)
	if err != nil {
		return db.ImageSet{}, err
	}
	set := db.ImageSet{Key: key, Aspect: float64(w) / float64(h)}
	for _, target := range Widths[kind] {
		if width := min(target, w); !slices.Contains(set.Widths, width) {
			set.Widths = append(set.Widths, width)
		}
	}
	slices.Sort(set.Widths)
	if err := os.MkdirAll(filepath.Join(a.dir, key[:2]), 0o755); err != nil {
		return db.ImageSet{}, err
	}
	for _, width := range set.Widths {
		out := a.Path(key, kind, width)
		// Two items may show one picture, whose image tasks then write the
		// same key at once: each writes a file of its own and renames it.
		tmp := fmt.Sprintf("%s.tmp%x%s", out, rand.Uint64(), extension(kind))
		args := []string{"-v", "error", "-nostdin", "-y", "-i", file, "-vf", "scale=" + strconv.Itoa(width) + ":-1", "-frames:v", "1"}
		if kind != "logo" {
			args = append(args, "-q:v", "3")
		}
		if _, err := a.ffrun(ctx, a.ffmpeg, append(args, tmp)...); err != nil {
			os.Remove(tmp)
			return db.ImageSet{}, err
		}
		if err := os.Rename(tmp, out); err != nil {
			return db.ImageSet{}, err
		}
	}
	// The colour: the smallest width, averaged to one pixel.
	rgb, err := a.ffrun(ctx, a.ffmpeg, "-v", "error", "-nostdin", "-i", a.Path(key, kind, set.Widths[0]),
		"-vf", "scale=1:1:flags=area", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1")
	if err == nil && len(rgb) >= 3 {
		set.Color = fmt.Sprintf("#%02x%02x%02x", rgb[0], rgb[1], rgb[2])
	}
	return set, nil
}

// size reads an image's width and height with ffprobe.
func (a *Artwork) size(ctx context.Context, file string) (int, int, error) {
	out, err := a.ffrun(ctx, a.ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0", file)
	if err != nil {
		return 0, 0, err
	}
	w, h, ok := strings.Cut(strings.TrimSpace(string(out)), ",")
	width, err1 := strconv.Atoi(w)
	height, err2 := strconv.Atoi(strings.TrimSpace(h))
	if !ok || err1 != nil || err2 != nil || width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("image %s: ffprobe read no size", file)
	}
	return width, height, nil
}

// ffrun runs ffmpeg or ffprobe for at most a minute, and returns what it
// printed. One that cannot be run pauses image work.
func (a *Artwork) ffrun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case errors.As(err, &exit):
		return nil, fmt.Errorf("%s: %s", filepath.Base(bin), strings.TrimSpace(stderr.String()))
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w", filepath.Base(bin), ctx.Err())
	default:
		return nil, &tasks.Pause{Err: fmt.Errorf("%s cannot be run: %w", bin, err), For: noToolWait}
	}
}

// sweepAge is how old an image's file must be before Sweep may delete it:
// an image task writes its files before the row that names them.
const sweepAge = time.Hour

// Sweep deletes the files of keys that no image and no person's photo has
// any more: those of items that went with a build, a removed folder or a
// removed library (docs/03-data-model.md, section 4). It reads one of
// the 256 folders of images/ at a time, and returns how many files it
// deleted.
func (a *Artwork) Sweep(ctx context.Context) (int, error) {
	dirs, err := os.ReadDir(a.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	old := a.now().Add(-sweepAge)
	deleted := 0
	for _, d := range dirs {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		dir := filepath.Join(a.dir, d.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return deleted, err
		}
		files := map[string][]string{}
		for _, e := range entries {
			name := e.Name()
			if len(name) < 34 || name[32] != '_' || !strings.HasPrefix(name, d.Name()) {
				continue
			}
			if fi, err := e.Info(); err != nil || fi.ModTime().After(old) {
				continue
			}
			files[name[:32]] = append(files[name[:32]], name)
		}
		if len(files) == 0 {
			continue
		}
		used, err := a.db.KeysInUse(ctx, slices.Collect(maps.Keys(files)))
		if err != nil {
			return deleted, err
		}
		for key, names := range files {
			if used[key] {
				continue
			}
			for _, name := range names {
				if os.Remove(filepath.Join(dir, name)) == nil {
					deleted++
				}
			}
		}
	}
	return deleted, nil
}

// removeFiles deletes the files of images that no item has any more.
func (a *Artwork) removeFiles(keys []string) {
	for _, key := range keys {
		matches, _ := filepath.Glob(filepath.Join(a.dir, key[:2], key+"_*"))
		for _, m := range matches {
			os.Remove(m)
		}
	}
}
