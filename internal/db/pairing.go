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
	// ErrExpired is returned for a pairing that ran out or was spent.
	ErrExpired = errors.New("db: the pairing ran out or was spent")
	// ErrTooMany is returned when as many pairings wait as may.
	ErrTooMany = errors.New("db: as many pairings wait as may")
	// ErrApproved is returned for a pairing that was already approved.
	ErrApproved = errors.New("db: the pairing is already approved")
)

// PairingKept is how long a pairing is kept after it expires or is
// claimed, so that a lookup can still say it ran out
// (docs/03-data-model.md, section 4).
const PairingKept = 60 * 60 * 1000 // ms

// Pairing is a device that waits with a code (docs/04-api.md, section
// 3.3).
type Pairing struct {
	Code           string
	Device         model.Device
	CreatedAt      int64
	ExpiresAt      int64
	ApprovedUserID int64 // 0 while not approved
	ClaimedAt      int64 // 0 while not claimed
}

// open reports whether a pairing still waits at now.
func (p Pairing) open(now int64) bool { return p.ClaimedAt == 0 && now < p.ExpiresAt }

// forgotten reports whether a pairing ran out or was claimed more than
// PairingKept ago: it is as if it never was, though the row may still be
// there until the next pairing starts.
func (p Pairing) forgotten(now int64) bool {
	end := p.ExpiresAt
	if p.ClaimedAt != 0 {
		end = p.ClaimedAt
	}
	return end < now-PairingKept
}

const pairingColumns = `code, device_id, device_name, client, created_at, expires_at, approved_user_id, claimed_at`

func scanPairing(row interface{ Scan(...any) error }) (Pairing, error) {
	var p Pairing
	var name, client sql.NullString
	var approved, claimed sql.NullInt64
	err := row.Scan(&p.Code, &p.Device.ID, &name, &client, &p.CreatedAt, &p.ExpiresAt, &approved, &claimed)
	p.Device.Name, p.Device.Client, p.ApprovedUserID, p.ClaimedAt = name.String, client.String, approved.Int64, claimed.Int64
	return p, err
}

// prunePairings deletes the pairings that ran out or were claimed more
// than PairingKept ago.
func (t *Tx) prunePairings(ctx context.Context, now int64) error {
	_, err := t.tx.ExecContext(ctx, `DELETE FROM pairings WHERE COALESCE(claimed_at, expires_at) < ?`, now-PairingKept)
	return err
}

// StartPairing stores a device's new pairing, unless maxOpen pairings
// already wait: ErrTooMany. A code that is in use is ErrNameTaken, and the
// caller makes another.
func (d *DB) StartPairing(ctx context.Context, code string, secretHash []byte, dev model.Device, now, expiresAt int64, maxOpen int) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.prunePairings(ctx, now); err != nil {
			return err
		}
		var open int
		var taken bool
		if err := tx.tx.QueryRowContext(ctx, `SELECT
			  (SELECT count(*) FROM pairings WHERE claimed_at IS NULL AND expires_at > ?),
			  EXISTS (SELECT 1 FROM pairings WHERE code = ?)`, now, code).Scan(&open, &taken); err != nil {
			return err
		}
		switch {
		case open >= maxOpen:
			return ErrTooMany
		case taken:
			return ErrNameTaken
		}
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO pairings (code, secret_hash, device_id, device_name, client, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, code, secretHash, dev.ID, nullString(dev.Name), nullString(dev.Client), now, expiresAt)
		return err
	})
	if err != nil && !errors.Is(err, ErrTooMany) && !errors.Is(err, ErrNameTaken) {
		return fmt.Errorf("db: start a pairing: %w", err)
	}
	return err
}

// PairingOf finds a pairing that still waits. ErrNotFound: none has the
// code; ErrExpired: it ran out or was spent, within the last hour.
func (d *DB) PairingOf(ctx context.Context, code string, now int64) (Pairing, error) {
	p, err := scanPairing(d.r.QueryRowContext(ctx, `SELECT `+pairingColumns+` FROM pairings WHERE code = ?`, code))
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err == nil && p.forgotten(now)):
		return Pairing{}, ErrNotFound
	case err != nil:
		return Pairing{}, fmt.Errorf("db: read a pairing: %w", err)
	case !p.open(now):
		return Pairing{}, ErrExpired
	}
	return p, nil
}

// ApprovePairing lets a waiting device in as a user. It returns
// ErrNotFound, ErrExpired, or ErrApproved for a code approved before.
func (d *DB) ApprovePairing(ctx context.Context, code string, userID, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		p, err := scanPairing(tx.tx.QueryRowContext(ctx, `SELECT `+pairingColumns+` FROM pairings WHERE code = ?`, code))
		switch {
		case errors.Is(err, sql.ErrNoRows) || (err == nil && p.forgotten(now)):
			return ErrNotFound
		case err != nil:
			return err
		case !p.open(now):
			return ErrExpired
		case p.ApprovedUserID != 0:
			return ErrApproved
		}
		_, err = tx.tx.ExecContext(ctx, `UPDATE pairings SET approved_user_id = ? WHERE code = ?`, userID, code)
		return err
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrExpired) && !errors.Is(err, ErrApproved) {
		return fmt.Errorf("db: approve a pairing: %w", err)
	}
	return err
}

// ClaimPairing is a waiting device asking for its token. Until the code
// is approved it reports false. Once it is, it stores the token's hash for
// the approving user on the device, spends the pairing, and returns the
// user. A wrong secret is ErrNotFound, as is an unknown code; a code that
// ran out or was spent is ErrExpired.
func (d *DB) ClaimPairing(ctx context.Context, code string, secretHash, tokenHash []byte, now int64) (model.User, bool, error) {
	var u model.User
	approved := false
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var stored []byte
		row := tx.tx.QueryRowContext(ctx, `SELECT `+pairingColumns+`, secret_hash FROM pairings WHERE code = ?`, code)
		var p Pairing
		var name, client sql.NullString
		var by, claimed sql.NullInt64
		err := row.Scan(&p.Code, &p.Device.ID, &name, &client, &p.CreatedAt, &p.ExpiresAt, &by, &claimed, &stored)
		p.Device.Name, p.Device.Client, p.ApprovedUserID, p.ClaimedAt = name.String, client.String, by.Int64, claimed.Int64
		switch {
		case errors.Is(err, sql.ErrNoRows) || (err == nil && p.forgotten(now)):
			return ErrNotFound
		case err != nil:
			return err
		case subtle.ConstantTimeCompare(stored, secretHash) != 1:
			return ErrNotFound
		case !p.open(now):
			return ErrExpired
		case p.ApprovedUserID == 0:
			return nil
		}
		approved = true
		if err := tx.tx.QueryRowContext(ctx, `SELECT id, name, is_admin FROM users WHERE id = ?`, p.ApprovedUserID).
			Scan(&u.ID, &u.Name, &u.IsAdmin); err != nil {
			return err
		}
		if err := tx.putToken(ctx, u.ID, p.Device, tokenHash, now); err != nil {
			return err
		}
		_, err = tx.tx.ExecContext(ctx, `UPDATE pairings SET claimed_at = ? WHERE code = ?`, now, code)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrExpired) {
			return model.User{}, false, err
		}
		return model.User{}, false, fmt.Errorf("db: collect a pairing's token: %w", err)
	}
	return u, approved, nil
}

// OpenPairings lists the pairings that wait to be approved, oldest first.
func (d *DB) OpenPairings(ctx context.Context, now int64) ([]Pairing, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+pairingColumns+` FROM pairings
		WHERE claimed_at IS NULL AND approved_user_id IS NULL AND expires_at > ? ORDER BY created_at, code`, now)
	if err != nil {
		return nil, fmt.Errorf("db: read pairings: %w", err)
	}
	defer rows.Close()
	out := []Pairing{}
	for rows.Next() {
		p, err := scanPairing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
