// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/gureetk/runnel/internal/model"
)

// ErrNotFound is returned when the row asked for does not exist.
var ErrNotFound = errors.New("db: not found")

// ErrNameTaken is returned when a name must be new and is not.
var ErrNameTaken = errors.New("db: the name is taken")

// CreateUser adds a user. The first user is always an admin, so that a
// new server has one. Names compare without case, as the column does.
func (d *DB) CreateUser(ctx context.Context, name string, pw *model.Password, isAdmin bool, now int64) (model.User, error) {
	u := model.User{Name: name, IsAdmin: isAdmin}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var taken, anyUser bool
		if err := tx.tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM users WHERE name = ?), EXISTS (SELECT 1 FROM users)`, name,
		).Scan(&taken, &anyUser); err != nil {
			return err
		}
		if taken {
			return ErrNameTaken
		}
		if !anyUser {
			u.IsAdmin = true
		}
		var hash, salt, iter any
		if pw != nil {
			hash, salt, iter = pw.Hash, pw.Salt, pw.Iter
		}
		res, err := tx.tx.ExecContext(ctx,
			`INSERT INTO users (name, pw_hash, pw_salt, pw_iter, is_admin, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			name, hash, salt, iter, u.IsAdmin, now)
		if err != nil {
			return err
		}
		u.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNameTaken) {
			return model.User{}, err
		}
		return model.User{}, fmt.Errorf("db: create user: %w", err)
	}
	return u, nil
}

// UserForLogin returns the user with this name and their password. A user
// who signs in by pairing only has a nil password.
func (d *DB) UserForLogin(ctx context.Context, name string) (model.User, *model.Password, error) {
	var u model.User
	var p model.Password
	var iter sql.NullInt64
	err := d.r.QueryRowContext(ctx,
		`SELECT id, name, is_admin, pw_hash, pw_salt, pw_iter FROM users WHERE name = ?`, name,
	).Scan(&u.ID, &u.Name, &u.IsAdmin, &p.Hash, &p.Salt, &iter)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, nil, ErrNotFound
	}
	if err != nil {
		return model.User{}, nil, fmt.Errorf("db: read user: %w", err)
	}
	if p.Hash == nil || !iter.Valid {
		return u, nil, nil
	}
	p.Iter = int(iter.Int64)
	return u, &p, nil
}

// ReplaceToken stores the hash of a new token for a user on a device. An
// earlier token of the same user on the same device is deleted in the same
// transaction (docs/04-api.md, section 3.2).
func (d *DB) ReplaceToken(ctx context.Context, userID int64, dev model.Device, hash []byte, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error { return tx.putToken(ctx, userID, dev, hash, now) })
	if err != nil {
		return fmt.Errorf("db: store token: %w", err)
	}
	return nil
}

// putToken stores a token's hash for a user on a device, in place of an
// earlier token of the same user on the same device.
func (t *Tx) putToken(ctx context.Context, userID int64, dev model.Device, hash []byte, now int64) error {
	if _, err := t.tx.ExecContext(ctx, `DELETE FROM tokens WHERE user_id = ? AND device_id = ?`, userID, dev.ID); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO tokens (hash, user_id, device_id, device_name, client, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hash, userID, dev.ID, nullString(dev.Name), nullString(dev.Client), now, now)
	return err
}

// TokenUser is a token that is in use, with its user.
type TokenUser struct {
	TokenID    int64
	LastUsedAt int64 // 0 if never written
	User       model.User
}

// UserByToken finds the user of a token from its hash. A revoked or unknown
// token is ErrNotFound.
func (d *DB) UserByToken(ctx context.Context, hash []byte) (TokenUser, error) {
	var t TokenUser
	var last sql.NullInt64
	err := d.r.QueryRowContext(ctx,
		`SELECT t.id, t.last_used_at, u.id, u.name, u.is_admin
		 FROM tokens t JOIN users u ON u.id = t.user_id
		 WHERE t.hash = ? AND t.revoked_at IS NULL`, hash,
	).Scan(&t.TokenID, &last, &t.User.ID, &t.User.Name, &t.User.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenUser{}, ErrNotFound
	}
	if err != nil {
		return TokenUser{}, fmt.Errorf("db: read token: %w", err)
	}
	t.LastUsedAt = last.Int64
	return t, nil
}

// TouchToken records that a token was used. The caller writes it at most
// once in ten minutes for a token (docs/03-data-model.md, section 4).
func (d *DB) TouchToken(ctx context.Context, tokenID, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, now, tokenID)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: touch token: %w", err)
	}
	return nil
}

// nullString stores an empty string as NULL.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
