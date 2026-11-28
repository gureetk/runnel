// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// SyncCollections makes a library's collections what its films say
// (docs/06-library-and-metadata.md, section 6.6; docs/03-data-model.md,
// section 4): a collection item for each provider collection that two or
// more of the library's films name in external_ids, the films pointed at
// it, and none for one that fewer name. A new collection is titled by
// names, the provider's name for it, until its own details are fetched.
// What changes is stamped; a collection that goes leaves a tombstone. It
// returns the collections it made. With refs, only the provider's
// collections it names are counted, as after one film's fetch; nil counts
// them all.
func (d *DB) SyncCollections(ctx context.Context, libraryID int64, refs []string, names map[string]string, sortTitle func(string) string, now int64) ([]int64, error) {
	if refs != nil && len(refs) == 0 {
		return nil, nil
	}
	// only narrows a query to refs, as its argument ?2 on.
	only, args := "", []any{libraryID}
	if refs != nil {
		only = " AND x.value IN (?" + strings.Repeat(", ?", len(refs)-1) + ")"
		for _, r := range refs {
			args = append(args, r)
		}
	}
	var made []int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		made = nil
		films := map[string][]int64{}
		rows, err := tx.tx.QueryContext(ctx, `SELECT x.value, i.id FROM external_ids x JOIN items i ON i.id = x.item_id
			WHERE x.provider = 'tmdb_collection' AND i.type = 'movie' AND i.library_id = ?`+only+` ORDER BY i.id`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v string
			var id int64
			if err := rows.Scan(&v, &id); err != nil {
				rows.Close()
				return err
			}
			films[v] = append(films[v], id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		have := map[string]int64{}
		rows, err = tx.tx.QueryContext(ctx, `SELECT x.value, i.id FROM items i
			JOIN external_ids x ON x.item_id = i.id AND x.provider = 'tmdb_collection'
			WHERE i.type = 'collection' AND i.library_id = ?`+only, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v string
			var id int64
			if err := rows.Scan(&v, &id); err != nil {
				rows.Close()
				return err
			}
			have[v] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// What every film of the library should point at.
		want := map[int64]int64{}
		for _, v := range slices.Sorted(maps.Keys(films)) {
			if len(films[v]) < 2 {
				continue
			}
			id, ok := have[v]
			if !ok {
				if id, err = tx.newCollection(ctx, libraryID, v, names[v], sortTitle, now); err != nil {
					return err
				}
				made = append(made, id)
			}
			for _, f := range films[v] {
				want[f] = id
			}
		}
		// Films that point elsewhere, or at nothing they should.
		var stamp []int64
		rows, err = tx.tx.QueryContext(ctx, `SELECT i.id, i.collection_id FROM items i WHERE i.library_id = ? AND i.type = 'movie'
			AND (i.collection_id IN (SELECT x.item_id FROM external_ids x WHERE x.provider = 'tmdb_collection'`+only+`)
			  OR i.id IN (SELECT x.item_id FROM external_ids x WHERE x.provider = 'tmdb_collection'`+only+`))`, append(args, args[1:]...)...)
		if err != nil {
			return err
		}
		type move struct{ film, to int64 }
		var moves []move
		for rows.Next() {
			var f int64
			var at sql.NullInt64
			if err := rows.Scan(&f, &at); err != nil {
				rows.Close()
				return err
			}
			if at.Int64 != want[f] {
				moves = append(moves, move{f, want[f]})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range moves {
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET collection_id = ? WHERE id = ?`, nullInt(m.to), m.film); err != nil {
				return err
			}
			stamp = append(stamp, m.film)
		}
		// Collections fewer than two films name go.
		for _, v := range slices.Sorted(maps.Keys(have)) {
			if len(films[v]) >= 2 {
				continue
			}
			id := have[v]
			seq, err := tx.Seq(ctx)
			if err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO tombstones (seq, item_id, at) VALUES (?, ?, ?)`, seq, id, now); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `DELETE FROM items_fts WHERE rowid = ?`, id); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return tx.stampItems(ctx, stamp)
	})
	if err != nil {
		return nil, fmt.Errorf("db: the collections of library %d: %w", libraryID, err)
	}
	return made, nil
}

// newCollection makes a library's collection item for a provider's
// collection: matched by its id, in no folder, added now, and in the
// search table.
func (t *Tx) newCollection(ctx context.Context, libraryID int64, ref, title string, sortTitle func(string) string, now int64) (int64, error) {
	if title == "" {
		title = "Collection"
	}
	id, err := t.newID(ctx, "last_item_id", "items")
	if err != nil {
		return 0, err
	}
	seq, err := t.Seq(ctx)
	if err != nil {
		return 0, err
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO items (id, library_id, type, title, sort_title, added_at, match_state, updated_seq)
		VALUES (?, ?, 'collection', ?, ?, ?, 'matched', ?)`, id, libraryID, title, sortTitle(title), now, seq); err != nil {
		return 0, err
	}
	if _, err := t.tx.ExecContext(ctx, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, 'tmdb_collection', ?)`, id, ref); err != nil {
		return 0, err
	}
	if err := reindex(ctx, t, id, title, "", nil); err != nil {
		return 0, err
	}
	return id, nil
}
