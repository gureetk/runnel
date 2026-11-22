// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gureetk/runnel/internal/model"
)

var (
	// ErrSetupDone is returned once the first person exists.
	ErrSetupDone = errors.New("db: the first person exists")
	// ErrWrongCode is returned for a setup code that is not the server's.
	ErrWrongCode = errors.New("db: the setup code is wrong")
)

// SetupCode makes the setup code while no user exists, or keeps the one
// made at an earlier start, and returns it (D35). Once a user exists it
// returns "" and deletes any code left.
func (d *DB) SetupCode(ctx context.Context, newCode func() string) (string, error) {
	var code string
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		code = ""
		var users bool
		if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&users); err != nil {
			return err
		}
		if users {
			_, err := tx.tx.ExecContext(ctx, `DELETE FROM meta WHERE key = 'setup_code'`)
			return err
		}
		err := tx.tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'setup_code'`).Scan(&code)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		code = newCode()
		_, err = tx.tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('setup_code', ?)`, code)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("db: the setup code: %w", err)
	}
	return code, nil
}

// Setup makes the first person, an admin, if the setup code is right,
// and signs them in on a device, in one transaction that also deletes the
// code (docs/04-api.md, section 3.4). ErrSetupDone: a person exists.
// ErrWrongCode: the code is not the server's.
func (d *DB) Setup(ctx context.Context, code, name string, pw model.Password, dev model.Device, tokenHash []byte, now int64) (model.User, error) {
	u := model.User{Name: name, IsAdmin: true}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var users bool
		var stored sql.NullString
		if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users), (SELECT value FROM meta WHERE key = 'setup_code')`).
			Scan(&users, &stored); err != nil {
			return err
		}
		switch {
		case users:
			return ErrSetupDone
		case !stored.Valid || subtle.ConstantTimeCompare([]byte(stored.String), []byte(code)) != 1:
			return ErrWrongCode
		}
		res, err := tx.tx.ExecContext(ctx, `INSERT INTO users (name, pw_hash, pw_salt, pw_iter, is_admin, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
			name, pw.Hash, pw.Salt, pw.Iter, now)
		if err != nil {
			return err
		}
		if u.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM meta WHERE key = 'setup_code'`); err != nil {
			return err
		}
		return tx.putToken(ctx, u.ID, dev, tokenHash, now)
	})
	if err != nil {
		if errors.Is(err, ErrSetupDone) || errors.Is(err, ErrWrongCode) {
			return model.User{}, err
		}
		return model.User{}, fmt.Errorf("db: make the first person: %w", err)
	}
	return u, nil
}

// Setting reads a setting kept in meta (docs/03-data-model.md, section
// 4), and whether it is set.
func (d *DB) Setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := d.r.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("db: read the setting %s: %w", key, err)
	}
	return v, true, nil
}

// SetSetting stores a setting, or with value nil removes it.
func (d *DB) SetSetting(ctx context.Context, key string, value *string) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if value == nil {
			_, err := tx.tx.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key)
			return err
		}
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, *value)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: store the setting %s: %w", key, err)
	}
	return nil
}
