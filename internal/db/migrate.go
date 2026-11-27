// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one numbered SQL file.
type migration struct {
	version int
	name    string
	sql     string
	// rebuilds is set for a migration whose header says "-- Rebuilds:": it
	// makes tables again that others point at, so it runs with foreign keys
	// off and after a copy of the database (docs/03-data-model.md,
	// section 7).
	rebuilds bool
}

// migrations lists the embedded migrations in order. They are numbered
// from 1 with no gaps: 0001_init.sql, 0002_….sql, and so on.
func migrations() ([]migration, error) {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []migration
	for i, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v != i+1 {
			return nil, fmt.Errorf("db: migration %s: want number %04d", base, i+1)
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return nil, err
		}
		header, _, _ := strings.Cut(string(body), "\n\n")
		out = append(out, migration{version: v, name: base, sql: string(body),
			rebuilds: strings.Contains(header, "\n-- Rebuilds:")})
	}
	return out, nil
}

// SchemaVersion is the number of the newest migration this server has.
func SchemaVersion() int {
	ms, err := migrations()
	if err != nil {
		return 0
	}
	return len(ms)
}

// migrate applies the migrations the database is missing, each in its own
// transaction, and refuses a database newer than this server
// (docs/03-data-model.md, section 7).
func (d *DB) migrate(ctx context.Context) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	current, err := d.schemaVersion(ctx)
	if err != nil {
		return err
	}
	if current > len(ms) {
		return fmt.Errorf("db: the database is at schema version %d, and this server knows only %d: it is newer than this server", current, len(ms))
	}
	// A database that holds something is copied before the first migration
	// that rebuilds a table. A new one has nothing to lose.
	if current > 0 {
		for _, m := range ms[current:] {
			if m.rebuilds {
				p, err := d.backup(ctx, current, m.name)
				if err != nil {
					return err
				}
				d.Backup = p
				break
			}
		}
	}
	for _, m := range ms[current:] {
		var err error
		if m.rebuilds {
			err = d.rebuild(ctx, m)
		} else {
			err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
				return apply(ctx, tx.tx, m)
			})
		}
		if err != nil {
			return fmt.Errorf("db: migration %s: %w", m.name, err)
		}
	}
	return nil
}

// apply runs a migration's SQL and records its number, inside tx.
func apply(ctx context.Context, tx *sql.Tx, m migration) error {
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		strconv.Itoa(m.version))
	return err
}

// rebuild runs a migration that makes tables again, by SQLite's procedure
// for a change to a table that others point at: foreign keys off, outside
// any transaction, since the pragma does nothing inside one; the
// migration's new tables made, filled, the old ones dropped and the new
// ones renamed, in one transaction; every foreign key checked before the
// commit; foreign keys on again. With foreign keys on, dropping a table
// would delete every row that points at it, watch state among them
// (docs/03-data-model.md, section 7).
func (d *DB) rebuild(ctx context.Context, m migration) (err error) {
	d.lock <- struct{}{}
	defer func() { <-d.lock }()
	conn, err := d.w.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		if _, e := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); e != nil && err == nil {
			err = e
		}
	}()
	var on int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil {
		return err
	}
	if on != 0 {
		return errors.New("foreign keys could not be turned off")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := apply(ctx, tx, m); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	var broken []string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fk int
		if err := rows.Scan(&table, &rowid, &parent, &fk); err != nil {
			rows.Close()
			return err
		}
		if len(broken) < 5 {
			broken = append(broken, fmt.Sprintf("%s row %d to %s", table, rowid.Int64, parent))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(broken) > 0 {
		return fmt.Errorf("it would break foreign keys (%s); nothing was changed", strings.Join(broken, ", "))
	}
	return tx.Commit()
}

// schemaVersion reads meta.schema_version, which is 0 before migration 0001
// has made the meta table.
func (d *DB) schemaVersion(ctx context.Context) (int, error) {
	var n int
	err := d.w.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: read schema version: %w", err)
	}
	if n == 0 {
		return 0, nil
	}
	var v string
	err = d.w.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("db: read schema version: %w", err)
	}
	version, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("db: schema version %q is not a number", v)
	}
	return version, nil
}
