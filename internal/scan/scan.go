// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/parse"
	"github.com/gureetk/runnel/internal/probe"
)

// missingFor is how long a missing file is kept, shown as unavailable,
// before its row goes (docs/03-data-model.md, section 4).
const missingFor = 30 * 24 * time.Hour

// youngFor is how recently a file may have been modified and still be
// left alone, as one that is being written. Its folder is scanned again
// once the file is that old (docs/06-library-and-metadata.md, section
// 2.2).
const youngFor = 10 * time.Second

// nightlyAt is the hour of the nightly walk, in the server's local time.
const nightlyAt = 4

// Scanner is the one scanner of the server. It scans one root at a time,
// writes what it found, and queues probes for the files that need one.
type Scanner struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time

	// Built, when set, is called after a library has been scanned. It
	// builds the library's items.
	Built func(ctx context.Context, libraryID int64) error
	// Queued, when set, is called after probes were queued.
	Queued func()
	// Images, when set, is called after a library's items are built, for
	// each root scanned, with the images the scan found in it and which of
	// the root's paths it looked at.
	Images func(ctx context.Context, libraryID int64, rootID int64, inScope func(rel string) bool, found []db.LocalSource) error
	// Checked, when set, is called each time a scan has checked a root,
	// before it walks it, with the root as the check found it: available,
	// or away. The watcher watches it from then on.
	Checked func(root model.Root)
	// Nightly, when set, is called when Run starts and at nightlyAt each
	// night, before the walk: the cleanup that runs with it.
	Nightly func(ctx context.Context)
	// Forgotten, when set, is called for each root that Forget is told
	// of. The watcher stops watching it.
	Forgotten func(rootID int64)

	walking int64 // the library a scan goes through now; guarded by mu

	// found is, for each root scanned since its library's items were
	// built, the images the scan found and what it looked at.
	found map[int64]foundImages

	mu      sync.Mutex
	pending map[int64]bool             // library ids to scan; 0 means all
	folders map[int64]map[string]bool  // root id -> folders to scan, relative to the root; "" is all of it
	later   map[int64]map[string]int64 // root id -> folder -> when to scan it, for files still being written
	wake    chan struct{}
}

// New makes the scanner.
func New(d *db.DB, log *slog.Logger) *Scanner {
	return &Scanner{
		db: d, log: log, now: time.Now,
		pending: map[int64]bool{},
		folders: map[int64]map[string]bool{},
		later:   map[int64]map[string]int64{},
		found:   map[int64]foundImages{},
		wake:    make(chan struct{}, 1),
	}
}

// Request asks for a scan of a library, or of every library when
// libraryID is 0. It does not wait. Requests made while a scan runs are
// gathered into the next one.
func (s *Scanner) Request(libraryID int64) {
	s.mu.Lock()
	s.pending[libraryID] = true
	s.mu.Unlock()
	s.poke()
}

// RequestFolders asks for a scan of some folders of a root, given
// relative to it, with "" for the whole root. It does not wait.
func (s *Scanner) RequestFolders(rootID int64, folders []string) {
	s.mu.Lock()
	if s.folders[rootID] == nil {
		s.folders[rootID] = map[string]bool{}
	}
	for _, f := range folders {
		s.folders[rootID][f] = true
	}
	s.mu.Unlock()
	s.poke()
}

// Forget drops the scans asked for of roots that were taken out of their
// library, or removed with it, and tells Forgotten of each.
func (s *Scanner) Forget(rootIDs []int64) {
	s.mu.Lock()
	for _, id := range rootIDs {
		delete(s.folders, id)
		delete(s.later, id)
	}
	s.mu.Unlock()
	if s.Forgotten != nil {
		for _, id := range rootIDs {
			s.Forgotten(id)
		}
	}
}

func (s *Scanner) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run scans when asked, until ctx ends. At start it queues the probes
// that an earlier run left undone, or that a newer probe version needs,
// and walks every library. It walks them all again each night at
// nightlyAt.
func (s *Scanner) Run(ctx context.Context) {
	if s.Nightly != nil {
		s.Nightly(ctx)
	}
	s.queueProbes(ctx, 0)
	s.Request(0)
	nightly := time.NewTimer(untilNightly(s.now()))
	defer nightly.Stop()
	for {
		wait := time.Hour
		if at, ok := s.nextLater(); ok {
			wait = max(0, time.Duration(at-s.now().UnixMilli())*time.Millisecond)
		}
		soon := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			soon.Stop()
			return
		case <-s.wake:
		case <-soon.C:
		case <-nightly.C:
			s.log.Info("scan: the nightly walk")
			if s.Nightly != nil {
				s.Nightly(ctx)
			}
			s.Request(0)
			nightly.Reset(untilNightly(s.now()))
		}
		soon.Stop()
		if s.runAsked(ctx) {
			debug.FreeOSMemory()
		}
	}
}

// untilNightly is how long it is from now to the next nightlyAt o'clock.
func untilNightly(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), nightlyAt, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next.Sub(now)
}

// nextLater is the earliest time a folder is to be scanned again for
// files that were still being written.
func (s *Scanner) nextLater() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var at int64
	for _, folders := range s.later {
		for _, t := range folders {
			if at == 0 || t < at {
				at = t
			}
		}
	}
	return at, at != 0
}

// runAsked runs the scans asked for, and those of folders whose files
// were being written and are old enough now. It reports whether it ran
// any.
func (s *Scanner) runAsked(ctx context.Context) bool {
	now := s.now().UnixMilli()
	s.mu.Lock()
	for root, folders := range s.later {
		for f, at := range folders {
			if at <= now {
				if s.folders[root] == nil {
					s.folders[root] = map[string]bool{}
				}
				s.folders[root][f] = true
				delete(folders, f)
			}
		}
		if len(folders) == 0 {
			delete(s.later, root)
		}
	}
	asked, folders := s.pending, s.folders
	s.pending, s.folders = map[int64]bool{}, map[int64]map[string]bool{}
	s.mu.Unlock()
	if len(asked) == 0 && len(folders) == 0 {
		return false
	}

	libs, err := s.db.Libraries(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("scan: cannot read the libraries", "err", err)
		}
		return false
	}
	defer s.setWalking(0)
	for _, lib := range libs {
		if asked[0] || asked[lib.ID] {
			s.setWalking(lib.ID)
			if _, err := s.ScanLibrary(ctx, lib); err != nil && ctx.Err() == nil {
				s.log.Error("scan failed", "library", lib.ID, "err", err)
			}
			s.setWalking(0)
			continue
		}
		scanned := false
		for _, root := range lib.Roots {
			f := folders[root.ID]
			if f == nil {
				continue
			}
			var list []string
			if !f[""] {
				list = outermost(f)
			}
			s.setWalking(lib.ID)
			if _, err := s.scanRoot(ctx, root, list); err != nil {
				if ctx.Err() == nil {
					s.log.Error("scan failed", "root", root.Path, "err", err)
				}
				continue
			}
			scanned = true
		}
		if scanned {
			if err := s.built(ctx, lib); err != nil && ctx.Err() == nil {
				s.log.Error("scan: cannot build items", "library", lib.ID, "err", err)
			}
		}
		s.setWalking(0)
	}
	return true
}

// setWalking records which library a scan is going through now, with
// building its items, 0 for none.
func (s *Scanner) setWalking(libraryID int64) {
	s.mu.Lock()
	s.walking = libraryID
	s.mu.Unlock()
}

// Walking reports whether a scan is going through a library's folders
// now, or building its items from what it found (docs/04-api.md, section
// 8.2, scan.state "walking").
func (s *Scanner) Walking(libraryID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.walking != 0 && s.walking == libraryID
}

// outermost lists the folders that lie inside none of the others, in
// order: scanning a folder scans what it holds.
func outermost(folders map[string]bool) []string {
	var out []string
	for f := range folders {
		inside := false
		for g := range folders {
			if g != f && strings.HasPrefix(f, g+"/") {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// queueProbes queues a probe for the files of a root, or of every root
// with 0, that need one, and says so.
func (s *Scanner) queueProbes(ctx context.Context, rootID int64) {
	n, err := s.db.QueueProbes(ctx, rootID, probe.Version, s.now().UnixMilli())
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("scan: cannot queue probes", "err", err)
		}
		return
	}
	if n > 0 && s.Queued != nil {
		s.Queued()
	}
}

type foundImages struct {
	inScope func(rel string) bool
	images  []db.LocalSource
}

// ScanLibrary scans every root of a library, one after another, and then
// builds its items. It returns what each root's scan found.
func (s *Scanner) ScanLibrary(ctx context.Context, lib model.Library) ([]model.ScanResult, error) {
	var results []model.ScanResult
	for _, root := range lib.Roots {
		r, err := s.scanRoot(ctx, root, nil)
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, s.built(ctx, lib)
}

// built builds a library's items, and then hands on the images its roots'
// scans found.
func (s *Scanner) built(ctx context.Context, lib model.Library) error {
	if s.Built != nil {
		if err := s.Built(ctx, lib.ID); err != nil {
			return err
		}
	}
	for _, root := range lib.Roots {
		f, ok := s.found[root.ID]
		if !ok {
			continue
		}
		delete(s.found, root.ID)
		if s.Images != nil {
			if err := s.Images(ctx, lib.ID, root.ID, f.inScope, f.images); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanRoot is one scan of one root (docs/06-library-and-metadata.md,
// section 2.1): of all of it, or only of the given folders below it and
// what they hold.
func (s *Scanner) scanRoot(ctx context.Context, root model.Root, folders []string) (model.ScanResult, error) {
	start := time.Now()
	now := s.now()
	known, err := s.db.KnownFiles(ctx, root.ID)
	if err != nil {
		return model.ScanResult{}, err
	}

	// 1. Check the root. One that cannot be listed, or that lists empty
	// while it held files, is away: nothing under it is marked missing.
	entries, err := os.ReadDir(root.Path)
	present := 0
	for _, k := range known {
		if k.MissingSince == 0 {
			present++
		}
	}
	away := ""
	switch {
	case err != nil:
		away = "the folder cannot be listed: " + err.Error()
	case len(entries) == 0 && present > 0:
		away = "the folder is empty, and held files before"
	}
	if away != "" {
		r := model.ScanResult{At: now.UnixMilli(), MS: time.Since(start).Milliseconds(), Error: away}
		if err := s.db.RootUnavailable(ctx, root.ID, r); err != nil {
			return model.ScanResult{}, err
		}
		s.log.Warn("scan: the folder is away; nothing under it is marked missing", "root", root.Path, "reason", away)
		if s.Checked != nil {
			root.Available = false
			s.Checked(root)
		}
		return r, nil
	}
	if s.Checked != nil {
		root.Available = true
		s.Checked(root)
	}

	// 2 and 3. Walk the tree, or the folders, and compare it with what is
	// known.
	w := s.walkAll(ctx, root.Path, folders, known, now)
	if w.err != nil {
		return model.ScanResult{}, w.err
	}

	// 4. What was not seen is missing, unless it moved: a new path whose
	// fingerprint is that of a missing file, or of one not seen in this
	// walk, is that file at a new path.
	changes := s.matchMoves(root.Path, known, w)

	changes.Result = model.ScanResult{
		At:      now.UnixMilli(),
		Added:   len(changes.New) + len(changes.Back),
		Changed: len(changes.Changed) + len(changes.Moved),
		Removed: len(changes.Missing),
	}
	if len(w.failed) > 0 {
		changes.Result.Error = fmt.Sprintf("%d folders could not be read, the first %s: %v; nothing in them was marked missing",
			len(w.failed), w.failed[0].rel, w.failed[0].err)
	}
	changes.Result.MS = time.Since(start).Milliseconds()

	// 5 and 6. Write it all in one transaction, with what the scan found.
	if err := s.db.ApplyScan(ctx, root.ID, changes, now.UnixMilli(), now.Add(-missingFor).UnixMilli()); err != nil {
		return model.ScanResult{}, err
	}
	inScope := func(rel string) bool { return w.expected(rel) && !underFailed(rel, w.failed) }
	if err := s.db.SyncTracks(ctx, root.ID, inScope, w.byVideo()); err != nil {
		return model.ScanResult{}, err
	}
	for i := range w.images {
		w.images[i].Root = root.ID
	}
	s.found[root.ID] = foundImages{inScope: inScope, images: w.images}
	s.queueProbes(ctx, root.ID)
	attrs := []any{"root", root.Path, "ms", changes.Result.MS,
		"files", len(w.seen) + len(w.new), "added", len(changes.New), "back", len(changes.Back),
		"changed", len(changes.Changed), "moved", len(changes.Moved), "missing", len(changes.Missing)}
	if folders != nil {
		attrs = append(attrs, "folders", len(folders))
	}
	if len(w.young) > 0 {
		attrs = append(attrs, "being_written", w.youngFiles)
	}
	s.log.Info("scan done", attrs...)
	if changes.Result.Error != "" {
		s.log.Warn("scan: some folders could not be read", "root", root.Path, "err", changes.Result.Error)
	}
	// Files still being written: their folders again, once they are old
	// enough.
	if len(w.young) > 0 {
		s.mu.Lock()
		if s.later[root.ID] == nil {
			s.later[root.ID] = map[string]int64{}
		}
		for f, at := range w.young {
			s.later[root.ID][f] = max(s.later[root.ID][f], at)
		}
		s.mu.Unlock()
		s.poke()
	}
	return changes.Result, nil
}

type failedDir struct {
	rel string
	err error
}

type walkResult struct {
	seen    map[string]bool // known paths that are there
	new     []db.SeenFile
	changed []db.SeenFile
	back    []db.SeenFile
	failed  []failedDir
	// under is the folders walked, when not the whole root: only what is
	// known under them is expected to be seen.
	under []string
	// young is the folders that hold files still being written, and when
	// to scan each again.
	young      map[string]int64
	youngFiles int
	// videos and tracks are, for each folder, the videos in it and the
	// files beside them that are their tracks (docs/06-library-and-metadata.md,
	// section 4.7).
	videos map[string][]string // folder -> paths of its videos
	tracks map[string][]db.ExternalTrack
	images []db.LocalSource // image files, outside extras folders
	err    error
}

// walkAll walks the whole root, or each of the folders.
func (s *Scanner) walkAll(ctx context.Context, rootPath string, folders []string, known map[string]db.KnownFile, now time.Time) walkResult {
	w := walkResult{seen: map[string]bool{}, young: map[string]int64{}, under: folders,
		videos: map[string][]string{}, tracks: map[string][]db.ExternalTrack{}}
	if folders == nil {
		w.err = s.walk(ctx, &w, rootPath, "", known, now)
		return w
	}
	for _, f := range folders {
		// A folder that is gone holds nothing now: what was known under it
		// is missing, or moved.
		fi, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(f)))
		if err != nil || !fi.IsDir() {
			continue
		}
		if w.err = s.walk(ctx, &w, rootPath, f, known, now); w.err != nil {
			return w
		}
	}
	return w
}

// byVideo gives each video the track files beside it whose names are its
// own. Two videos with one base name, such as one name with two
// extensions, both have the track.
func (w *walkResult) byVideo() map[string][]db.ExternalTrack {
	out := map[string][]db.ExternalTrack{}
	for dir, tracks := range w.tracks {
		bases := make([]string, 0, len(w.videos[dir]))
		for _, v := range w.videos[dir] {
			bases = append(bases, parse.Base(v))
		}
		for _, t := range tracks {
			name := path.Base(t.RelPath)
			owner := parse.TrackOwner(name, bases)
			if owner == "" {
				continue
			}
			p, _ := parse.ParseTrack(name, owner)
			t.Kind, t.Codec, t.Lang, t.Title = p.Kind, p.Codec, p.Lang, p.Title
			t.Default, t.Forced, t.HearingImpaired = p.Default, p.Forced, p.HearingImpaired
			for _, v := range w.videos[dir] {
				if parse.Base(v) == owner {
					out[v] = append(out[v], t)
				}
			}
		}
	}
	return out
}

// expected reports whether a known file is under what was walked.
func (w *walkResult) expected(rel string) bool {
	if w.under == nil {
		return true
	}
	for _, f := range w.under {
		if strings.HasPrefix(rel, f+"/") {
			return true
		}
	}
	return false
}

// walk goes through the tree under rootPath, or under the folder from
// below it, and sorts each video file into new, changed, back or
// unchanged. A new or changed file modified less than youngFor ago is
// left alone, and its folder noted for a later scan.
func (s *Scanner) walk(ctx context.Context, w *walkResult, rootPath, from string, known map[string]db.KnownFile, now time.Time) error {
	start := rootPath
	if from != "" {
		start = filepath.Join(rootPath, filepath.FromSlash(from))
	}
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, relErr := filepath.Rel(rootPath, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			if p == rootPath {
				return err
			}
			// A folder that cannot be read: what it holds is not marked
			// missing. A file that cannot be read is treated the same.
			w.failed = append(w.failed, failedDir{rel: rel, err: err})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != rootPath && ignoredDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if ignoredFile(d.Name()) {
			return nil
		}
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		if !isVideo(d.Name()) {
			track, image := parse.TrackKind(d.Name()) != "", parse.IsImage(d.Name()) && !isExtra(rel)
			if !track && !image {
				return nil
			}
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			if track {
				w.tracks[dir] = append(w.tracks[dir], db.ExternalTrack{RelPath: rel, Size: info.Size(), MtimeNS: info.ModTime().UnixNano()})
			} else {
				w.images = append(w.images, db.LocalSource{Path: rel, Size: info.Size(), MtimeNS: info.ModTime().UnixNano()})
			}
			return nil
		}
		w.videos[dir] = append(w.videos[dir], rel)
		info, err := d.Info()
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			info, err = os.Stat(p) // a link to a file counts as the file
		}
		if err != nil {
			w.failed = append(w.failed, failedDir{rel: rel, err: err})
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		f := db.SeenFile{RelPath: rel, Kind: "video", Size: info.Size(), MtimeNS: info.ModTime().UnixNano()}
		if isExtra(rel) {
			f.Kind = "extra"
		}
		k, ok := known[rel]
		if ok {
			w.seen[rel] = true
			f.ID = k.ID
		}
		changed := ok && (k.Size != f.Size || k.MtimeNS != f.MtimeNS || k.Kind != f.Kind)
		if (!ok || changed) && young(info.ModTime(), now) {
			w.young[dir] = max(w.young[dir], info.ModTime().Add(youngFor).UnixMilli()+500)
			w.youngFiles++
			return nil
		}
		switch {
		case !ok:
			w.new = append(w.new, f)
		case changed:
			w.changed = append(w.changed, f)
		case k.MissingSince != 0:
			w.back = append(w.back, f)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan %s: %w", start, err)
	}
	return nil
}

// young reports whether a file modified at mtime is still being written:
// modified less than youngFor before now. A time in the future, past a
// little drift between clocks, is not.
func young(mtime, now time.Time) bool {
	return mtime.After(now.Add(-youngFor)) && !mtime.After(now.Add(2*time.Second))
}

// underFailed reports whether rel is a folder that could not be read, or
// lies inside one.
func underFailed(rel string, failed []failedDir) bool {
	for _, f := range failed {
		if rel == f.rel || strings.HasPrefix(rel, f.rel+"/") {
			return true
		}
	}
	return false
}

// matchMoves finds the new paths that are known files at a new place,
// and lists what is missing.
func (s *Scanner) matchMoves(rootPath string, known map[string]db.KnownFile, w walkResult) db.ScanChanges {
	c := db.ScanChanges{Changed: w.changed, Back: w.back}

	var unseen []db.KnownFile
	bySize := map[int64][]db.KnownFile{} // files that may have moved, with a fingerprint
	for _, k := range known {
		if w.seen[k.RelPath] || underFailed(k.RelPath, w.failed) {
			continue
		}
		// A file outside the folders walked was not looked at: it is not
		// missing, and did not move.
		expected := w.expected(k.RelPath)
		if k.MissingSince == 0 && expected {
			unseen = append(unseen, k)
		}
		if k.Fingerprint != "" && (expected || k.MissingSince != 0) {
			bySize[k.Size] = append(bySize[k.Size], k)
		}
	}
	for size := range bySize {
		sort.Slice(bySize[size], func(i, j int) bool { return bySize[size][i].ID < bySize[size][j].ID })
	}

	moved := map[int64]bool{}
	for _, f := range w.new {
		candidates := bySize[f.Size]
		if len(candidates) > 0 {
			fp, err := Fingerprint(filepath.Join(rootPath, filepath.FromSlash(f.RelPath)))
			if err == nil {
				for _, k := range candidates {
					if !moved[k.ID] && k.Fingerprint == fp {
						moved[k.ID] = true
						f.ID = k.ID
						c.Moved = append(c.Moved, f)
						break
					}
				}
			}
		}
		if f.ID == 0 {
			c.New = append(c.New, f)
		}
	}
	for _, k := range unseen {
		if !moved[k.ID] {
			c.Missing = append(c.Missing, k.ID)
		}
	}
	sort.Slice(c.Missing, func(i, j int) bool { return c.Missing[i] < c.Missing[j] })
	return c
}
