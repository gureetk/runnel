// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

// InstanceID returns the server's instance id, making it on the first
// start. It is a random UUID, kept in meta (docs/03-data-model.md,
// section 4, Settings and sign-in).
func (d *DB) InstanceID(ctx context.Context) (string, error) {
	var id string
	err := d.r.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'instance_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db: read instance id: %w", err)
	}
	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES ('instance_id', ?) ON CONFLICT (key) DO NOTHING`,
			newUUID()); err != nil {
			return err
		}
		return tx.tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'instance_id'`).Scan(&id)
	})
	if err != nil {
		return "", fmt.Errorf("db: make instance id: %w", err)
	}
	return id, nil
}

// SetupDone reports whether any user exists. While none does, a client
// opens the first run (docs/04-api.md, section 3.1).
func (d *DB) SetupDone(ctx context.Context) (bool, error) {
	var done bool
	if err := d.r.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&done); err != nil {
		return false, fmt.Errorf("db: read users: %w", err)
	}
	return done, nil
}

// newUUID returns a random (version 4) UUID in its usual text form.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) // never fails (crypto/rand, Go 1.24)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
