// SPDX-License-Identifier: GPL-3.0-or-later

// Package db owns the SQLite database: opening it with its pragmas, the
// migrations, the one writer and the change sequence. SQL lives in this
// package and nowhere else (docs/02-architecture.md, section 4).
//
// The rules are in docs/03-data-model.md, sections 2, 5 and 7.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3" // registers "sqlite3"; built with -tags sqlite_fts5
)

// readers is the most connections that read at once.
const readers = 4

// pragmas are set on every connection, writer and readers alike
// (docs/03-data-model.md, section 2). The driver applies them as it opens
// each connection.
const pragmas = "_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_busy_timeout=5000&_cache_size=-1000"

// DB is the server's database: one connection that writes, used by one
// goroutine at a time, and a few that read.
type DB struct {
	w *sql.DB // one connection; every transaction is BEGIN IMMEDIATE
	r *sql.DB // up to readers connections, each with query_only set

	// lock is the write lock. Writers queue on it, in the process, so that
	// SQLite never has two of this process's writers waiting on each other.
	lock chan struct{}

	closed bool // set by Close, read and written only while holding lock
}

// ErrClosed is returned by a write after Close.
var ErrClosed = errors.New("db: the database is closed")

// Open opens the database at path, creating it if needed, checks that the
// driver was built with FTS5, and applies the migrations it is missing.
func Open(ctx context.Context, path string) (*DB, error) {
	// The driver reads what follows a '?' as its own options.
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("db: %q: a database path must not contain '?'", path)
	}

	w, err := sql.Open("sqlite3", path+"?"+pragmas+"&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("db: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)

	d := &DB{w: w, lock: make(chan struct{}, 1)}

	if err := checkFTS5(ctx, w); err != nil {
		w.Close()
		return nil, err
	}
	if err := d.migrate(ctx); err != nil {
		w.Close()
		return nil, err
	}

	// Readers are opened after the writer has put the file in WAL mode.
	r, err := sql.Open("sqlite3", path+"?"+pragmas+"&_query_only=1")
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("db: open readers: %w", err)
	}
	r.SetMaxOpenConns(readers)
	r.SetMaxIdleConns(readers)
	d.r = r
	if err := r.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: open readers: %w", err)
	}
	return d, nil
}

// Close waits for a write in progress to finish, then closes every
// connection. A write after Close returns ErrClosed. Closing twice is
// harmless.
func (d *DB) Close() error {
	d.lock <- struct{}{}
	defer func() { <-d.lock }()
	if d.closed {
		return nil
	}
	d.closed = true
	var errs []error
	if d.r != nil {
		errs = append(errs, d.r.Close())
	}
	errs = append(errs, d.w.Close())
	return errors.Join(errs...)
}

// checkFTS5 fails with an explanation when the driver was built without
// the sqlite_fts5 tag. The search table of migration 0001 needs FTS5.
func checkFTS5(ctx context.Context, w *sql.DB) error {
	var used int
	if err := w.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&used); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	if used != 1 {
		return errors.New("db: this binary was built without FTS5; build it with -tags sqlite_fts5")
	}
	return nil
}
