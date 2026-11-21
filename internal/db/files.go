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

// KnownFile is a file the database holds for a root, as a scan compares it
// with the disk (docs/03-data-model.md, section 6, the scanner's
// comparison set).
type KnownFile struct {
	ID           int64
	RelPath      string
	Kind         string
	Size         int64
	MtimeNS      int64
	MissingSince int64 // 0 while the file is there
	Fingerprint  string
}

// KnownFiles is the comparison set of one root, by relative path.
func (d *DB) KnownFiles(ctx context.Context, rootID int64) (map[string]KnownFile, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT id, rel_path, kind, size, mtime_ns, missing_since, fingerprint FROM files WHERE root_id = ?`, rootID)
	if err != nil {
		return nil, fmt.Errorf("db: read files: %w", err)
	}
	defer rows.Close()
	known := map[string]KnownFile{}
	for rows.Next() {
		var f KnownFile
		var missing sql.NullInt64
		var fp sql.NullString
		if err := rows.Scan(&f.ID, &f.RelPath, &f.Kind, &f.Size, &f.MtimeNS, &missing, &fp); err != nil {
			return nil, fmt.Errorf("db: read files: %w", err)
		}
		f.MissingSince, f.Fingerprint = missing.Int64, fp.String
		known[f.RelPath] = f
	}
	return known, rows.Err()
}

// SeenFile is a file as one walk found it.
type SeenFile struct {
	ID      int64 // for a known file; 0 for a new one
	RelPath string
	Kind    string // "video" or "extra"
	Size    int64
	MtimeNS int64
}

// ScanChanges is what one walk of a root found that differs from the
// database.
type ScanChanges struct {
	New     []SeenFile // paths the database did not know
	Changed []SeenFile // size or time changed: probed again
	Moved   []SeenFile // a missing or unseen file found at a new path, by its fingerprint
	Back    []SeenFile // files marked missing that are there again, unchanged
	Missing []int64    // files that were there and are not any more
	Result  model.ScanResult
}

// ApplyScan writes one walk of a root in a single transaction, records its
// result on the root, marks the root available, and removes files that
// have been missing since before purgeBefore (docs/03-data-model.md,
// section 4).
func (d *DB) ApplyScan(ctx context.Context, rootID int64, c ScanChanges, now, purgeBefore int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		ins, err := tx.tx.PrepareContext(ctx,
			`INSERT INTO files (root_id, rel_path, kind, size, mtime_ns, first_seen_at) VALUES (?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer ins.Close()
		for _, f := range c.New {
			if _, err := ins.ExecContext(ctx, rootID, f.RelPath, f.Kind, f.Size, f.MtimeNS, now); err != nil {
				return fmt.Errorf("new file %q: %w", f.RelPath, err)
			}
		}

		var touched []int64
		for _, f := range c.Changed {
			if _, err := tx.tx.ExecContext(ctx,
				`UPDATE files SET kind = ?, size = ?, mtime_ns = ?, fingerprint = NULL, probe_version = 0, missing_since = NULL WHERE id = ?`,
				f.Kind, f.Size, f.MtimeNS, f.ID); err != nil {
				return err
			}
			// A probe that failed was of the file as it was: the changed
			// file is probed afresh.
			if _, err := tx.tx.ExecContext(ctx, `DELETE FROM tasks WHERE kind = 'probe' AND key = ? AND state = 'failed'`, FileKey(f.ID)); err != nil {
				return err
			}
			touched = append(touched, f.ID)
		}
		for _, f := range c.Moved {
			if _, err := tx.tx.ExecContext(ctx,
				`UPDATE files SET rel_path = ?, kind = ?, size = ?, mtime_ns = ?, missing_since = NULL WHERE id = ?`,
				f.RelPath, f.Kind, f.Size, f.MtimeNS, f.ID); err != nil {
				return fmt.Errorf("moved file %q: %w", f.RelPath, err)
			}
			touched = append(touched, f.ID)
		}
		for _, f := range c.Back {
			if _, err := tx.tx.ExecContext(ctx, `UPDATE files SET missing_since = NULL WHERE id = ?`, f.ID); err != nil {
				return err
			}
			touched = append(touched, f.ID)
		}
		for _, id := range c.Missing {
			if _, err := tx.tx.ExecContext(ctx,
				`UPDATE files SET missing_since = ? WHERE id = ? AND missing_since IS NULL`, now, id); err != nil {
				return err
			}
			touched = append(touched, id)
		}
		if err := bumpItemsOfFiles(ctx, tx, touched); err != nil {
			return err
		}

		// After the grace period a missing file goes. Items left with no
		// file are removed when items are built.
		if _, err := tx.tx.ExecContext(ctx,
			`DELETE FROM files WHERE root_id = ? AND missing_since IS NOT NULL AND missing_since < ?`, rootID, purgeBefore); err != nil {
			return err
		}
		return recordScan(ctx, tx, rootID, true, c.Result)
	})
	if err != nil {
		return fmt.Errorf("db: apply scan of root %d: %w", rootID, err)
	}
	return nil
}

// RootUnavailable records a scan that found the root away: it cannot be
// listed, or lists empty while it held files. Nothing under it is marked
// missing.
func (d *DB) RootUnavailable(ctx context.Context, rootID int64, result model.ScanResult) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		return recordScan(ctx, tx, rootID, false, result)
	})
	if err != nil {
		return fmt.Errorf("db: record unavailable root %d: %w", rootID, err)
	}
	return nil
}

// recordScan writes a scan's result on its root. A root that goes away or
// comes back bumps its library's sequence, and nothing else
// (docs/03-data-model.md, section 4).
func recordScan(ctx context.Context, tx *Tx, rootID int64, available bool, r model.ScanResult) error {
	var was bool
	var libraryID int64
	if err := tx.tx.QueryRowContext(ctx, `SELECT available, library_id FROM roots WHERE id = ?`, rootID).Scan(&was, &libraryID); err != nil {
		return err
	}
	if was != available {
		seq, err := tx.Seq(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE libraries SET updated_seq = ? WHERE id = ?`, seq, libraryID); err != nil {
			return err
		}
	}
	_, err := tx.tx.ExecContext(ctx,
		`UPDATE roots SET available = ?, last_scan_at = ?, last_scan_ms = ?, last_scan_added = ?,
		   last_scan_changed = ?, last_scan_removed = ?, last_scan_error = ? WHERE id = ?`,
		available, r.At, r.MS, r.Added, r.Changed, r.Removed, nullString(r.Error), rootID)
	return err
}

// bumpItemsOfFiles stamps every item that plays one of the files with this
// transaction's sequence values: a file change is a change to its items.
func bumpItemsOfFiles(ctx context.Context, tx *Tx, fileIDs []int64) error {
	if len(fileIDs) == 0 {
		return nil
	}
	ids, err := json.Marshal(fileIDs)
	if err != nil {
		return err
	}
	rows, err := tx.tx.QueryContext(ctx,
		`SELECT DISTINCT item_id FROM item_files WHERE file_id IN (SELECT value FROM json_each(?)) ORDER BY item_id`, string(ids))
	if err != nil {
		return err
	}
	var items []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		items = append(items, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.stampItems(ctx, items)
}

// ProbeTargetOf is the file a probe task names, as the database has it
// now, or ErrNotFound when there is nothing to probe: the file is gone,
// missing, on a root that is away, an extra, or probed with this version
// already.
func (d *DB) ProbeTargetOf(ctx context.Context, fileID int64, version int) (ProbeTarget, error) {
	var t ProbeTarget
	var root, rel string
	err := d.r.QueryRowContext(ctx,
		`SELECT f.id, r.path, f.rel_path, f.size, f.mtime_ns
		 FROM files f JOIN roots r ON r.id = f.root_id
		 WHERE f.id = ? AND f.probe_version < ? AND f.missing_since IS NULL AND f.kind = 'video' AND r.available = 1`,
		fileID, version).Scan(&t.FileID, &root, &rel, &t.Size, &t.MtimeNS)
	if errors.Is(err, sql.ErrNoRows) {
		return ProbeTarget{}, ErrNotFound
	}
	if err != nil {
		return ProbeTarget{}, fmt.Errorf("db: file to probe: %w", err)
	}
	t.Path = filepath.Join(root, filepath.FromSlash(rel))
	return t, nil
}

// Unprobed lists the ids of the video files that are there and whose
// probe is missing or older than version.
func (d *DB) Unprobed(ctx context.Context, version int) ([]int64, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT f.id FROM files f JOIN roots r ON r.id = f.root_id
		 WHERE f.probe_version < ? AND f.missing_since IS NULL AND f.kind = 'video' AND r.available = 1 ORDER BY f.id`, version)
	if err != nil {
		return nil, fmt.Errorf("db: files to probe: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UnreadableFile is a file ffprobe could not read.
type UnreadableFile struct {
	ID      int64
	RelPath string
	Error   string
}

// UnreadableFiles lists the files of a library whose probe failed.
func (d *DB) UnreadableFiles(ctx context.Context, libraryID int64) ([]UnreadableFile, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT f.id, f.rel_path, f.probe_error FROM files f JOIN roots r ON r.id = f.root_id
		 WHERE r.library_id = ? AND f.probe_error IS NOT NULL ORDER BY f.id`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("db: read unreadable files: %w", err)
	}
	defer rows.Close()
	var out []UnreadableFile
	for rows.Next() {
		var u UnreadableFile
		if err := rows.Scan(&u.ID, &u.RelPath, &u.Error); err != nil {
			return nil, fmt.Errorf("db: read unreadable files: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
