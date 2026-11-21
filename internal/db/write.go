// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Tx is a write transaction. It is only valid inside the function given to
// Write.
type Tx struct {
	tx  *sql.Tx
	seq int64 // 0 until Seq is first called
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

	if err := fn(ctx, &Tx{tx: sqlTx}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	committed = true
	return nil
}

// Seq returns this transaction's value of the change sequence, taking the
// next value the first time it is called. A transaction that changes
// nothing a client can see never calls it, so it uses up no value
// (docs/03-data-model.md, section 5).
func (t *Tx) Seq(ctx context.Context) (int64, error) {
	if t.seq != 0 {
		return t.seq, nil
	}
	err := t.tx.QueryRowContext(ctx,
		`UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key = 'seq' RETURNING CAST(value AS INTEGER) AS seq`,
	).Scan(&t.seq)
	if err != nil {
		return 0, fmt.Errorf("db: next sequence: %w", err)
	}
	return t.seq, nil
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
