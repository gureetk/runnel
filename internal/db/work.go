// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Work is the progress of one kind of background work in one library
// (docs/04-api.md, section 8.3).
type Work struct {
	Kind                string
	LibraryID           int64 // 0 for work of no library
	Done, Total, Failed int
	Running, Queued     int
	Error               string // the last failure's message: of a task that failed, else of one that waits to try again
	Since               int64
}

// WorkOf reads the progress of background work, one row for each kind
// and library, as docs/03-data-model.md, section 6, counts it.
func (d *DB) WorkOf(ctx context.Context) ([]Work, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT t.kind, t.library_id,
		  SUM(t.state = 'done'), COUNT(*), SUM(t.state = 'failed'), SUM(t.state = 'running'), SUM(t.state = 'queued'),
		  (SELECT f.error FROM tasks f WHERE f.kind = t.kind AND f.library_id IS t.library_id AND f.error IS NOT NULL
		   ORDER BY f.state = 'failed' DESC, f.id DESC LIMIT 1),
		  MIN(t.created_at)
		FROM tasks t GROUP BY t.kind, t.library_id ORDER BY t.library_id, t.kind`)
	if err != nil {
		return nil, fmt.Errorf("db: read background work: %w", err)
	}
	defer rows.Close()
	out := []Work{}
	for rows.Next() {
		var w Work
		var lib sql.NullInt64
		var msg sql.NullString
		if err := rows.Scan(&w.Kind, &lib, &w.Done, &w.Total, &w.Failed, &w.Running, &w.Queued, &msg, &w.Since); err != nil {
			return nil, err
		}
		w.LibraryID, w.Error = lib.Int64, msg.String
		out = append(out, w)
	}
	return out, rows.Err()
}

// CancelWork drops the queued and the failed tasks of a kind in a
// library; what runs finishes (docs/04-api.md, section 8.3, and
// docs/03-data-model.md, section 4). It returns how many it dropped.
func (d *DB) CancelWork(ctx context.Context, kind string, libraryID int64) (int, error) {
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM tasks WHERE kind = ? AND library_id IS ? AND state IN ('queued', 'failed')`,
			kind, nullInt(libraryID))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		// Done rows of a kind with nothing left queued or running go too,
		// as when the work ends (docs/03-data-model.md, section 4).
		_, err = tx.tx.ExecContext(ctx, `DELETE FROM tasks WHERE kind = ?1 AND library_id IS ?2 AND state = 'done'
			AND NOT EXISTS (SELECT 1 FROM tasks WHERE kind = ?1 AND library_id IS ?2 AND state IN ('queued', 'running'))`,
			kind, nullInt(libraryID))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("db: cancel background work: %w", err)
	}
	return int(n), nil
}

// RetryWork queues again the failed tasks of a kind in a library, as new,
// and returns how many.
func (d *DB) RetryWork(ctx context.Context, kind string, libraryID, now int64) (int, error) {
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET state = 'queued', attempts = 0, run_after = ?, error = NULL
			WHERE kind = ? AND library_id IS ? AND state = 'failed'`, now, kind, nullInt(libraryID))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("db: retry background work: %w", err)
	}
	return int(n), nil
}
