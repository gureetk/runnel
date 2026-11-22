// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gureetk/runnel/internal/model"
)

// ErrRootOverlap is returned for a root that is, contains or lies inside
// another root: its files would be indexed twice.
var ErrRootOverlap = errors.New("db: the folder is, contains or lies inside a folder that a library already scans")

// CreateLibrary adds a library with its roots. Root paths must be
// absolute; they are stored cleaned, without a trailing slash.
func (d *DB) CreateLibrary(ctx context.Context, name, kind, profile string, roots []string, now int64) (model.Library, error) {
	lib := model.Library{Name: name, Kind: kind, Profile: profile}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var taken bool
		if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM libraries WHERE name = ?)`, name).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		existing, err := rootPaths(ctx, tx)
		if err != nil {
			return err
		}
		var clean []string
		for _, r := range roots {
			p := filepath.Clean(r)
			for _, other := range append(existing, clean...) {
				if overlaps(p, other) {
					return ErrRootOverlap
				}
			}
			clean = append(clean, p)
		}

		seq, err := tx.Seq(ctx)
		if err != nil {
			return err
		}
		// A client that missed a removed library would keep its items
		// under a new library given the same id.
		if lib.ID, err = tx.newID(ctx, "last_library_id", "libraries"); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx,
			`INSERT INTO libraries (id, name, kind, profile, created_at, updated_seq) VALUES (?, ?, ?, ?, ?, ?)`,
			lib.ID, name, kind, profile, now, seq); err != nil {
			return err
		}
		lib.UpdatedSeq = seq
		for _, p := range clean {
			res, err := tx.tx.ExecContext(ctx, `INSERT INTO roots (library_id, path) VALUES (?, ?)`, lib.ID, p)
			if err != nil {
				return err
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			lib.Roots = append(lib.Roots, model.Root{ID: id, LibraryID: lib.ID, Path: p, Available: true})
		}
		return nil
	})
	if errors.Is(err, ErrNameTaken) || errors.Is(err, ErrRootOverlap) {
		return model.Library{}, err
	}
	if err != nil {
		return model.Library{}, fmt.Errorf("db: create library: %w", err)
	}
	return lib, nil
}

func rootPaths(ctx context.Context, tx *Tx) ([]string, error) {
	rows, err := tx.tx.QueryContext(ctx, `SELECT path FROM roots`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// overlaps reports whether one of two cleaned absolute paths is the other
// or lies inside it.
func overlaps(a, b string) bool {
	within := func(inner, outer string) bool {
		return inner == outer || outer == "/" || strings.HasPrefix(inner, outer+"/")
	}
	return within(a, b) || within(b, a)
}

// Libraries lists every library with its roots, in id order.
func (d *DB) Libraries(ctx context.Context) ([]model.Library, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT l.id, l.name, l.kind, l.profile, l.updated_seq, r.id, r.path, r.available
		 FROM libraries l LEFT JOIN roots r ON r.library_id = l.id
		 ORDER BY l.id, r.id`)
	if err != nil {
		return nil, fmt.Errorf("db: read libraries: %w", err)
	}
	defer rows.Close()
	var out []model.Library
	for rows.Next() {
		var l model.Library
		var rootID *int64
		var path *string
		var available *bool
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Profile, &l.UpdatedSeq, &rootID, &path, &available); err != nil {
			return nil, fmt.Errorf("db: read libraries: %w", err)
		}
		if n := len(out); n == 0 || out[n-1].ID != l.ID {
			out = append(out, l)
		}
		if rootID != nil {
			last := &out[len(out)-1]
			last.Roots = append(last.Roots, model.Root{ID: *rootID, LibraryID: l.ID, Path: *path, Available: *available})
		}
	}
	return out, rows.Err()
}

// Library is one library with its roots, or ErrNotFound.
func (d *DB) Library(ctx context.Context, id int64) (model.Library, error) {
	all, err := d.Libraries(ctx)
	if err != nil {
		return model.Library{}, err
	}
	for _, l := range all {
		if l.ID == id {
			return l, nil
		}
	}
	return model.Library{}, ErrNotFound
}
