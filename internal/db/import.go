// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ErrNoItem is returned for a file that no film or episode plays.
var ErrNoItem = errors.New("db: no item plays the file")

// Joined is the item that a path of another server names here
// (docs/06-library-and-metadata.md, section 7).
type Joined struct {
	ItemID, FileID int64 // FileID is 0 for a series
	Type           string
	MatchState     string
}

// ItemOfFile finds the item that a file of a root plays. ErrNotFound: no
// file is at that path. ErrNoItem: the file plays no item, as an extra.
func (d *DB) ItemOfFile(ctx context.Context, rootID int64, rel string) (Joined, error) {
	var j Joined
	err := d.r.QueryRowContext(ctx, `SELECT id FROM files WHERE root_id = ? AND rel_path = ?`, rootID, rel).Scan(&j.FileID)
	if errors.Is(err, sql.ErrNoRows) {
		return Joined{}, ErrNotFound
	}
	if err != nil {
		return Joined{}, fmt.Errorf("db: find a file: %w", err)
	}
	err = d.r.QueryRowContext(ctx, `SELECT i.id, i.type, i.match_state FROM item_files x JOIN items i ON i.id = x.item_id
		WHERE x.file_id = ? ORDER BY i.id LIMIT 1`, j.FileID).Scan(&j.ItemID, &j.Type, &j.MatchState)
	if errors.Is(err, sql.ErrNoRows) {
		return Joined{}, ErrNoItem
	}
	if err != nil {
		return Joined{}, fmt.Errorf("db: find the item of a file: %w", err)
	}
	return j, nil
}

// SeriesOfFolder finds the series built from a folder of a root, or
// ErrNotFound.
func (d *DB) SeriesOfFolder(ctx context.Context, rootID int64, rel string) (Joined, error) {
	j := Joined{Type: "series"}
	err := d.r.QueryRowContext(ctx, `SELECT id, match_state FROM items WHERE root_id = ? AND path_key = ? AND type = 'series'`,
		rootID, rel).Scan(&j.ItemID, &j.MatchState)
	if errors.Is(err, sql.ErrNoRows) {
		return Joined{}, ErrNotFound
	}
	if err != nil {
		return Joined{}, fmt.Errorf("db: find a series: %w", err)
	}
	return j, nil
}

// ImportMatch is the provider ids that another server has for a film or a
// series here, by the provider names of docs/03-data-model.md, section 3.
type ImportMatch struct {
	ItemID int64
	IDs    map[string]string
}

// ImportMatches writes provider ids and makes the items matched, with write
// set, and returns how many items it changed, or would change without it.
// An item matched by hand, or left unmatched on purpose, is left as it is,
// and so is one that already has these ids and is matched. A changed item
// is stamped, since its detail changed (docs/03-data-model.md, section 5).
func (d *DB) ImportMatches(ctx context.Context, in []ImportMatch, write bool) (int, error) {
	changed := func(q querier) ([]ImportMatch, error) {
		var out []ImportMatch
		for _, m := range in {
			rows, err := q.QueryContext(ctx, `SELECT i.match_state, e.provider, e.value FROM items i
				LEFT JOIN external_ids e ON e.item_id = i.id WHERE i.id = ?`, m.ItemID)
			if err != nil {
				return nil, err
			}
			state, have := "", map[string]string{}
			found := false
			for rows.Next() {
				var provider, value sql.NullString
				if err := rows.Scan(&state, &provider, &value); err != nil {
					rows.Close()
					return nil, err
				}
				found = true
				if provider.Valid {
					have[provider.String] = value.String
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if !found || state == "manual" || state == "ignored" || len(m.IDs) == 0 {
				continue
			}
			same := state == "matched"
			for p, v := range m.IDs {
				same = same && have[p] == v
			}
			if !same {
				out = append(out, m)
			}
		}
		return out, nil
	}
	if !write {
		out, err := changed(d.r)
		if err != nil {
			return 0, fmt.Errorf("db: read matches: %w", err)
		}
		return len(out), nil
	}
	n := 0
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		out, err := changed(tx.tx)
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(out))
		for _, m := range out {
			// A new TMDB id is another title: what was fetched for the old
			// one is fetched again.
			if v, ok := m.IDs["tmdb"]; ok {
				if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET refreshed_at = NULL WHERE id = ?
					AND (SELECT value FROM external_ids WHERE item_id = ? AND provider = 'tmdb') IS NOT ?`, m.ItemID, m.ItemID, v); err != nil {
					return err
				}
			}
			for _, p := range slices.Sorted(maps.Keys(m.IDs)) {
				if _, err := tx.tx.ExecContext(ctx, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, ?, ?)
					ON CONFLICT (item_id, provider) DO UPDATE SET value = excluded.value`, m.ItemID, p, m.IDs[p]); err != nil {
					return err
				}
			}
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'matched', match_confidence = NULL, match_note = NULL WHERE id = ?`, m.ItemID); err != nil {
				return err
			}
			ids = append(ids, m.ItemID)
		}
		n = len(ids)
		return tx.stampItems(ctx, ids)
	})
	if err != nil {
		return 0, fmt.Errorf("db: write matches: %w", err)
	}
	return n, nil
}

// ImportState is another server's play state of a film or an episode for
// one user here.
type ImportState struct {
	UserID, ItemID int64
	FileID         int64 // the file the position is in
	Played         bool
	PlayCount      int
	LastPlayedAt   int64 // 0 when never
	PositionMS     int64
}

// ImportStates writes play states, with write set, and returns how many it
// wrote, or would write without it. A state that says nothing (unplayed,
// never played, no position) is left out. So is one where the state here
// was played later, and one that would change nothing: the import is safe
// to repeat (docs/06-library-and-metadata.md, section 7). A played item
// has no position, and the play count is the greater of the two.
func (d *DB) ImportStates(ctx context.Context, in []ImportState, write bool) (int, error) {
	byUser := map[int64][]ImportState{}
	for _, s := range in {
		if s.Played || s.PlayCount > 0 || s.LastPlayedAt > 0 || s.PositionMS > 0 {
			byUser[s.UserID] = append(byUser[s.UserID], s)
		}
	}
	type row struct {
		user, file int64
		state      State
	}
	decide := func(q querier) ([]row, error) {
		var out []row
		for _, user := range slices.Sorted(maps.Keys(byUser)) {
			list := byUser[user]
			ids := make([]int64, len(list))
			for i, s := range list {
				ids[i] = s.ItemID
			}
			have, err := statesOf(ctx, q, user, ids)
			if err != nil {
				return nil, err
			}
			for _, s := range list {
				here, ok := have[s.ItemID]
				if ok && here.LastPlayedAt > s.LastPlayedAt {
					continue
				}
				next := State{ItemID: s.ItemID, Played: s.Played, PlayCount: max(here.PlayCount, s.PlayCount),
					LastPlayedAt: s.LastPlayedAt, PositionMS: s.PositionMS}
				if next.LastPlayedAt == 0 {
					next.LastPlayedAt = here.LastPlayedAt
				}
				if next.Played {
					next.PositionMS = 0
				}
				if ok && next == here {
					continue
				}
				have[s.ItemID] = next // a second row for the item here compares with this one
				file := int64(0)
				if next.PositionMS > 0 {
					file = s.FileID
				}
				out = append(out, row{user, file, next})
			}
		}
		return out, nil
	}
	if !write {
		out, err := decide(d.r)
		if err != nil {
			return 0, fmt.Errorf("db: read watch state: %w", err)
		}
		return len(out), nil
	}
	n := 0
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		out, err := decide(tx.tx)
		if err != nil {
			return err
		}
		for _, r := range out {
			if err := tx.putStates(ctx, r.user, []State{r.state}, r.file); err != nil {
				return err
			}
		}
		n = len(out)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("db: write watch state: %w", err)
	}
	return n, nil
}
