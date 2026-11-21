// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/model"
)

const (
	// gatherFor is how long changes are gathered before their folders are
	// scanned (docs/06-library-and-metadata.md, section 2.2).
	gatherFor = 5 * time.Second
	// pollEvery is how often a root that cannot be watched is scanned.
	pollEvery = 15 * time.Minute
)

// Watcher tells the scanner where something changed on disk: it watches
// every folder under each root, gathers the folders where something
// changed for gatherFor, and asks for a scan of them. A root it cannot
// watch, because it is away, on a network file system, or past the
// system's limit of watches, it has scanned every pollEvery instead, and
// says so once (docs/06-library-and-metadata.md, section 2.2).
type Watcher struct {
	log *slog.Logger
	// request asks for a scan of some folders of a root; the scanner's
	// RequestFolders outside tests.
	request func(rootID int64, folders []string)
	gather  time.Duration
	every   time.Duration
	mounts  string // the mount table: /proc/self/mountinfo outside tests

	mu      sync.Mutex
	changed map[int64]map[string]bool // root id -> folders where something changed
	flush   *time.Timer               // ends the gathering under way
	polled  map[int64]polledRoot      // roots scanned every pollEvery
	sys     *watchSys                 // nil where the system cannot watch
	closed  bool                      // Run has ended: nothing more is watched
}

type polledRoot struct {
	path, why string
}

// NewWatcher makes a watcher that asks s to scan.
func NewWatcher(s *Scanner, log *slog.Logger) *Watcher {
	w := &Watcher{log: log, request: s.RequestFolders, gather: gatherFor, every: pollEvery,
		mounts: "/proc/self/mountinfo", changed: map[int64]map[string]bool{}, polled: map[int64]polledRoot{}}
	sys, err := newWatchSys()
	if err != nil {
		log.Warn("folders cannot be watched for changes; every library is scanned every 15 minutes", "err", err)
	}
	w.sys = sys
	return w
}

// Run handles what the system reports, and scans the roots that are not
// watched every pollEvery, until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	done := make(chan struct{})
	if w.sys != nil {
		go func() {
			defer close(done)
			w.read(ctx)
		}()
	} else {
		close(done)
	}
	t := time.NewTicker(w.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			w.closed = true
			if w.sys != nil {
				w.sys.close()
			}
			if w.flush != nil {
				w.flush.Stop()
			}
			w.mu.Unlock()
			<-done
			return
		case <-t.C:
			w.mu.Lock()
			var ids []int64
			for id := range w.polled {
				ids = append(ids, id)
			}
			w.mu.Unlock()
			for _, id := range ids {
				w.request(id, []string{""})
			}
		}
	}
}

// Watch makes sure a root is watched, or scanned every pollEvery when it
// cannot be. The scanner calls it each time it has checked a root, so a
// root that comes back is watched again.
func (w *Watcher) Watch(root model.Root) {
	switch {
	case !root.Available:
		w.poll(root, "the folder is away")
		return
	case w.sys == nil:
		w.poll(root, "this system cannot watch folders")
		return
	}
	if fs := networkFS(w.mounts, root.Path); fs != "" {
		w.poll(root, "it is on a network file system, "+fs+", where changes made elsewhere are not reported")
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.sys.watching(root.ID) {
		return
	}
	if err := w.sys.addTree(w.log, root.ID, root.Path, ""); err != nil {
		w.sys.removeTree(root.ID, "")
		why := "it cannot be watched: " + err.Error()
		if isNoSpace(err) {
			why = "the system's limit of inotify watches is reached; raising fs.inotify.max_user_watches lets it be watched"
		}
		w.pollLocked(root, why)
		return
	}
	if p, ok := w.polled[root.ID]; ok {
		delete(w.polled, root.ID)
		w.log.Info("watching a folder for changes again", "root", p.path)
	}
}

func (w *Watcher) poll(root model.Root, why string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.sys != nil {
		w.sys.removeTree(root.ID, "")
	}
	w.pollLocked(root, why)
}

func rootOf(id int64, path string) model.Root { return model.Root{ID: id, Path: path} }

// pollLocked has the root scanned every pollEvery, and logs why when the
// reason is new.
func (w *Watcher) pollLocked(root model.Root, why string) {
	if p, ok := w.polled[root.ID]; ok && p.why == why {
		return
	}
	w.polled[root.ID] = polledRoot{path: root.Path, why: why}
	w.log.Warn("a folder is not watched for changes; it is scanned every 15 minutes", "root", root.Path, "reason", why)
}

// changedLocked notes a folder where something changed, and starts a
// gathering when none is under way.
func (w *Watcher) changedLocked(rootID int64, folder string) {
	if w.changed[rootID] == nil {
		w.changed[rootID] = map[string]bool{}
	}
	w.changed[rootID][folder] = true
	if w.flush == nil {
		w.flush = time.AfterFunc(w.gather, w.flushNow)
	}
}

// flushNow ends a gathering: it asks for a scan of what changed.
func (w *Watcher) flushNow() {
	w.mu.Lock()
	changed := w.changed
	w.changed, w.flush = map[int64]map[string]bool{}, nil
	w.mu.Unlock()
	for root, folders := range changed {
		list := make([]string, 0, len(folders))
		for f := range folders {
			list = append(list, f)
		}
		w.request(root, list)
	}
}

// networkTypes are the file systems on which changes made by another
// machine are not reported to this one: network file systems, and FUSE
// file systems that reach another machine.
var networkTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "9p": true, "ceph": true,
	"glusterfs": true, "afs": true, "davfs": true, "lustre": true, "gpfs": true,
	"fuse.sshfs": true, "fuse.rclone": true, "fuse.s3fs": true, "fuse.gcsfuse": true, "fuse.davfs2": true,
	"fuse.glusterfs": true, "fuse.ceph-fuse": true, "fuse.smbnetfs": true,
}

// networkFS is the type of the network file system that holds dir, from
// the mount table at mounts, or "" when dir is on another kind.
func networkFS(mounts, dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	f, err := os.Open(mounts)
	if err != nil {
		return ""
	}
	defer f.Close()
	return networkFSIn(f, dir)
}

// networkFSIn reads a mount table in the form of /proc/self/mountinfo and
// finds the mount that holds dir: the one with the longest mount point
// that is dir or one of its parents.
func networkFSIn(r io.Reader, dir string) string {
	best, typ := -1, ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mount-point options [optional…] - type source super-options
		before, after, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		fields, rest := strings.Fields(before), strings.Fields(after)
		if len(fields) < 5 || len(rest) < 1 {
			continue
		}
		mp := unescapeMount(fields[4])
		if (dir == mp || strings.HasPrefix(dir, strings.TrimSuffix(mp, "/")+"/")) && len(mp) > best {
			best, typ = len(mp), rest[0]
		}
	}
	if networkTypes[typ] {
		return typ
	}
	return ""
}

// unescapeMount undoes the octal escapes the mount table writes for
// spaces, tabs, newlines and backslashes.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
