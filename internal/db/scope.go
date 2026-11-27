// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Scope is the part of a library that a build reads
// (docs/06-library-and-metadata.md, section 5): for each root, the names of
// the folders and lone files at its top. A root that maps to nil is read
// whole, and a root that is not in the map is not read. A nil Scope is the
// whole library.
//
// A film's or a series' key begins with the top folder of its files, so a
// build of some top folders reads every item their files make or made.
type Scope map[int64][]string

// Top is the name at the top of a path below a root: its first folder, or
// the file itself.
func Top(rel string) string {
	top, _, _ := strings.Cut(rel, "/")
	return top
}

// Add puts a root's top names into the scope. A root already read whole
// stays so.
func (s Scope) Add(root int64, tops ...string) {
	have, ok := s[root]
	if ok && have == nil {
		return
	}
	for _, t := range tops {
		if !slices.Contains(have, t) {
			have = append(have, t)
		}
	}
	if have == nil {
		have = []string{}
	}
	s[root] = have
}

// Whole puts a whole root into the scope.
func (s Scope) Whole(root int64) { s[root] = nil }

// Has reports whether the scope holds a root's top name.
func (s Scope) Has(root int64, top string) bool {
	tops, ok := s[root]
	return ok && (tops == nil || slices.Contains(tops, top))
}

// topOf is the SQL for the top name of a path column.
func topOf(col string) string {
	return "CASE WHEN instr(" + col + ", '/') > 0 THEN substr(" + col + ", 1, instr(" + col + ", '/') - 1) ELSE " + col + " END"
}

// where is the SQL condition that a row with the given root and path
// columns is in the scope, and its two arguments: the roots read whole,
// and the root and top pairs.
func (s Scope) where(root, path string) (string, []any) {
	whole := []int64{}
	pairs := [][2]any{}
	for _, r := range slices.Sorted(maps.Keys(s)) {
		if s[r] == nil {
			whole = append(whole, r)
			continue
		}
		for _, t := range s[r] {
			pairs = append(pairs, [2]any{r, t})
		}
	}
	w, _ := json.Marshal(whole)
	p, _ := json.Marshal(pairs)
	return "(" + root + " IN (SELECT value FROM json_each(?)) OR EXISTS (SELECT 1 FROM json_each(?) p WHERE json_extract(p.value, '$[0]') = " +
		root + " AND json_extract(p.value, '$[1]') = " + topOf(path) + "))", []any{string(w), string(p)}
}

// LibraryFilesIn lists the video files of a library that a scope holds,
// as LibraryFiles does.
func (d *DB) LibraryFilesIn(ctx context.Context, libraryID int64, scope Scope) ([]LibraryFile, error) {
	if scope == nil {
		return d.LibraryFiles(ctx, libraryID)
	}
	cond, args := scope.where("f.root_id", "f.rel_path")
	rows, err := d.r.QueryContext(ctx,
		`SELECT f.id, f.root_id, f.rel_path FROM files f JOIN roots r ON r.id = f.root_id
		 WHERE r.library_id = ? AND f.kind = 'video' AND `+cond+` ORDER BY f.id`, append([]any{libraryID}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("db: read files: %w", err)
	}
	defer rows.Close()
	var out []LibraryFile
	for rows.Next() {
		var f LibraryFile
		if err := rows.Scan(&f.ID, &f.RootID, &f.RelPath); err != nil {
			return nil, fmt.Errorf("db: read files: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ItemsPlaying gives the roots and top names of the films and series whose
// items play the given files now: an episode's series' among them. A
// file that moved from another top folder is played by an item there
// until the next build.
func (d *DB) ItemsPlaying(ctx context.Context, fileIDs []int64) (Scope, error) {
	out := Scope{}
	if len(fileIDs) == 0 {
		return out, nil
	}
	list, err := json.Marshal(fileIDs)
	if err != nil {
		return nil, err
	}
	rows, err := d.r.QueryContext(ctx, `SELECT DISTINCT o.root_id, o.path_key FROM item_files l
		JOIN items i ON i.id = l.item_id
		JOIN items o ON o.id = COALESCE(i.series_id, i.id)
		WHERE l.file_id IN (SELECT value FROM json_each(?)) AND o.path_key IS NOT NULL AND o.root_id IS NOT NULL`, string(list))
	if err != nil {
		return nil, fmt.Errorf("db: read the items of files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var root int64
		var key string
		if err := rows.Scan(&root, &key); err != nil {
			return nil, fmt.Errorf("db: read the items of files: %w", err)
		}
		out.Add(root, Top(key))
	}
	return out, rows.Err()
}

// MissingBefore lists the paths of a root's files that have been missing
// since before a time: those a scan's ApplyScan removes with that time.
func (d *DB) MissingBefore(ctx context.Context, rootID, before int64) ([]string, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT rel_path FROM files WHERE root_id = ? AND missing_since IS NOT NULL AND missing_since < ?`, rootID, before)
	if err != nil {
		return nil, fmt.Errorf("db: read missing files: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("db: read missing files: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
