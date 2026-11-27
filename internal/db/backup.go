// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// backupsKept is how many copies backup/ keeps: the newest, made before
// the migrations that rebuilt tables (docs/03-data-model.md, section 7).
const backupsKept = 3

// freeSpace is the room left for an unprivileged user on the file system
// that holds dir. Tests replace it.
var freeSpace = func(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// backup copies the database, at schema version from, into backup/ beside
// it before the migration named before runs, and returns the copy's path.
//
// The copy is VACUUM INTO, which writes a consistent snapshot of the
// database into a new file. It is written under a name ending in
// ".partial" and renamed once it is complete and on disk, so that a
// copy cut short by a crash is never taken for a backup. Without room for
// the copy and the migration's rebuilt tables, about twice the database,
// it refuses, and nothing is changed. Only the newest backupsKept copies
// are kept.
func (d *DB) backup(ctx context.Context, from int, before string) (string, error) {
	dir := filepath.Join(filepath.Dir(d.path), "backup")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("db: make %s for the copy of the database before migration %s: %w", dir, before, err)
	}
	var size int64
	for _, p := range []string{d.path, d.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			size += fi.Size()
		}
	}
	need := uint64(2 * size)
	free, err := freeSpace(dir)
	if err != nil {
		return "", fmt.Errorf("db: how much room %s has: %w", dir, err)
	}
	if free < need {
		return "", fmt.Errorf("db: migration %s rebuilds tables, and the database is copied into %s first, "+
			"which needs about %d MB free there and has %d MB; free some room and start again. Nothing has been changed",
			before, dir, (need+1<<20-1)>>20, free>>20)
	}

	name := fmt.Sprintf("runnel-schema%d-%s.db", from, time.Now().UTC().Format("20060102-150405"))
	final := filepath.Join(dir, name)
	partial := final + ".partial"
	os.Remove(partial)
	if _, err := d.w.ExecContext(ctx, `VACUUM INTO ?`, partial); err != nil {
		os.Remove(partial)
		return "", fmt.Errorf("db: copy the database into %s before migration %s: %w; nothing has been changed", dir, before, err)
	}
	// The copy holds what the database does, password hashes and the
	// metadata key among it: readable by the server's own user only.
	if err := os.Chmod(partial, 0o600); err != nil {
		os.Remove(partial)
		return "", fmt.Errorf("db: copy the database into %s: %w", dir, err)
	}
	if err := syncFile(partial); err != nil {
		os.Remove(partial)
		return "", fmt.Errorf("db: copy the database into %s: %w", dir, err)
	}
	if err := os.Rename(partial, final); err != nil {
		os.Remove(partial)
		return "", fmt.Errorf("db: copy the database into %s: %w", dir, err)
	}
	if err := syncFile(dir); err != nil {
		return "", fmt.Errorf("db: copy the database into %s: %w", dir, err)
	}
	pruneBackups(dir)
	return final, nil
}

func syncFile(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// pruneBackups deletes what a copy cut short left, and every copy but the
// newest backupsKept. Names carry the time they were made, so they sort in
// that order within one schema version; across versions the time decides.
func pruneBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type copyOf struct {
		name string
		at   time.Time
	}
	var copies []copyOf
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "runnel-schema") {
			continue
		}
		if strings.HasSuffix(n, ".partial") {
			os.Remove(filepath.Join(dir, n))
			continue
		}
		if !strings.HasSuffix(n, ".db") {
			continue
		}
		_, stamp, ok := strings.Cut(strings.TrimSuffix(n, ".db"), "-")
		if !ok {
			continue
		}
		_, stamp, _ = strings.Cut(stamp, "-")
		at, err := time.Parse("20060102-150405", stamp)
		if err != nil {
			continue
		}
		copies = append(copies, copyOf{n, at})
	}
	sort.Slice(copies, func(i, j int) bool {
		if !copies[i].at.Equal(copies[j].at) {
			return copies[i].at.After(copies[j].at)
		}
		return copies[i].name > copies[j].name
	})
	for _, c := range copies[min(len(copies), backupsKept):] {
		os.Remove(filepath.Join(dir, c.name))
	}
}
