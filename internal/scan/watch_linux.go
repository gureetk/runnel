// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// watchMask is what is asked of inotify for each folder: files and
// folders that come, go, are written or change their times. Reading a
// file, which the server does, reports none of these.
const watchMask = unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO | unix.IN_CLOSE_WRITE |
	unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF |
	unix.IN_ONLYDIR | unix.IN_DONT_FOLLOW | unix.IN_EXCL_UNLINK

// watchSys holds the inotify watches: one for each folder under each
// root. Its maps are guarded by the Watcher's mutex.
type watchSys struct {
	f     *os.File
	fd    int
	byWD  map[int32]dirWatch
	roots map[int64]*rootWatch
}

type dirWatch struct {
	root int64
	rel  string // the folder below its root, "" for the root
}

type rootWatch struct {
	path string
	wds  map[string]int32 // folder below the root -> its watch
}

func newWatchSys() (*watchSys, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, os.NewSyscallError("inotify_init1", err)
	}
	// A non-blocking descriptor in an os.File is read through the
	// runtime's poller, so Close ends a Read under way.
	return &watchSys{f: os.NewFile(uintptr(fd), "inotify"), fd: fd,
		byWD: map[int32]dirWatch{}, roots: map[int64]*rootWatch{}}, nil
}

func (s *watchSys) close() { s.f.Close() }

func isNoSpace(err error) bool { return errors.Is(err, unix.ENOSPC) }

func (s *watchSys) watching(root int64) bool {
	r := s.roots[root]
	if r == nil {
		return false
	}
	_, ok := r.wds[""]
	return ok
}

// addTree watches a folder below a root, or the root with "", and every
// folder under it that a scan walks. A folder that cannot be watched is
// left out, unless it is the root, or the system's limit is reached.
func (s *watchSys) addTree(log *slog.Logger, root int64, rootPath, rel string) error {
	r := s.roots[root]
	if r == nil {
		r = &rootWatch{path: rootPath, wds: map[string]int32{}}
		s.roots[root] = r
	}
	start := rootPath
	if rel != "" {
		start = filepath.Join(rootPath, filepath.FromSlash(rel))
	}
	return filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == rootPath {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if p != rootPath && ignoredDir(d.Name()) {
			return fs.SkipDir
		}
		folder, _ := filepath.Rel(rootPath, p)
		folder = filepath.ToSlash(folder)
		if folder == "." {
			folder = ""
		}
		wd, err := unix.InotifyAddWatch(s.fd, p, watchMask)
		switch {
		case err == nil:
		case errors.Is(err, unix.ENOSPC) || p == rootPath:
			return os.NewSyscallError("inotify_add_watch", err)
		default:
			log.Debug("a folder cannot be watched", "path", p, "err", err)
			return fs.SkipDir
		}
		w := int32(wd)
		// A folder that was renamed keeps its watch: forget its old name.
		if old, ok := s.byWD[w]; ok && old.root == root && old.rel != folder {
			delete(r.wds, old.rel)
		}
		s.byWD[w] = dirWatch{root: root, rel: folder}
		r.wds[folder] = w
		return nil
	})
}

// removeTree stops watching a folder below a root, and everything under
// it; with "" the whole root.
func (s *watchSys) removeTree(root int64, rel string) {
	r := s.roots[root]
	if r == nil {
		return
	}
	for folder, wd := range r.wds {
		if rel == "" || folder == rel || strings.HasPrefix(folder, rel+"/") {
			unix.InotifyRmWatch(s.fd, uint32(wd))
			delete(r.wds, folder)
			delete(s.byWD, wd)
		}
	}
	if rel == "" {
		delete(s.roots, root)
	}
}

// read handles events until the descriptor is closed.
func (w *Watcher) read(ctx context.Context) {
	buf := make([]byte, 64<<10)
	for {
		n, err := w.sys.f.Read(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, os.ErrClosed) {
				w.log.Error("watching for changes stopped; changes are found by the nightly walk", "err", err)
			}
			return
		}
		for off := 0; off+unix.SizeofInotifyEvent <= n; {
			e := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			end := off + unix.SizeofInotifyEvent + int(e.Len)
			if end > n {
				break
			}
			name := strings.TrimRight(string(buf[off+unix.SizeofInotifyEvent:end]), "\x00")
			w.handle(e.Wd, e.Mask, name)
			off = end
		}
	}
}

// handle takes one event: it keeps the watches in step with the folders,
// and notes the folder where something changed.
func (w *Watcher) handle(wd int32, mask uint32, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	s := w.sys
	if mask&unix.IN_Q_OVERFLOW != 0 {
		// Events were lost: watch what may have come, and scan it all.
		w.log.Warn("too many changes at once to follow one by one; every watched folder is scanned")
		for id, r := range s.roots {
			s.addTree(w.log, id, r.path, "")
			w.changedLocked(id, "")
		}
		return
	}
	dw, ok := s.byWD[wd]
	if !ok {
		return
	}
	r := s.roots[dw.root]
	switch {
	case mask&unix.IN_IGNORED != 0:
		// The watch is gone: its folder was removed, or its file system
		// unmounted.
		delete(s.byWD, wd)
		if r != nil && r.wds[dw.rel] == wd {
			delete(r.wds, dw.rel)
		}
		if dw.rel == "" && r != nil {
			s.removeTree(dw.root, "")
			w.pollLocked(rootOf(dw.root, r.path), "the folder is away")
			w.changedLocked(dw.root, "")
		}
		return
	case mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0:
		// A folder below the root went: its parent says so too. The root
		// itself: scan it, which finds it away.
		if dw.rel == "" && r != nil {
			s.removeTree(dw.root, "")
			w.pollLocked(rootOf(dw.root, r.path), "the folder was moved or removed")
			w.changedLocked(dw.root, "")
		}
		return
	case name == "":
		return
	}
	if mask&unix.IN_ISDIR != 0 {
		if ignoredDir(name) {
			return
		}
		child := path.Join(dw.rel, name)
		switch {
		case mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 && r != nil:
			if err := s.addTree(w.log, dw.root, r.path, child); err != nil && isNoSpace(err) {
				s.removeTree(dw.root, "")
				w.pollLocked(rootOf(dw.root, r.path),
					"the system's limit of inotify watches is reached; raising fs.inotify.max_user_watches lets it be watched")
			}
		case mask&(unix.IN_DELETE|unix.IN_MOVED_FROM) != 0:
			s.removeTree(dw.root, child)
		}
	} else if ignoredFile(name) {
		return
	}
	w.changedLocked(dw.root, dw.rel)
}
