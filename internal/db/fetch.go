// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// ItemKey is a task's key for an item.
func ItemKey(itemID int64) string { return "item:" + strconv.FormatInt(itemID, 10) }

// ItemOfKey reads the item id back from a task's key, or 0.
func ItemOfKey(key string) int64 {
	if len(key) < 6 || key[:5] != "item:" {
		return 0
	}
	id, _ := strconv.ParseInt(key[5:], 10, 64)
	return id
}

// FetchTarget is a film or a series as fetching its metadata reads it.
type FetchTarget struct {
	ID, LibraryID int64
	Type          string // movie or series
	MatchState    string
	Numbering     string // a series': seasons or absolute
	Airing        bool
	IDs           map[string]string
	Seasons       []int // a series': the season numbers it holds, in order
}

// FetchTargetOf reads an item for its fetch, or ErrNotFound.
func (d *DB) FetchTargetOf(ctx context.Context, id int64) (FetchTarget, error) {
	t := FetchTarget{ID: id, IDs: map[string]string{}}
	var numbering sql.NullString
	var airing sql.NullInt64
	err := d.r.QueryRowContext(ctx, `SELECT library_id, type, match_state, numbering, airing FROM items WHERE id = ?`, id).
		Scan(&t.LibraryID, &t.Type, &t.MatchState, &numbering, &airing)
	if errors.Is(err, sql.ErrNoRows) {
		return FetchTarget{}, ErrNotFound
	}
	if err != nil {
		return FetchTarget{}, fmt.Errorf("db: read an item to fetch: %w", err)
	}
	t.Numbering, t.Airing = numbering.String, airing.Int64 == 1
	rows, err := d.r.QueryContext(ctx, `SELECT provider, value FROM external_ids WHERE item_id = ?`, id)
	if err != nil {
		return FetchTarget{}, fmt.Errorf("db: read an item's ids: %w", err)
	}
	for rows.Next() {
		var p, v string
		if err := rows.Scan(&p, &v); err != nil {
			rows.Close()
			return FetchTarget{}, err
		}
		t.IDs[p] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FetchTarget{}, err
	}
	if t.Type != "series" {
		return t, nil
	}
	rows, err = d.r.QueryContext(ctx, `SELECT season_number FROM items WHERE parent_id = ? AND type = 'season' ORDER BY season_number`, id)
	if err != nil {
		return FetchTarget{}, fmt.Errorf("db: read a series' seasons: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return FetchTarget{}, err
		}
		t.Seasons = append(t.Seasons, n)
	}
	return t, rows.Err()
}

// needsFetch is true for a matched film or series, of a library or of
// every library with ?2 = 0, that has never been fetched, was fetched
// before ?3, or before ?4 while it airs, or is a series with a season or a
// numbered episode not fetched yet, outside a running count's. ?1 is now.
const needsFetch = `i.type IN ('movie', 'series') AND i.match_state IN ('matched', 'manual') AND (?2 = 0 OR i.library_id = ?2)
	AND (i.refreshed_at IS NULL
	  OR i.refreshed_at < CASE WHEN i.airing = 1 THEN ?4 ELSE ?3 END
	  OR (i.type = 'series' AND COALESCE(i.numbering, 'seasons') <> 'absolute' AND EXISTS (SELECT 1 FROM items c
	        WHERE c.series_id = i.id AND c.refreshed_at IS NULL AND (c.type = 'season' OR c.episode_number IS NOT NULL))))`

// QueueFetches queues a fetch for every film and series of a library, or
// of every library with 0, that needs one: matched, and never fetched,
// fetched more than every ago, or more than airing ago while it airs, or a
// series with a season or an episode that is not fetched yet
// (docs/06-library-and-metadata.md, section 6.3). A fetch that runs is
// left as it is. One that is queued, or failed, is too, unless again is
// set: then it runs as soon as it may, its attempts counted afresh, as
// after a new key. It returns how many it queued.
func (d *DB) QueueFetches(ctx context.Context, libraryID int64, every, airing int64, again bool, now int64) (int, error) {
	states := `'done'`
	if again {
		states = `'done', 'failed', 'queued'`
	}
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		r, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, created_at)
			 SELECT 'fetch', 'item:' || i.id, i.library_id, ?1 FROM items i WHERE `+needsFetch+`
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = 0, error = NULL, payload = NULL
			 WHERE tasks.state IN (`+states+`)`, now, libraryID, now-every, now-airing)
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("db: queue fetches: %w", err)
	}
	return int(n), nil
}

// QueueFetch queues the fetch of one item, which asks the provider again
// with force: an item's refresh (docs/04-api.md, section 8.4). A fetch
// queued already takes force; one that runs is left to finish.
func (d *DB) QueueFetch(ctx context.Context, itemID, libraryID int64, force bool, now int64) error {
	var payload any
	if force {
		payload = `{"force":true}`
	}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, payload, created_at) VALUES ('fetch', ?, ?, ?, ?)
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = 0, error = NULL,
			   payload = COALESCE(excluded.payload, tasks.payload)
			 WHERE tasks.state <> 'running'`, ItemKey(itemID), libraryID, payload, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: queue a fetch: %w", err)
	}
	return nil
}

// SetMatchID writes a provider's id onto a matched item, as fetching finds
// TMDB's id for another provider's, and stamps it.
func (d *DB) SetMatchID(ctx context.Context, id int64, provider, value string) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, ?, ?)
			ON CONFLICT (item_id, provider) DO UPDATE SET value = excluded.value`, id, provider, value); err != nil {
			return err
		}
		return tx.stampItems(ctx, []int64{id})
	})
	if err != nil {
		return fmt.Errorf("db: write an id of item %d: %w", id, err)
	}
	return nil
}

// FetchOwner is the film or the series that an item is, or belongs to,
// and its library: 0 for an item of another type.
func (d *DB) FetchOwner(ctx context.Context, id int64) (owner, libraryID int64, err error) {
	var typ string
	var series sql.NullInt64
	err = d.r.QueryRowContext(ctx, `SELECT type, series_id, library_id FROM items WHERE id = ?`, id).Scan(&typ, &series, &libraryID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("db: read an item: %w", err)
	}
	switch typ {
	case "movie", "series":
		return id, libraryID, nil
	case "season", "episode":
		return series.Int64, libraryID, nil
	}
	return 0, libraryID, nil
}

// MatchNote reads why an item is on the review list, as JSON, or "".
func (d *DB) MatchNote(ctx context.Context, id int64) (string, error) {
	var n sql.NullString
	err := d.r.QueryRowContext(ctx, `SELECT match_note FROM items WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return n.String, err
}

// MarkReview puts a matched item on the review list with why, as JSON in
// match_note, and stamps it: its ids name nothing at the provider.
func (d *DB) MarkReview(ctx context.Context, id int64, note string) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'review', match_confidence = NULL, match_note = ?
			WHERE id = ? AND match_state = 'matched'`, note, id); err != nil {
			return err
		}
		return tx.stampItems(ctx, []int64{id})
	})
	if err != nil {
		return fmt.Errorf("db: put item %d on the review list: %w", id, err)
	}
	return nil
}
