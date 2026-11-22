// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gureetk/runnel/internal/model"
)

// Device is a person signed in on a device: one token (docs/04-api.md,
// section 3.5).
type Device struct {
	ID         int64 // the token's id
	Name       string
	Client     string
	SignedInAt int64
	LastUsedAt int64 // 0 if never written
}

const deviceColumns = `id, device_name, client, created_at, last_used_at`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var dev Device
	var name, client sql.NullString
	var last sql.NullInt64
	err := row.Scan(&dev.ID, &name, &client, &dev.SignedInAt, &last)
	dev.Name, dev.Client, dev.LastUsedAt = name.String, client.String, last.Int64
	return dev, err
}

// Devices lists a user's signed-in devices, the one used last first.
func (d *DB) Devices(ctx context.Context, userID int64) ([]Device, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+deviceColumns+` FROM tokens
		WHERE user_id = ? AND revoked_at IS NULL ORDER BY COALESCE(last_used_at, created_at) DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: read devices: %w", err)
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		dev, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dev)
	}
	return out, rows.Err()
}

// DeviceOf reads the device of a token, or ErrNotFound.
func (d *DB) DeviceOf(ctx context.Context, tokenID int64) (Device, error) {
	dev, err := scanDevice(d.r.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM tokens WHERE id = ? AND revoked_at IS NULL`, tokenID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("db: read a device: %w", err)
	}
	return dev, nil
}

// SignOut deletes one of a user's tokens. ErrNotFound: the user has no
// such token.
func (d *DB) SignOut(ctx context.Context, userID, tokenID int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM tokens WHERE id = ? AND user_id = ?`, tokenID, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("db: sign out: %w", err)
	}
	return err
}

// PasswordOf reads a user's password, nil for one who signs in by
// pairing only. ErrNotFound: no such user.
func (d *DB) PasswordOf(ctx context.Context, userID int64) (*model.Password, error) {
	var p model.Password
	var iter sql.NullInt64
	err := d.r.QueryRowContext(ctx, `SELECT pw_hash, pw_salt, pw_iter FROM users WHERE id = ?`, userID).Scan(&p.Hash, &p.Salt, &iter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: read a password: %w", err)
	}
	if p.Hash == nil || !iter.Valid {
		return nil, nil
	}
	p.Iter = int(iter.Int64)
	return &p, nil
}

// SetPassword stores a user's new password. Their tokens stay valid.
func (d *DB) SetPassword(ctx context.Context, userID int64, p model.Password) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE users SET pw_hash = ?, pw_salt = ?, pw_iter = ? WHERE id = ?`, p.Hash, p.Salt, p.Iter, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: store a password: %w", err)
	}
	return nil
}

// UserRow is a user as GET /admin/users lists them (docs/04-api.md,
// section 8.6).
type UserRow struct {
	model.User
	HasPassword  bool
	Devices      int
	LastActiveAt int64 // the latest use of one of their tokens; 0 never
}

// Users lists every user, by name.
func (d *DB) Users(ctx context.Context) ([]UserRow, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT u.id, u.name, u.is_admin, u.pw_hash IS NOT NULL,
		  (SELECT count(*) FROM tokens t WHERE t.user_id = u.id AND t.revoked_at IS NULL),
		  (SELECT max(COALESCE(t.last_used_at, t.created_at)) FROM tokens t WHERE t.user_id = u.id AND t.revoked_at IS NULL)
		FROM users u ORDER BY u.name`)
	if err != nil {
		return nil, fmt.Errorf("db: read users: %w", err)
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		var last sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Name, &u.IsAdmin, &u.HasPassword, &u.Devices, &last); err != nil {
			return nil, err
		}
		u.LastActiveAt = last.Int64
		out = append(out, u)
	}
	return out, rows.Err()
}
