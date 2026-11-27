// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// State is one user's watch state of a film or an episode
// (docs/03-data-model.md, section 4).
type State struct {
	ItemID       int64
	PositionMS   int64 // from the start of the item, across its parts
	Played       bool
	PlayCount    int
	LastPlayedAt int64 // 0 when never
}

// KeptFromMS is the first position that a progress report keeps. Below it
// an item counts as not started (docs/04-api.md, section 7.6; P3).
const KeptFromMS = 30_000

// playedTenths is how far into its runtime a stop marks an item played:
// nine tenths, 90% (P3).
const playedTenths = 9

var (
	// ErrNoState is returned for an item that is not a film or an episode:
	// only those have a watch state.
	ErrNoState = errors.New("db: only films and episodes have a watch state")
	// ErrWrongFile is returned when a report's file is not one of its
	// item's files.
	ErrWrongFile = errors.New("db: the file is not one of the item's")
)

// querier is a connection or a transaction to read with.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// StatesOf reads a user's states of some items. An item without a row was
// never started, and is absent from the map.
func (d *DB) StatesOf(ctx context.Context, userID int64, ids []int64) (map[int64]State, error) {
	out, err := statesOf(ctx, d.r, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("db: read watch state: %w", err)
	}
	return out, nil
}

func statesOf(ctx context.Context, q querier, userID int64, ids []int64) (map[int64]State, error) {
	out := map[int64]State{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT item_id, position_ms, played, play_count, last_played_at FROM user_state
		WHERE user_id = ? AND item_id IN (SELECT value FROM json_each(?))`, userID, string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s State
		var last sql.NullInt64
		if err := rows.Scan(&s.ItemID, &s.PositionMS, &s.Played, &s.PlayCount, &last); err != nil {
			return nil, err
		}
		s.LastPlayedAt = last.Int64
		out[s.ItemID] = s
	}
	return out, rows.Err()
}

// stateOr is the stored state of an item, or the state of one never
// started.
func stateOr(states map[int64]State, id int64) State {
	if s, ok := states[id]; ok {
		return s
	}
	return State{ItemID: id}
}

// putStates writes states, each row stamped with this transaction's
// sequence values (docs/03-data-model.md, section 5). A file of 0 keeps
// the file a row has.
func (t *Tx) putStates(ctx context.Context, userID int64, states []State, fileID int64) error {
	if len(states) == 0 {
		return nil
	}
	stmt, err := t.tx.PrepareContext(ctx, `INSERT INTO user_state
		  (user_id, item_id, position_ms, played, play_count, last_played_at, file_id, updated_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, item_id) DO UPDATE SET position_ms = excluded.position_ms, played = excluded.played,
		  play_count = excluded.play_count, last_played_at = excluded.last_played_at,
		  file_id = COALESCE(excluded.file_id, user_state.file_id), updated_seq = excluded.updated_seq`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range states {
		seq, err := t.Seq(ctx)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, userID, s.ItemID, s.PositionMS, s.Played, s.PlayCount,
			nullInt(s.LastPlayedAt), nullInt(fileID), seq); err != nil {
			return err
		}
	}
	return nil
}

// Progress is one progress report of a playback (docs/04-api.md,
// section 7.6).
type Progress struct {
	ItemID, FileID int64
	PositionMS     int64 // from the start of the item, across its parts
	Stopped        bool
	At             int64
}

// SaveProgress applies a report to the user's state of its item, and
// reports whether the state changed. It returns ErrNotFound when there is
// no such item, ErrNoState when the item is not a film or an episode, and
// ErrWrongFile when the file is not one of the item's.
func (d *DB) SaveProgress(ctx context.Context, userID int64, p Progress) (bool, error) {
	changed := false
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var typ string
		err := tx.tx.QueryRowContext(ctx, `SELECT type FROM items WHERE id = ?`, p.ItemID).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if typ != "movie" && typ != "episode" {
			return ErrNoState
		}
		var label sql.NullString
		err = tx.tx.QueryRowContext(ctx, `SELECT label FROM item_files WHERE item_id = ? AND file_id = ?`, p.ItemID, p.FileID).Scan(&label)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWrongFile
		}
		if err != nil {
			return err
		}
		// The runtime is the played version's: the file, or the parts that
		// share its label. It is not known while one of them has no
		// duration.
		var files, known int
		var runtime int64
		if err := tx.tx.QueryRowContext(ctx, `SELECT count(*), count(f.duration_ms), COALESCE(sum(f.duration_ms), 0)
			FROM item_files l JOIN files f ON f.id = l.file_id WHERE l.item_id = ? AND l.label IS ?`,
			p.ItemID, label).Scan(&files, &known, &runtime); err != nil {
			return err
		}
		if known < files {
			runtime = 0
		}
		cur, err := statesOf(ctx, tx.tx, userID, []int64{p.ItemID})
		if err != nil {
			return err
		}
		before := stateOr(cur, p.ItemID)
		after, withFile := progressed(before, p, runtime)
		if after == before {
			return nil
		}
		changed = true
		file := int64(0)
		if withFile {
			file = p.FileID
		}
		return tx.putStates(ctx, userID, []State{after}, file)
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoState) || errors.Is(err, ErrWrongFile) {
			return false, err
		}
		return false, fmt.Errorf("db: save progress: %w", err)
	}
	return changed, nil
}

// progressed is a state after a report, by the rules of docs/04-api.md,
// section 7.6, and whether the report's file becomes the state's file. A
// runtime of 0 is not known.
func progressed(cur State, p Progress, runtime int64) (State, bool) {
	next := cur
	switch {
	case p.PositionMS < KeptFromMS:
		next.PositionMS = 0
		return next, false
	case p.Stopped && runtime > 0 && p.PositionMS*10 >= runtime*playedTenths:
		next.Played, next.PositionMS, next.PlayCount, next.LastPlayedAt = true, 0, cur.PlayCount+1, p.At
	default:
		next.Played, next.PositionMS, next.LastPlayedAt = false, p.PositionMS, p.At
	}
	return next, true
}

// MarkPlayed marks played, or unplayed, every film and episode that ids
// reach (docs/04-api.md, section 7.7): a film or an episode itself, a
// season's episodes, a series' episodes with its specials, a collection's
// films. An id that no item has is skipped. It returns the states it
// replaced, in the order it reached them, for Undo.
func (d *DB) MarkPlayed(ctx context.Context, userID int64, ids []int64, played bool, at int64) ([]State, error) {
	var prev []State
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		prev = nil
		reached, err := tx.reach(ctx, ids)
		if err != nil {
			return err
		}
		cur, err := statesOf(ctx, tx.tx, userID, reached)
		if err != nil {
			return err
		}
		var next []State
		for _, id := range reached {
			c := stateOr(cur, id)
			n := c
			if played {
				if c.Played {
					continue
				}
				n.Played, n.PositionMS, n.PlayCount, n.LastPlayedAt = true, 0, max(c.PlayCount, 1), at
			} else {
				if !c.Played && c.PositionMS == 0 {
					continue
				}
				n.Played, n.PositionMS = false, 0
			}
			prev = append(prev, c)
			next = append(next, n)
		}
		return tx.putStates(ctx, userID, next, 0)
	})
	if err != nil {
		return nil, fmt.Errorf("db: mark played: %w", err)
	}
	return prev, nil
}

// reach lists, once each, the films and episodes that ids stand for: in
// the order of ids, and the episodes of a season or a series in their
// order, specials last, the films of a collection by release date.
func (t *Tx) reach(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		WITH asked(ord, id) AS (SELECT key, value FROM json_each(?)),
		reached(ord, id) AS (
		  SELECT a.ord, i.id FROM asked a JOIN items i ON i.id = a.id WHERE i.type IN ('movie', 'episode')
		  UNION ALL
		  SELECT a.ord, i.id FROM asked a JOIN items c ON c.id = a.id AND c.type = 'season'
		    JOIN items i ON i.parent_id = c.id AND i.type = 'episode'
		  UNION ALL
		  SELECT a.ord, i.id FROM asked a JOIN items c ON c.id = a.id AND c.type = 'series'
		    JOIN items i ON i.series_id = c.id AND i.type = 'episode'
		  UNION ALL
		  SELECT a.ord, i.id FROM asked a JOIN items c ON c.id = a.id AND c.type = 'collection'
		    JOIN items i ON i.collection_id = c.id AND i.type = 'movie')
		SELECT r.id FROM reached r JOIN items i ON i.id = r.id
		ORDER BY r.ord, i.season_number = 0, i.season_number, `+episodePlace("i")+`, COALESCE(i.absolute_number, 0),
		  i.release_date, i.sort_title, i.id`, string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	seen := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// RestoreStates writes states as they are given, for Undo
// (docs/04-api.md, section 7.7). The state of an item that no longer
// exists is skipped, and one that equals what is stored is not written. A
// later state of the same item wins. ErrNoState: a state is for an item
// that is not a film or an episode.
func (d *DB) RestoreStates(ctx context.Context, userID int64, states []State) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		last := map[int64]int{}
		var ids []int64
		for i, s := range states {
			if _, ok := last[s.ItemID]; !ok {
				ids = append(ids, s.ItemID)
			}
			last[s.ItemID] = i
		}
		types, err := tx.typesOf(ctx, ids)
		if err != nil {
			return err
		}
		cur, err := statesOf(ctx, tx.tx, userID, ids)
		if err != nil {
			return err
		}
		var next []State
		for _, id := range ids {
			typ, ok := types[id]
			if !ok {
				continue
			}
			if typ != "movie" && typ != "episode" {
				return ErrNoState
			}
			if s := states[last[id]]; s != stateOr(cur, id) {
				next = append(next, s)
			}
		}
		return tx.putStates(ctx, userID, next, 0)
	})
	if err != nil {
		if errors.Is(err, ErrNoState) {
			return err
		}
		return fmt.Errorf("db: restore watch state: %w", err)
	}
	return nil
}

// typesOf reads the type of each item of ids that exists.
func (t *Tx) typesOf(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `SELECT id, type FROM items WHERE id IN (SELECT value FROM json_each(?))`, string(list))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var typ string
		if err := rows.Scan(&id, &typ); err != nil {
			return nil, err
		}
		out[id] = typ
	}
	return out, rows.Err()
}

// Unplayed counts the episodes that a user has not played: for each
// series of series, those outside the specials, and for each season of
// seasons, all of its own (docs/04-api.md, section 2.1). An id that is
// missing from the map has none.
func (d *DB) Unplayed(ctx context.Context, userID int64, series, seasons []int64) (map[int64]int, error) {
	out := map[int64]int{}
	for _, q := range []struct {
		ids   []int64
		query string
	}{
		{series, `SELECT e.series_id, count(*) FROM items e
			WHERE e.series_id IN (SELECT value FROM json_each(?)) AND e.type = 'episode' AND e.season_number > 0
			  AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = ? AND s.item_id = e.id AND s.played = 1)
			GROUP BY e.series_id`},
		{seasons, `SELECT e.parent_id, count(*) FROM items e
			WHERE e.parent_id IN (SELECT value FROM json_each(?)) AND e.type = 'episode'
			  AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = ? AND s.item_id = e.id AND s.played = 1)
			GROUP BY e.parent_id`},
	} {
		if len(q.ids) == 0 {
			continue
		}
		list, err := json.Marshal(q.ids)
		if err != nil {
			return nil, err
		}
		if err := d.countInto(ctx, out, q.query, string(list), userID); err != nil {
			return nil, fmt.Errorf("db: count unplayed: %w", err)
		}
	}
	return out, nil
}

// UnplayedByLibrary counts, for each library, the films a user has not
// played and the series with an episode the user has not played outside
// the specials (docs/04-api.md, section 4.1).
func (d *DB) UnplayedByLibrary(ctx context.Context, userID int64) (map[int64]int, error) {
	out := map[int64]int{}
	err := d.countInto(ctx, out, `SELECT i.library_id, count(*) FROM items i
		WHERE (i.type = 'movie'
		       AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = ?1 AND s.item_id = i.id AND s.played = 1))
		   OR (i.type = 'series'
		       AND EXISTS (SELECT 1 FROM items e WHERE e.series_id = i.id AND e.type = 'episode' AND e.season_number > 0
		                   AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = ?1 AND s.item_id = e.id AND s.played = 1)))
		GROUP BY i.library_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: count unplayed: %w", err)
	}
	return out, nil
}

// countInto runs a query of (id, count) rows into out.
func (d *DB) countInto(ctx context.Context, out map[int64]int, query string, args ...any) error {
	rows, err := d.r.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		out[id] = n
	}
	return rows.Err()
}

// Resume is where a series' Play button starts (docs/04-api.md,
// section 2.2).
type Resume struct {
	ID              int64
	Season, Episode int
	Unnumbered      bool  // an episode without a number (D51): Episode is 0
	PositionMS      int64 // 0 unless the episode is in progress
}

// ResumeOf finds where a user's Play on a series starts: the episode in
// progress, the one played last if several are; else the first episode
// after the one played most recently that is neither played nor started,
// specials skipped, as Next up finds it (docs/03-data-model.md, section
// 6); else the first episode. False when the series has no episode.
func (d *DB) ResumeOf(ctx context.Context, userID, seriesID int64) (Resume, bool, error) {
	var r Resume
	for _, q := range []struct {
		query    string
		position bool
	}{
		{`SELECT e.id, e.season_number, COALESCE(e.episode_number, 0), e.episode_number IS NULL, s.position_ms FROM user_state s JOIN items e ON e.id = s.item_id
			WHERE s.user_id = ?1 AND e.series_id = ?2 AND e.type = 'episode' AND s.played = 0 AND s.position_ms > 0
			ORDER BY s.last_played_at DESC, e.season_number DESC, ` + episodePlace("e") + ` DESC LIMIT 1`, true},
		{`WITH last(s, n) AS (
			  SELECT e.season_number, e.episode_number FROM user_state us JOIN items e ON e.id = us.item_id
			  WHERE us.user_id = ?1 AND e.series_id = ?2 AND e.type = 'episode' AND us.played = 1 AND e.episode_number IS NOT NULL
			  ORDER BY us.last_played_at DESC, e.season_number DESC, e.episode_number DESC LIMIT 1)
			SELECT c.id, c.season_number, c.episode_number, 0 FROM items c
			WHERE c.series_id = ?2 AND c.type = 'episode' AND c.season_number > 0 AND c.episode_number IS NOT NULL
			  AND (c.season_number, c.episode_number) > (SELECT COALESCE((SELECT s FROM last), 0), COALESCE((SELECT n FROM last), 0))
			  AND NOT EXISTS (SELECT 1 FROM user_state u WHERE u.user_id = ?1 AND u.item_id = c.id AND (u.played = 1 OR u.position_ms > 0))
			ORDER BY c.season_number, c.episode_number, COALESCE(c.absolute_number, 0), c.id LIMIT 1`, false},
		{`SELECT e.id, e.season_number, COALESCE(e.episode_number, 0), e.episode_number IS NULL FROM items e
			WHERE e.series_id = ?2 AND e.type = 'episode'
			ORDER BY e.season_number = 0, e.season_number, ` + episodePlace("e") + `, COALESCE(e.absolute_number, 0), e.sort_title, e.id LIMIT 1`, false},
	} {
		row := d.r.QueryRowContext(ctx, q.query, userID, seriesID)
		var err error
		if q.position {
			err = row.Scan(&r.ID, &r.Season, &r.Episode, &r.Unnumbered, &r.PositionMS)
		} else {
			err = row.Scan(&r.ID, &r.Season, &r.Episode, &r.Unnumbered)
		}
		if err == nil {
			return r, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Resume{}, false, fmt.Errorf("db: find where a series resumes: %w", err)
		}
	}
	return Resume{}, false, nil
}
