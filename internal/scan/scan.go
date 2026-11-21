// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/probe"
)

// missingFor is how long a missing file is kept, shown as unavailable,
// before its row goes (docs/03-data-model.md, section 4).
const missingFor = 30 * 24 * time.Hour

// Scanner is the one scanner of the server. It scans one root at a time,
// writes what it found, and then has the files that need it probed.
type Scanner struct {
	db     *db.DB
	prober *probe.Prober // nil: nothing is probed
	log    *slog.Logger
	now    func() time.Time

	// Built, when set, is called after a library has been scanned. It
	// builds the library's items.
	Built func(ctx context.Context, libraryID int64) error

	mu      sync.Mutex
	pending map[int64]bool // library ids to scan; 0 means all
	wake    chan struct{}
	probes  chan struct{}
}

// New makes the scanner. prober may be nil, when ffprobe cannot be found.
func New(d *db.DB, prober *probe.Prober, log *slog.Logger) *Scanner {
	return &Scanner{
		db: d, prober: prober, log: log, now: time.Now,
		pending: map[int64]bool{},
		wake:    make(chan struct{}, 1),
		probes:  make(chan struct{}, 1),
	}
}

// Request asks for a scan of a library, or of every library when
// libraryID is 0. It does not wait. Requests made while a scan runs are
// gathered into the next one.
func (s *Scanner) Request(libraryID int64) {
	s.mu.Lock()
	s.pending[libraryID] = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run scans when asked, and probes in the background, until ctx ends. At
// start it probes what an earlier run left unprobed.
func (s *Scanner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.probeLoop(ctx)
	}()
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		s.mu.Lock()
		asked := s.pending
		s.pending = map[int64]bool{}
		s.mu.Unlock()

		libs, err := s.db.Libraries(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Error("scan: cannot read the libraries", "err", err)
			}
			continue
		}
		for _, lib := range libs {
			if asked[0] || asked[lib.ID] {
				if _, err := s.ScanLibrary(ctx, lib); err != nil && ctx.Err() == nil {
					s.log.Error("scan failed", "library", lib.ID, "err", err)
				}
			}
		}
		s.wakeProbes()
		debug.FreeOSMemory()
	}
}

func (s *Scanner) wakeProbes() {
	select {
	case s.probes <- struct{}{}:
	default:
	}
}

// ScanLibrary scans every root of a library, one after another, and then
// builds its items. It returns what each root's scan found.
func (s *Scanner) ScanLibrary(ctx context.Context, lib model.Library) ([]model.ScanResult, error) {
	var results []model.ScanResult
	for _, root := range lib.Roots {
		r, err := s.scanRoot(ctx, root)
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	if s.Built != nil {
		return results, s.Built(ctx, lib.ID)
	}
	return results, nil
}

// scanRoot is one scan of one root (docs/06-library-and-metadata.md,
// section 2.1).
func (s *Scanner) scanRoot(ctx context.Context, root model.Root) (model.ScanResult, error) {
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
		return r, nil
	}

	// 2 and 3. Walk the tree and compare it with what is known.
	w, err := s.walk(ctx, root.Path, known)
	if err != nil {
		return model.ScanResult{}, err
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
	s.log.Info("scan done", "root", root.Path, "ms", changes.Result.MS,
		"files", len(w.seen)+len(w.new), "added", len(changes.New), "back", len(changes.Back),
		"changed", len(changes.Changed), "moved", len(changes.Moved), "missing", len(changes.Missing))
	if changes.Result.Error != "" {
		s.log.Warn("scan: some folders could not be read", "root", root.Path, "err", changes.Result.Error)
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
}

// walk goes through the tree under rootPath and sorts each video file
// into new, changed, back or unchanged.
func (s *Scanner) walk(ctx context.Context, rootPath string, known map[string]db.KnownFile) (walkResult, error) {
	w := walkResult{seen: map[string]bool{}}
	err := filepath.WalkDir(rootPath, func(p string, d fs.DirEntry, err error) error {
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
		if ignoredFile(d.Name()) || !isVideo(d.Name()) {
			return nil
		}
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
		if !ok {
			w.new = append(w.new, f)
			return nil
		}
		w.seen[rel] = true
		f.ID = k.ID
		switch {
		case k.Size != f.Size || k.MtimeNS != f.MtimeNS || k.Kind != f.Kind:
			w.changed = append(w.changed, f)
		case k.MissingSince != 0:
			w.back = append(w.back, f)
		}
		return nil
	})
	if err != nil {
		return walkResult{}, fmt.Errorf("scan %s: %w", rootPath, err)
	}
	return w, nil
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
		if k.MissingSince == 0 {
			unseen = append(unseen, k)
		}
		if k.Fingerprint != "" {
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
