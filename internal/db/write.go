// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// StampBatch is the most rows that one sequence value is stamped on. A
// transaction that stamps more takes another value for every StampBatch
// rows, so that a page of /changes, which ends at a sequence boundary, is
// never larger than its limit or StampBatch rows (docs/03-data-model.md,
// section 5).
const StampBatch = 500

// Tx is a write transaction. It is only valid inside the function given to
// Write.
type Tx struct {
	tx   *sql.Tx
	seq  int64            // 0 until Seq is first called
	used int              // rows stamped with seq so far
	ids  map[string]int64 // the next id newID gives, by its key in meta
}

// Write runs fn in a write transaction and commits it if fn returns nil.
//
// Writers queue here, inside the process, and run one at a time on the one
// writing connection. The transaction starts with BEGIN IMMEDIATE, so it
// holds SQLite's write lock from its first statement
// (docs/03-data-model.md, section 2).
func (d *DB) Write(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) (err error) {
	select {
	case d.lock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-d.lock }()
	if d.closed {
		return ErrClosed
	}

	sqlTx, err := d.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Also runs when fn panics, so that the one writing connection
			// is never left inside a transaction.
			_ = sqlTx.Rollback()
		}
	}()

	t := &Tx{tx: sqlTx}
	if err := fn(ctx, t); err != nil {
		return err
	}
	for key, next := range t.ids {
		if _, err := sqlTx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, next-1); err != nil {
			return fmt.Errorf("db: record the ids given: %w", err)
		}
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	committed = true
	return nil
}

// newID returns the id for a new row of an item, a library, a library's
// folder, a user or a token: one above any id such a row has had, so that
// a removed row's id is never given to another. A client that missed the removal would take
// the new row for the old one, and so would work queued for it, or a call
// made from a page loaded before (docs/03-data-model.md, section 4). key
// names the highest id given in meta, which is written when the
// transaction commits. table is an internal name, never input.
func (t *Tx) newID(ctx context.Context, key, table string) (int64, error) {
	next, ok := t.ids[key]
	if !ok {
		if err := t.tx.QueryRowContext(ctx, `SELECT max(COALESCE((SELECT max(id) FROM `+table+`), 0),
			  COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key = ?), 0)) + 1`, key).Scan(&next); err != nil {
			return 0, fmt.Errorf("db: next id of %s: %w", table, err)
		}
		if t.ids == nil {
			t.ids = map[string]int64{}
		}
	}
	t.ids[key] = next + 1
	return next, nil
}

// Seq returns the sequence value to stamp on one row that this transaction
// changes. The first call takes the next value of the change sequence, and
// so does the first call after every StampBatch rows. A transaction that
// changes nothing a client can see never calls it, so it uses up no value
// (docs/03-data-model.md, section 5).
func (t *Tx) Seq(ctx context.Context) (int64, error) {
	seq, _, err := t.seqFor(ctx, 1)
	return seq, err
}

// seqFor returns the value to stamp on up to n rows, and on how many of
// them it may go.
func (t *Tx) seqFor(ctx context.Context, n int) (int64, int, error) {
	if t.seq == 0 || t.used >= StampBatch {
		err := t.tx.QueryRowContext(ctx,
			`UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'seq' RETURNING CAST(value AS INTEGER) AS seq`,
		).Scan(&t.seq)
		if err != nil {
			return 0, 0, fmt.Errorf("db: next sequence: %w", err)
		}
		t.used = 0
	}
	room := min(n, StampBatch-t.used)
	t.used += room
	return t.seq, room, nil
}

// stampItems stamps items with this transaction's sequence values, no more
// than StampBatch rows to a value.
func (t *Tx) stampItems(ctx context.Context, ids []int64) error {
	for len(ids) > 0 {
		seq, room, err := t.seqFor(ctx, len(ids))
		if err != nil {
			return err
		}
		chunk, err := json.Marshal(ids[:room])
		if err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(ctx, `UPDATE items SET updated_seq = ? WHERE id IN (SELECT value FROM json_each(?))`, seq, string(chunk)); err != nil {
			return err
		}
		ids = ids[room:]
	}
	return nil
}

// Seq reads the current value of the change sequence.
func (d *DB) Seq(ctx context.Context) (int64, error) {
	var seq int64
	err := d.r.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'seq'`).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("db: read sequence: %w", err)
	}
	return seq, nil
}
