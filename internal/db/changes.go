// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
)

// Changes is one page of what changed for a user after a sequence value
// (docs/04-api.md, section 5). It holds a read transaction, so that every
// part of the page is read from the same view of the database. Close ends
// it.
type Changes struct {
	tx     *sql.Tx
	user   int64
	Since  int64
	Upto   int64 // the page holds every change above Since and at or below Upto
	Latest int64
	// Reset is set when the page cannot continue from Since: the delete
	// markers that far back are gone, or Since is ahead of this database.
	// The page then holds nothing.
	Reset bool
}

// Changes opens a page of what changed for a user after since. The page
// ends at a sequence boundary: it takes whole sequence values, at least
// one, while the rows the user sees stay within limit, and since a value is
// on at most StampBatch rows, a page holds at most limit rows, or
// StampBatch when limit is lower (docs/03-data-model.md, section 5).
func (d *DB) Changes(ctx context.Context, userID, since int64, limit int) (*Changes, error) {
	tx, err := d.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("db: begin a read: %w", err)
	}
	c := &Changes{tx: tx, user: userID, Since: since}
	if err := c.open(ctx, limit); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("db: read changes: %w", err)
	}
	return c, nil
}

func (c *Changes) open(ctx context.Context, limit int) error {
	var floor int64
	if err := c.tx.QueryRowContext(ctx, `SELECT (SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'seq'),
		  (SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'tombstone_floor')`).Scan(&c.Latest, &floor); err != nil {
		return err
	}
	if c.Since > c.Latest || (c.Since > 0 && c.Since < floor) {
		c.Reset, c.Upto = true, c.Since
		return nil
	}
	rows, err := c.tx.QueryContext(ctx, `SELECT seq, sum(n) FROM (
		  SELECT updated_seq AS seq, count(*) AS n FROM items WHERE updated_seq > ?1 GROUP BY updated_seq
		  UNION ALL SELECT seq, count(*) FROM tombstones WHERE seq > ?1 GROUP BY seq
		  UNION ALL SELECT updated_seq, count(*) FROM libraries WHERE updated_seq > ?1 GROUP BY updated_seq
		  UNION ALL SELECT updated_seq, count(*) FROM user_state WHERE user_id = ?2 AND updated_seq > ?1 GROUP BY updated_seq
		  UNION ALL SELECT updated_seq, count(*) FROM user_list WHERE user_id = ?2 AND updated_seq > ?1 GROUP BY updated_seq
		  UNION ALL SELECT updated_seq, count(*) FROM series_prefs WHERE user_id = ?2 AND updated_seq > ?1 GROUP BY updated_seq)
		GROUP BY seq ORDER BY seq`, c.Since, c.user)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Upto = c.Latest
	total := 0
	for rows.Next() {
		var seq int64
		var n int
		if err := rows.Scan(&seq, &n); err != nil {
			return err
		}
		if total > 0 && total+n > limit {
			// Everything between the last value taken and this one is
			// what other users changed: the page reaches up to it.
			c.Upto = seq - 1
			break
		}
		total += n
	}
	return rows.Err()
}

// Close ends the page's read transaction.
func (c *Changes) Close() error { return c.tx.Rollback() }

// typeRank puts a parent before its children.
const typeRank = `CASE i.type WHEN 'season' THEN 1 WHEN 'episode' THEN 2 ELSE 0 END`

// Items calls fn with the card of every item of the page: films, series
// and collections, then seasons, then episodes, each in sequence order.
// The series and the season of an episode or a season on the page that
// changed later come with it, so that a page applied on its own never
// holds an episode without its season; they come again at their own
// value.
func (c *Changes) Items(ctx context.Context, fn func(Card) error) error {
	if c.Reset {
		return nil
	}
	rows, err := c.tx.QueryContext(ctx, `
		WITH later(id) AS (
		  SELECT a.id FROM items i JOIN items a ON a.id IN (i.parent_id, i.series_id)
		  WHERE i.updated_seq > ?1 AND i.updated_seq <= ?2 AND a.updated_seq > ?2)
		SELECT `+cardColumns+` FROM items i
		WHERE (i.updated_seq > ?1 AND i.updated_seq <= ?2) OR i.id IN (SELECT id FROM later)
		ORDER BY `+typeRank+`, i.updated_seq, i.id`, c.Since, c.Upto)
	if err != nil {
		return fmt.Errorf("db: read changed items: %w", err)
	}
	defer rows.Close()
	return eachCard(rows, fn)
}

// eachCard calls fn with each card of a query of cardColumns, one at a
// time.
func eachCard(rows *sql.Rows, fn func(Card) error) error {
	for rows.Next() {
		c, err := scanCard(rows, nil)
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ChangedState is a row of a user's watch state, with its sequence value.
type ChangedState struct {
	State
	Seq int64
}

// States calls fn with every row of the user's watch state on the page.
func (c *Changes) States(ctx context.Context, fn func(ChangedState) error) error {
	if c.Reset {
		return nil
	}
	rows, err := c.tx.QueryContext(ctx, `SELECT item_id, position_ms, played, play_count, last_played_at, updated_seq
		FROM user_state WHERE user_id = ? AND updated_seq > ? AND updated_seq <= ? ORDER BY updated_seq, item_id`, c.user, c.Since, c.Upto)
	if err != nil {
		return fmt.Errorf("db: read changed watch state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s ChangedState
		var last sql.NullInt64
		if err := rows.Scan(&s.ItemID, &s.PositionMS, &s.Played, &s.PlayCount, &last, &s.Seq); err != nil {
			return err
		}
		s.LastPlayedAt = last.Int64
		if err := fn(s); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ChangedPrefs is a series' track choice, with its sequence value. One
// without a SubMode was reset.
type ChangedPrefs struct {
	SeriesID int64
	Prefs
	Seq int64
}

// Prefs calls fn with every series' track choice of the user on the page.
func (c *Changes) Prefs(ctx context.Context, fn func(ChangedPrefs) error) error {
	if c.Reset {
		return nil
	}
	rows, err := c.tx.QueryContext(ctx, `SELECT series_id, audio_lang, sub_lang, sub_mode, updated_seq
		FROM series_prefs WHERE user_id = ? AND updated_seq > ? AND updated_seq <= ? ORDER BY updated_seq, series_id`, c.user, c.Since, c.Upto)
	if err != nil {
		return fmt.Errorf("db: read changed track choices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ChangedPrefs
		var audio, sub, mode sql.NullString
		if err := rows.Scan(&p.SeriesID, &audio, &sub, &mode, &p.Seq); err != nil {
			return err
		}
		p.AudioLang, p.SubLang, p.SubMode = audio.String, sub.String, mode.String
		if err := fn(p); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ChangedListing is a row of the user's list: ListedAt 0 was taken off.
type ChangedListing struct {
	ItemID, ListedAt, Seq int64
}

// List calls fn with every row of the user's list on the page.
func (c *Changes) List(ctx context.Context, fn func(ChangedListing) error) error {
	if c.Reset {
		return nil
	}
	rows, err := c.tx.QueryContext(ctx, `SELECT item_id, listed_at, updated_seq
		FROM user_list WHERE user_id = ? AND updated_seq > ? AND updated_seq <= ? ORDER BY updated_seq, item_id`, c.user, c.Since, c.Upto)
	if err != nil {
		return fmt.Errorf("db: read changes to My list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l ChangedListing
		var at sql.NullInt64
		if err := rows.Scan(&l.ItemID, &at, &l.Seq); err != nil {
			return err
		}
		l.ListedAt = at.Int64
		if err := fn(l); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Deleted calls fn with every item deleted on the page.
func (c *Changes) Deleted(ctx context.Context, fn func(itemID int64) error) error {
	if c.Reset {
		return nil
	}
	rows, err := c.tx.QueryContext(ctx, `SELECT item_id FROM tombstones WHERE seq > ? AND seq <= ? ORDER BY seq, item_id`, c.Since, c.Upto)
	if err != nil {
		return fmt.Errorf("db: read deletions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if err := fn(id); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ChangedLibrary is a library on the page, with its roots' availability.
type ChangedLibrary struct {
	ID         int64
	Name, Kind string
	Roots      []RootState
	Seq        int64
}

// RootState is a library folder and whether it is there.
type RootState struct {
	ID        int64 `json:"id"`
	Available bool  `json:"available"`
}

// Libraries reads the libraries on the page, and the ids of every library
// that exists.
func (c *Changes) Libraries(ctx context.Context) (changed []ChangedLibrary, all []int64, err error) {
	rows, err := c.tx.QueryContext(ctx, `SELECT l.id, l.name, l.kind, l.updated_seq,
		  (SELECT json_group_array(json_object('id', r.id, 'available', json(CASE r.available WHEN 1 THEN 'true' ELSE 'false' END)))
		   FROM (SELECT id, available FROM roots WHERE library_id = l.id ORDER BY id) r)
		FROM libraries l ORDER BY l.id`)
	if err != nil {
		return nil, nil, fmt.Errorf("db: read libraries: %w", err)
	}
	defer rows.Close()
	all = []int64{}
	for rows.Next() {
		var l ChangedLibrary
		var roots string
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &l.Seq, &roots); err != nil {
			return nil, nil, err
		}
		all = append(all, l.ID)
		if c.Reset || l.Seq <= c.Since || l.Seq > c.Upto {
			continue
		}
		if err := json.Unmarshal([]byte(roots), &l.Roots); err != nil {
			return nil, nil, fmt.Errorf("db: read the roots of library %d: %w", l.ID, err)
		}
		changed = append(changed, l)
	}
	return changed, all, rows.Err()
}

// PruneAfter is how long a delete marker is kept (docs/03-data-model.md,
// section 5).
const PruneAfter = 90 * 24 * 60 * 60 * 1000 // ms

// Pruned is what one pruning removed.
type Pruned struct {
	Tombstones, Listings, Prefs int
	Floor                       int64
}

// Prune deletes the delete markers older than PruneAfter, moves
// meta.tombstone_floor up to the highest value it removed, and deletes the
// rows of My list and of track choices that only said "taken off" or
// "reset" at or below the floor: a client that has not seen them starts
// again from a full copy (docs/03-data-model.md, sections 4 and 5).
func (d *DB) Prune(ctx context.Context, now int64) (Pruned, error) {
	var p Pruned
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		p = Pruned{}
		var highest sql.NullInt64
		if err := tx.tx.QueryRowContext(ctx, `SELECT max(seq) FROM tombstones WHERE at < ?`, now-PruneAfter).Scan(&highest); err != nil {
			return err
		}
		var floor int64
		if err := tx.tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'tombstone_floor'`).Scan(&floor); err != nil {
			return err
		}
		if highest.Valid && highest.Int64 > floor {
			res, err := tx.tx.ExecContext(ctx, `DELETE FROM tombstones WHERE seq <= ?`, highest.Int64)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			p.Tombstones = int(n)
			floor = highest.Int64
			if _, err := tx.tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = 'tombstone_floor'`, strconv.FormatInt(floor, 10)); err != nil {
				return err
			}
		}
		p.Floor = floor
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM user_list WHERE listed_at IS NULL AND updated_seq <= ?`, floor)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		p.Listings = int(n)
		res, err = tx.tx.ExecContext(ctx, `DELETE FROM series_prefs
			WHERE audio_lang IS NULL AND sub_lang IS NULL AND sub_mode IS NULL AND updated_seq <= ?`, floor)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		p.Prefs = int(n)
		return nil
	})
	if err != nil {
		return Pruned{}, fmt.Errorf("db: prune: %w", err)
	}
	return p, nil
}
