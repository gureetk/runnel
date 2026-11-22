// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// LocalSource is where a local image came from: a file below a root, as
// it was when it was scaled. It is kept in images.source_ref.
type LocalSource struct {
	Root    int64  `json:"root"`
	Path    string `json:"path"` // below the root
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
}

// LocalImage is an item's image from a local file.
type LocalImage struct {
	ID     int64
	ItemID int64
	Kind   string
	Key    string
	Widths []int
	Source LocalSource
}

// LocalImages lists the local images of a library's items.
func (d *DB) LocalImages(ctx context.Context, libraryID int64) ([]LocalImage, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT im.id, im.item_id, im.kind, im.key, im.widths, COALESCE(im.source_ref, '')
		 FROM images im JOIN items i ON i.id = im.item_id WHERE i.library_id = ? AND im.source = 'local'`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("db: read images: %w", err)
	}
	defer rows.Close()
	var out []LocalImage
	for rows.Next() {
		var im LocalImage
		var widths, ref string
		if err := rows.Scan(&im.ID, &im.ItemID, &im.Kind, &im.Key, &widths, &ref); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(widths), &im.Widths)
		json.Unmarshal([]byte(ref), &im.Source)
		out = append(out, im)
	}
	return out, rows.Err()
}

// RemoveImages deletes images by id, and bumps their items' sequence. It
// returns the keys that no image has any more, whose files can go.
func (d *DB) RemoveImages(ctx context.Context, ids []int64) ([]string, error) {
	var gone []string
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var items []int64
		for _, id := range ids {
			var item int64
			var key string
			err := tx.tx.QueryRowContext(ctx, `DELETE FROM images WHERE id = ? RETURNING item_id, key`, id).Scan(&item, &key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			items = append(items, item)
			if unused, err := keyUnused(ctx, tx, key); err != nil {
				return err
			} else if unused {
				gone = append(gone, key)
			}
		}
		return tx.stampItems(ctx, items)
	})
	if err != nil {
		return nil, fmt.Errorf("db: remove images: %w", err)
	}
	return gone, nil
}

// KeysInUse reports which of the keys an image or a person's photo still
// has.
func (d *DB) KeysInUse(ctx context.Context, keys []string) (map[string]bool, error) {
	list, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	rows, err := d.r.QueryContext(ctx, `
		SELECT key FROM images WHERE key IN (SELECT value FROM json_each(?1))
		UNION SELECT image_key FROM people WHERE image_key IN (SELECT value FROM json_each(?1))`, string(list))
	if err != nil {
		return nil, fmt.Errorf("db: read image keys: %w", err)
	}
	defer rows.Close()
	used := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("db: read image keys: %w", err)
		}
		used[k] = true
	}
	return used, rows.Err()
}

func keyUnused(ctx context.Context, tx *Tx, key string) (bool, error) {
	var used bool
	err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM images WHERE key = ?)`, key).Scan(&used)
	return !used, err
}

// ImageSet is a stored image: its key, the widths kept, its aspect ratio
// and its placeholder colour.
type ImageSet struct {
	Key    string
	Widths []int
	Aspect float64
	Color  string
}

// SetLocalImage makes a local image an item's image of a kind, in place of
// any other local image of that kind, and bumps the item's sequence. It
// returns the keys that no image has any more.
func (d *DB) SetLocalImage(ctx context.Context, itemID int64, kind string, set ImageSet, src LocalSource) ([]string, error) {
	widths, _ := json.Marshal(set.Widths)
	ref, _ := json.Marshal(src)
	var gone []string
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var exists bool
		if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM items WHERE id = ?)`, itemID).Scan(&exists); err != nil || !exists {
			return err // the item went since the image was queued
		}
		rows, err := tx.tx.QueryContext(ctx,
			`DELETE FROM images WHERE item_id = ? AND kind = ? AND source = 'local' AND key <> ? RETURNING key`, itemID, kind, set.Key)
		if err != nil {
			return err
		}
		var old []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			old = append(old, k)
		}
		rows.Close()
		if _, err := tx.tx.ExecContext(ctx,
			`INSERT INTO images (item_id, kind, key, widths, aspect, color, source, source_ref, rank) VALUES (?, ?, ?, ?, ?, ?, 'local', ?, 0)
			 ON CONFLICT (item_id, kind, key) DO UPDATE SET widths = excluded.widths, aspect = excluded.aspect, color = excluded.color,
			   source_ref = excluded.source_ref`,
			itemID, kind, set.Key, string(widths), nullFloat(set.Aspect), nullString(set.Color), string(ref)); err != nil {
			return err
		}
		for _, k := range old {
			if unused, err := keyUnused(ctx, tx, k); err != nil {
				return err
			} else if unused {
				gone = append(gone, k)
			}
		}
		return tx.stampItems(ctx, []int64{itemID})
	})
	if err != nil {
		return nil, fmt.Errorf("db: set image of item %d: %w", itemID, err)
	}
	return gone, nil
}

// ImageByKey is the kind and the stored widths of an image, or
// ErrNotFound.
func (d *DB) ImageByKey(ctx context.Context, key string) (kind string, widths []int, err error) {
	var w string
	err = d.r.QueryRowContext(ctx, `SELECT kind, widths FROM images WHERE key = ? LIMIT 1`, key).Scan(&kind, &w)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, fmt.Errorf("db: read image: %w", err)
	}
	json.Unmarshal([]byte(w), &widths)
	return kind, widths, nil
}

// ImageOfItem is an item's stored image of a kind, if it has one with
// that key already: then a file that was renamed needs no new scaling.
func (d *DB) ImageOfItem(ctx context.Context, itemID int64, kind, key string) (ImageSet, bool, error) {
	var s ImageSet
	var w string
	var aspect sql.NullFloat64
	var color sql.NullString
	err := d.r.QueryRowContext(ctx, `SELECT widths, aspect, color FROM images WHERE item_id = ? AND kind = ? AND key = ?`, itemID, kind, key).
		Scan(&w, &aspect, &color)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageSet{}, false, nil
	}
	if err != nil {
		return ImageSet{}, false, fmt.Errorf("db: read image: %w", err)
	}
	json.Unmarshal([]byte(w), &s.Widths)
	s.Key, s.Aspect, s.Color = key, aspect.Float64, color.String
	return s, true, nil
}

// RootPath is a root's folder.
func (d *DB) RootPath(ctx context.Context, rootID int64) (string, error) {
	var p string
	err := d.r.QueryRowContext(ctx, `SELECT path FROM roots WHERE id = ?`, rootID).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return p, err
}

func nullFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}
