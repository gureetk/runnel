// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gureetk/runnel/internal/model"
)

// MaxRoots is how many folders a library may have (docs/04-api.md,
// section 8.2).
const MaxRoots = 32

// ErrNoSuchRoot is returned for a folder that is not one of the library's.
var ErrNoSuchRoot = errors.New("db: the folder is not one of the library's")

// ErrRootCount is returned for a change that would leave a library with
// no folder, or with more than MaxRoots.
var ErrRootCount = errors.New("db: a library has 1 to 32 folders")

// AdminRoot is a library folder with its last scan, nil before the first
// (docs/04-api.md, section 8.2).
type AdminRoot struct {
	model.Root
	LastScan *model.ScanResult
}

// AdminLibrary is a library as an admin sees it: Items counts its films
// and series.
type AdminLibrary struct {
	ID                  int64
	Name, Kind, Profile string
	Items               int
	Roots               []AdminRoot
}

// AdminLibraries lists the libraries with their folders and each
// folder's last scan, in id order.
func (d *DB) AdminLibraries(ctx context.Context) ([]AdminLibrary, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT l.id, l.name, l.kind, l.profile,
		  (SELECT count(*) FROM items i WHERE i.library_id = l.id AND i.type IN ('movie', 'series')),
		  r.id, r.path, r.available, r.last_scan_at, r.last_scan_ms, r.last_scan_added, r.last_scan_changed,
		  r.last_scan_removed, r.last_scan_error
		FROM libraries l LEFT JOIN roots r ON r.library_id = l.id ORDER BY l.id, r.id`)
	if err != nil {
		return nil, fmt.Errorf("db: read libraries: %w", err)
	}
	defer rows.Close()
	out := []AdminLibrary{}
	for rows.Next() {
		var l AdminLibrary
		var rootID, at, ms, added, changed, removed sql.NullInt64
		var path, msg sql.NullString
		var available sql.NullBool
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Profile, &l.Items, &rootID, &path, &available, &at, &ms,
			&added, &changed, &removed, &msg); err != nil {
			return nil, fmt.Errorf("db: read libraries: %w", err)
		}
		if len(out) == 0 || out[len(out)-1].ID != l.ID {
			out = append(out, l)
		}
		if !rootID.Valid {
			continue
		}
		r := AdminRoot{Root: model.Root{ID: rootID.Int64, LibraryID: l.ID, Path: path.String, Available: available.Bool}}
		if at.Valid {
			r.LastScan = &model.ScanResult{At: at.Int64, MS: ms.Int64, Added: int(added.Int64), Changed: int(changed.Int64),
				Removed: int(removed.Int64), Error: msg.String}
		}
		last := &out[len(out)-1]
		last.Roots = append(last.Roots, r)
	}
	return out, rows.Err()
}

// ErrKindChange is returned for a change of kind other than a films or a
// shows library becoming mixed (D49).
var ErrKindChange = errors.New("db: only a films or a shows library can change its kind, and only to mixed")

// LibraryChange is what a change to a library asks for. Nil fields stay
// as they are.
type LibraryChange struct {
	Name, Profile *string
	// Kind is "mixed", for a films or a shows library, or the library's
	// own kind, which changes nothing.
	Kind        *string
	AddRoots    []string // absolute paths
	RemoveRoots []int64
}

// ChangeLibrary renames a library, sets its profile, makes a films or a
// shows library mixed, and adds and removes folders, all in one
// transaction, and returns the library as it is then. A removed folder
// takes what was built from it: its items are deleted, each with a
// tombstone, children before their parents, as a build deletes them
// (docs/03-data-model.md, sections 4 and 5). A new kind changes no item:
// the next build reads the library's folders by it.
//
// ErrNotFound: no such library. ErrNameTaken: another library has the
// name. ErrKindChange: a change of kind that is not to mixed, or not from
// films or shows. ErrNoSuchRoot: a folder to remove is not the library's.
// ErrRootCount: the library would have no folder, or too many.
// ErrRootOverlap: a new folder is, contains or lies inside one that a
// library scans, other than those removed.
func (d *DB) ChangeLibrary(ctx context.Context, id int64, c LibraryChange, now int64) (model.Library, error) {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var name, kind, profile string
		err := tx.tx.QueryRowContext(ctx, `SELECT name, kind, profile FROM libraries WHERE id = ?`, id).Scan(&name, &kind, &profile)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if c.Name != nil && *c.Name != name {
			var taken bool
			if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM libraries WHERE name = ? AND id <> ?)`,
				*c.Name, id).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return ErrNameTaken
			}
			name = *c.Name
		}
		if c.Profile != nil {
			profile = *c.Profile
		}
		if c.Kind != nil && *c.Kind != kind {
			if *c.Kind != "mixed" || (kind != "movies" && kind != "shows") {
				return ErrKindChange
			}
			kind = *c.Kind
		}

		own := map[int64]string{}
		rows, err := tx.tx.QueryContext(ctx, `SELECT id, path FROM roots WHERE library_id = ?`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var rid int64
			var p string
			if err := rows.Scan(&rid, &p); err != nil {
				rows.Close()
				return err
			}
			own[rid] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		removed := map[int64]bool{}
		for _, rid := range c.RemoveRoots {
			if _, ok := own[rid]; !ok {
				return ErrNoSuchRoot
			}
			removed[rid] = true
		}
		if left := len(own) - len(removed) + len(c.AddRoots); left < 1 || left > MaxRoots {
			return ErrRootCount
		}
		var others []string
		all, err := tx.tx.QueryContext(ctx, `SELECT id, path FROM roots`)
		if err != nil {
			return err
		}
		for all.Next() {
			var rid int64
			var p string
			if err := all.Scan(&rid, &p); err != nil {
				all.Close()
				return err
			}
			if !removed[rid] {
				others = append(others, p)
			}
		}
		all.Close()
		if err := all.Err(); err != nil {
			return err
		}
		var added []string
		for _, r := range c.AddRoots {
			p := filepath.Clean(r)
			for _, other := range append(others, added...) {
				if overlaps(p, other) {
					return ErrRootOverlap
				}
			}
			added = append(added, p)
		}

		if len(removed) > 0 {
			ids := make([]int64, 0, len(removed))
			for rid := range removed {
				ids = append(ids, rid)
			}
			if err := tx.removeRoots(ctx, id, ids, now); err != nil {
				return err
			}
		}
		for _, p := range added {
			rid, err := tx.newID(ctx, "last_root_id", "roots")
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO roots (id, library_id, path) VALUES (?, ?, ?)`, rid, id, p); err != nil {
				return err
			}
		}
		seq, err := tx.Seq(ctx)
		if err != nil {
			return err
		}
		_, err = tx.tx.ExecContext(ctx, `UPDATE libraries SET name = ?, kind = ?, profile = ?, updated_seq = ? WHERE id = ?`,
			name, kind, profile, seq, id)
		return err
	})
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNameTaken), errors.Is(err, ErrKindChange), errors.Is(err, ErrNoSuchRoot),
		errors.Is(err, ErrRootCount), errors.Is(err, ErrRootOverlap):
		return model.Library{}, err
	case err != nil:
		return model.Library{}, fmt.Errorf("db: change library %d: %w", id, err)
	}
	return d.Library(ctx, id)
}

// removeRoots deletes folders of a library with their files and what was
// built from them, writing a tombstone for each item, episodes first,
// then seasons, then films and series.
func (t *Tx) removeRoots(ctx context.Context, libraryID int64, rootIDs []int64, now int64) error {
	list, err := json.Marshal(rootIDs)
	if err != nil {
		return err
	}
	rows, err := t.tx.QueryContext(ctx, `
		WITH top(id) AS (SELECT id FROM items WHERE library_id = ?1 AND root_id IN (SELECT value FROM json_each(?2)))
		SELECT i.id FROM items i
		WHERE i.id IN (SELECT id FROM top) OR i.series_id IN (SELECT id FROM top)
		ORDER BY CASE i.type WHEN 'episode' THEN 0 WHEN 'season' THEN 1 ELSE 2 END, i.id`, libraryID, string(list))
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var item int64
		if err := rows.Scan(&item); err != nil {
			rows.Close()
			return err
		}
		gone = append(gone, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range gone {
		seq, err := t.Seq(ctx)
		if err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO tombstones (seq, item_id, at) VALUES (?, ?, ?)`, seq, item, now); err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, `DELETE FROM items_fts WHERE rowid = ?`, item); err != nil {
			return err
		}
	}
	// The folders' items, files, tracks and images go with them.
	_, err = t.tx.ExecContext(ctx, `DELETE FROM roots WHERE library_id = ? AND id IN (SELECT value FROM json_each(?))`,
		libraryID, string(list))
	return err
}

// DeleteLibrary removes a library with its folders, files, items, watch
// state and queued work, and returns the ids its folders had. It writes
// no tombstones: /changes stops listing the library, and a client drops
// it with its items (docs/03-data-model.md, section 5). It takes a
// sequence value, so that ETags move. ErrNotFound: no such library.
func (d *DB) DeleteLibrary(ctx context.Context, id int64) ([]int64, error) {
	var roots []int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := tx.tx.QueryContext(ctx, `SELECT id FROM roots WHERE library_id = ? ORDER BY id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r int64
			if err := rows.Scan(&r); err != nil {
				rows.Close()
				return err
			}
			roots = append(roots, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM items_fts WHERE rowid IN (SELECT id FROM items WHERE library_id = ?)`, id); err != nil {
			return err
		}
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM libraries WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.Seq(ctx)
		return err
	})
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, err
	case err != nil:
		return nil, fmt.Errorf("db: remove library %d: %w", id, err)
	}
	return roots, nil
}
