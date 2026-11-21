// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"embed"
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
		out = append(out, migration{version: v, name: base, sql: string(body)})
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
	for _, m := range ms[current:] {
		err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
			if _, err := tx.tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.tx.ExecContext(ctx,
				`INSERT INTO meta (key, value) VALUES ('schema_version', ?)
				 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
				strconv.Itoa(m.version))
			return err
		})
		if err != nil {
			return fmt.Errorf("db: migration %s: %w", m.name, err)
		}
	}
	return nil
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
