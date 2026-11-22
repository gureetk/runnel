// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotListable is returned for an item that is not a film or a series:
// only those go on My list (docs/04-api.md, section 4.7).
var ErrNotListable = errors.New("db: only films and series go on My list")

// SetListed puts an item on a user's list at the time at, or with listed
// false takes it off. A row taken off stays, with listed_at null and a new
// sequence value, so that /changes can say it went
// (docs/03-data-model.md, section 4). Putting an item on that is on, or
// taking off one that is not, writes nothing. ErrNotFound: no such item.
func (d *DB) SetListed(ctx context.Context, userID, itemID int64, listed bool, at int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var typ string
		err := tx.tx.QueryRowContext(ctx, `SELECT type FROM items WHERE id = ?`, itemID).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if typ != "movie" && typ != "series" {
			return ErrNotListable
		}
		var cur sql.NullInt64
		err = tx.tx.QueryRowContext(ctx, `SELECT listed_at FROM user_list WHERE user_id = ? AND item_id = ?`, userID, itemID).Scan(&cur)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cur.Valid == listed {
			return nil
		}
		seq, err := tx.Seq(ctx)
		if err != nil {
			return err
		}
		var value any
		if listed {
			value = at
		}
		_, err = tx.tx.ExecContext(ctx, `INSERT INTO user_list (user_id, item_id, listed_at, updated_seq) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, item_id) DO UPDATE SET listed_at = excluded.listed_at, updated_seq = excluded.updated_seq`,
			userID, itemID, value, seq)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotListable) {
			return err
		}
		return fmt.Errorf("db: change My list: %w", err)
	}
	return nil
}

// ListedOf reads when a user put each of some items on the list. An item
// that is not on it is absent from the map.
func (d *DB) ListedOf(ctx context.Context, userID int64, ids []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := d.r.QueryContext(ctx, `SELECT item_id, listed_at FROM user_list
		WHERE user_id = ? AND listed_at IS NOT NULL AND item_id IN (SELECT value FROM json_each(?))`, userID, string(list))
	if err != nil {
		return nil, fmt.Errorf("db: read My list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}
