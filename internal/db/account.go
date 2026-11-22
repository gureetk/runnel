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

// ErrLastAdmin is returned for a change that would leave the server with
// no admin (docs/04-api.md, section 8.6).
var ErrLastAdmin = errors.New("db: the server would have no admin")

// UserChange is what a change to a user asks for. Nil fields stay as they
// are, and NoPassword takes the password away.
type UserChange struct {
	Name       *string
	Password   *model.Password
	NoPassword bool
	IsAdmin    *bool
}

// ChangeUser renames a user, gives them a new password or takes it away,
// and makes them an admin or not, all in one transaction. Their tokens
// stay valid. ErrNotFound: no such user. ErrNameTaken: another user has
// the name, compared without case. ErrLastAdmin: the change would leave
// the server without an admin.
func (d *DB) ChangeUser(ctx context.Context, id int64, c UserChange) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var u model.User
		err := tx.tx.QueryRowContext(ctx, `SELECT name, is_admin FROM users WHERE id = ?`, id).Scan(&u.Name, &u.IsAdmin)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if c.Name != nil {
			var taken bool
			if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE name = ? AND id <> ?)`, *c.Name, id).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return ErrNameTaken
			}
			u.Name = *c.Name
		}
		if c.IsAdmin != nil {
			if u.IsAdmin && !*c.IsAdmin {
				if err := lastAdmin(ctx, tx, id); err != nil {
					return err
				}
			}
			u.IsAdmin = *c.IsAdmin
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE users SET name = ?, is_admin = ? WHERE id = ?`, u.Name, u.IsAdmin, id); err != nil {
			return err
		}
		switch {
		case c.Password != nil:
			_, err = tx.tx.ExecContext(ctx, `UPDATE users SET pw_hash = ?, pw_salt = ?, pw_iter = ? WHERE id = ?`,
				c.Password.Hash, c.Password.Salt, c.Password.Iter, id)
		case c.NoPassword:
			_, err = tx.tx.ExecContext(ctx, `UPDATE users SET pw_hash = NULL, pw_salt = NULL, pw_iter = NULL WHERE id = ?`, id)
		}
		return err
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNameTaken) && !errors.Is(err, ErrLastAdmin) {
		return fmt.Errorf("db: change user %d: %w", id, err)
	}
	return err
}

// lastAdmin is ErrLastAdmin when no admin other than the user exists.
func lastAdmin(ctx context.Context, tx *Tx, id int64) error {
	var others bool
	if err := tx.tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE is_admin = 1 AND id <> ?)`, id).Scan(&others); err != nil {
		return err
	}
	if !others {
		return ErrLastAdmin
	}
	return nil
}

// DeleteUser removes a user, with their watch state, their list, their
// track choices and their sign-ins. Their rows of the start-time log stay,
// as the snapshots they are, without the user's id
// (docs/03-data-model.md, section 4). ErrNotFound: no such user.
// ErrLastAdmin: they are the last admin.
func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var admin bool
		err := tx.tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ?`, id).Scan(&admin)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if admin {
			if err := lastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE play_log SET user_id = NULL WHERE user_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		return err
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrLastAdmin) {
		return fmt.Errorf("db: remove user %d: %w", id, err)
	}
	return err
}

// SignedIn is a person signed in on a device, with who they are.
type SignedIn struct {
	Device
	UserID   int64
	UserName string
}

// AllDevices lists every signed-in device, one entry for each person on a
// device, the one used last first (docs/04-api.md, section 8.6).
func (d *DB) AllDevices(ctx context.Context) ([]SignedIn, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT t.id, t.device_name, t.client, t.created_at, t.last_used_at, u.id, u.name
		FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE t.revoked_at IS NULL ORDER BY COALESCE(t.last_used_at, t.created_at) DESC, t.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("db: read devices: %w", err)
	}
	defer rows.Close()
	out := []SignedIn{}
	for rows.Next() {
		var s SignedIn
		var name, client sql.NullString
		var last sql.NullInt64
		if err := rows.Scan(&s.ID, &name, &client, &s.SignedInAt, &last, &s.UserID, &s.UserName); err != nil {
			return nil, fmt.Errorf("db: read devices: %w", err)
		}
		s.Name, s.Client, s.LastUsedAt = name.String, client.String, last.Int64
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeToken signs out one device of anyone's. ErrNotFound: no such
// token.
func (d *DB) RevokeToken(ctx context.Context, tokenID int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, tokenID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("db: sign out a token: %w", err)
	}
	return err
}
