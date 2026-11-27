// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
)

// KeyStatus is whether the metadata key worked at the last request made
// with it, and when that was (docs/03-data-model.md, section 4). Both are
// nil before any request.
type KeyStatus struct {
	OK        *bool
	CheckedAt *int64
}

// MetadataKey reads the metadata key kept in meta, "" when none is, and
// its status.
func (d *DB) MetadataKey(ctx context.Context) (string, KeyStatus, error) {
	var key, ok, at sql.NullString
	err := d.r.QueryRowContext(ctx, `SELECT
		(SELECT value FROM meta WHERE key = 'tmdb_key'),
		(SELECT value FROM meta WHERE key = 'tmdb_key_ok'),
		(SELECT value FROM meta WHERE key = 'tmdb_key_checked_at')`).Scan(&key, &ok, &at)
	if err != nil {
		return "", KeyStatus{}, fmt.Errorf("db: read the metadata key: %w", err)
	}
	var s KeyStatus
	if ok.Valid {
		v := ok.String == "1"
		s.OK = &v
	}
	if at.Valid {
		if v, err := strconv.ParseInt(at.String, 10, 64); err == nil {
			s.CheckedAt = &v
		}
	}
	return key.String, s, nil
}

// SetMetadataKey keeps a key that was just checked, as working at now; nil
// removes the key, and its status with it.
func (d *DB) SetMetadataKey(ctx context.Context, key *string, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if key == nil {
			_, err := tx.tx.ExecContext(ctx, `DELETE FROM meta WHERE key IN ('tmdb_key', 'tmdb_key_ok', 'tmdb_key_checked_at')`)
			return err
		}
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('tmdb_key', ?), ('tmdb_key_ok', '1'), ('tmdb_key_checked_at', ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, *key, strconv.FormatInt(now, 10))
		return err
	})
	if err != nil {
		return fmt.Errorf("db: keep the metadata key: %w", err)
	}
	return nil
}

// SetKeyStatus keeps whether the key worked at a request made at a time.
func (d *DB) SetKeyStatus(ctx context.Context, ok bool, at int64) error {
	v := "0"
	if ok {
		v = "1"
	}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('tmdb_key_ok', ?), ('tmdb_key_checked_at', ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v, strconv.FormatInt(at, 10))
		return err
	})
	if err != nil {
		return fmt.Errorf("db: keep the metadata key's status: %w", err)
	}
	return nil
}
