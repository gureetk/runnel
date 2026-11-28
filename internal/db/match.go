// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MatchTarget is a film or a series as matching reads it.
type MatchTarget struct {
	ID, LibraryID int64
	Type          string // movie or series
	Title         string
	Year          int // 0 when its name gives none
	State         string
	Profile       string // its library's: default or anime
	// Searchable is whether a search may match it: unmatched or on the
	// review list, without a provider's id of its title and without a
	// manual match or one left unmatched for its folder.
	Searchable bool
}

// searchable is true for an item that matching may search for
// (docs/06-library-and-metadata.md, section 6.1): a film or a series that
// is unmatched or on the review list, whose names gave no id, that the
// import gave none, and whose folder has no row in overrides.
const searchable = `i.type IN ('movie', 'series') AND i.match_state IN ('unmatched', 'review')
	AND NOT EXISTS (SELECT 1 FROM external_ids e WHERE e.item_id = i.id AND e.provider IN ('tmdb', 'imdb', 'tvdb'))
	AND NOT EXISTS (SELECT 1 FROM overrides o WHERE o.root_id = i.root_id AND o.path_key = i.path_key)`

// MatchTargetOf reads an item for its search, or ErrNotFound.
func (d *DB) MatchTargetOf(ctx context.Context, id int64) (MatchTarget, error) {
	t := MatchTarget{ID: id}
	var year sql.NullInt64
	err := d.r.QueryRowContext(ctx, `SELECT i.library_id, i.type, i.title, i.year, i.match_state, l.profile,
		CASE WHEN `+searchable+` THEN 1 ELSE 0 END
		FROM items i JOIN libraries l ON l.id = i.library_id WHERE i.id = ?`, id).
		Scan(&t.LibraryID, &t.Type, &t.Title, &year, &t.State, &t.Profile, &t.Searchable)
	if errors.Is(err, sql.ErrNoRows) {
		return MatchTarget{}, ErrNotFound
	}
	if err != nil {
		return MatchTarget{}, fmt.Errorf("db: read an item to match: %w", err)
	}
	t.Year = int(year.Int64)
	return t, nil
}

// needsMatch is true for an item that may be searched for, of a library or
// of every library with ?2 = 0, that was never searched for, or was
// searched under another title or year, or with ?3 set more than ?4 ago:
// match_note keeps the title, the year and the time of its last search.
const needsMatch = searchable + ` AND (?2 = 0 OR i.library_id = ?2)
	AND (i.match_note IS NULL
	  OR json_extract(i.match_note, '$.title') IS NOT i.title
	  OR json_extract(i.match_note, '$.year') IS NOT i.year
	  OR (?3 AND COALESCE(json_extract(i.match_note, '$.at'), 0) < ?4))`

// QueueMatches queues a search for every film and series of a library, or
// of every library with 0, that needs one (needsMatch). A search that runs
// is left as it is, and so is one queued or failed, unless again is set:
// then it runs as soon as it may, and what was searched for before since
// more than every ago is searched for again. It returns how many it
// queued.
func (d *DB) QueueMatches(ctx context.Context, libraryID int64, again bool, every, now int64) (int, error) {
	states := `'done'`
	if again {
		states = `'done', 'failed', 'queued'`
	}
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		r, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, created_at)
			 SELECT 'match', 'item:' || i.id, i.library_id, ?1 FROM items i WHERE `+needsMatch+`
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = 0, error = NULL, payload = NULL
			 WHERE tasks.state IN (`+states+`)`, now, libraryID, again, now-every)
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("db: queue searches: %w", err)
	}
	return int(n), nil
}

// QueueMatch queues the search for one item, which asks the provider
// again with force, as an item's refresh does. One that runs is left to
// finish.
func (d *DB) QueueMatch(ctx context.Context, itemID, libraryID int64, force bool, now int64) error {
	var payload any
	if force {
		payload = `{"force":true}`
	}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, payload, created_at) VALUES ('match', ?, ?, ?, ?)
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = 0, error = NULL,
			   payload = COALESCE(excluded.payload, tasks.payload)
			 WHERE tasks.state <> 'running'`, ItemKey(itemID), libraryID, payload, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: queue a search: %w", err)
	}
	return nil
}

// SearchOutcome is what a search found for an item: a match it takes, or
// why it takes none.
type SearchOutcome struct {
	Provider, Value string  // the match; "" when none is taken
	Score           float64 // the best candidate's score; 0 without one
	Candidates      int
	Note            string // match_note: JSON with why, the title, the year and when
}

// ApplySearch writes a search's outcome onto the item searched for as
// title and year, unless it may no longer be searched for, or its title
// or year changed meanwhile. A match makes it matched with its score as
// the confidence, the provider's id written; otherwise it is on the review
// list with candidates, or unmatched without, and the note says why. The
// item is stamped when its state or confidence changed. It reports
// whether the item was matched.
func (d *DB) ApplySearch(ctx context.Context, id int64, title string, year int, out SearchOutcome) (bool, error) {
	matched := false
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var ok bool
		var state string
		var conf sql.NullFloat64
		err := tx.tx.QueryRowContext(ctx, `SELECT `+searchable+` AND i.title = ? AND i.year IS ?, i.match_state, i.match_confidence
			FROM items i WHERE i.id = ?`, title, nullInt(int64(year)), id).Scan(&ok, &state, &conf)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil || !ok {
			return err
		}
		if out.Value != "" {
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, ?, ?)
				ON CONFLICT (item_id, provider) DO UPDATE SET value = excluded.value`, id, out.Provider, out.Value); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'matched', match_confidence = ?, match_note = NULL,
				refreshed_at = NULL WHERE id = ?`, out.Score, id); err != nil {
				return err
			}
			matched = true
			return tx.stampItems(ctx, []int64{id})
		}
		next := "unmatched"
		var score any
		if out.Candidates > 0 {
			next, score = "review", out.Score
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = ?, match_confidence = ?, match_note = ? WHERE id = ?`,
			next, score, out.Note, id); err != nil {
			return err
		}
		if next == state && (out.Candidates > 0) == conf.Valid && (!conf.Valid || conf.Float64 == out.Score) {
			return nil
		}
		return tx.stampItems(ctx, []int64{id})
	})
	if err != nil {
		return false, fmt.Errorf("db: write the search for item %d: %w", id, err)
	}
	return matched, nil
}
