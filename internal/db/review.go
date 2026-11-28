// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ReviewItem is one item on the review list (docs/04-api.md, section 8.4):
// a film or a series without a match, and an episode without a number
// (D51).
type ReviewItem struct {
	ID, LibraryID int64
	Library       string
	Type          string
	Title         string
	SortTitle     string
	Year          int
	Path          string // the folder, or the file, below the library's folder joined to it
	State         string // unmatched or review
	Confidence    float64
	Note          string // match_note, JSON, or ""
	SeriesID      int64  // an episode's
	SeriesTitle   string
	Season        int
}

// onReview is true for what is on the review list as unmatched (?1 set)
// or as review (?2 set): a film or a series by its match_state, and an
// episode without a number that is not left as it is, as review.
const onReview = `((i.type IN ('movie', 'series') AND ((?1 AND i.match_state = 'unmatched') OR (?2 AND i.match_state = 'review')))
	OR (?2 AND i.type = 'episode' AND i.episode_number IS NULL AND i.match_state <> 'ignored'))`

// ReviewAfter is where a page of the review list starts: after the item
// of this library, sort title and id.
type ReviewAfter struct {
	Library int64
	Sort    string
	ID      int64
}

// Review lists the review list by library, then sort title, then id: the
// unmatched with unmatched, those on review with review, up to limit after
// after (nil for the first page). It returns one more than limit when there
// are more.
func (d *DB) Review(ctx context.Context, unmatched, review bool, after *ReviewAfter, limit int) ([]ReviewItem, error) {
	args := []any{unmatched, review}
	cond := ""
	if after != nil {
		cond = ` AND (i.library_id, i.sort_title, i.id) > (?3, ?4, ?5)`
		args = append(args, after.Library, after.Sort, after.ID)
	}
	args = append(args, limit+1)
	rows, err := d.r.QueryContext(ctx, `SELECT i.id, i.library_id, l.name, i.type, i.title, i.sort_title, COALESCE(i.year, 0),
		COALESCE(r.path, ''), COALESCE(i.path_key, (SELECT f.rel_path FROM item_files x JOIN files f ON f.id = x.file_id
		  WHERE x.item_id = i.id ORDER BY x.part, f.id LIMIT 1), ''),
		i.match_state, COALESCE(i.match_confidence, 0), COALESCE(i.match_note, ''),
		COALESCE(i.series_id, 0), COALESCE(s.title, ''), COALESCE(i.season_number, 0)
		FROM items i JOIN libraries l ON l.id = i.library_id
		LEFT JOIN roots r ON r.id = COALESCE(i.root_id, (SELECT f.root_id FROM item_files x JOIN files f ON f.id = x.file_id
		  WHERE x.item_id = i.id LIMIT 1))
		LEFT JOIN items s ON s.id = i.series_id
		WHERE `+onReview+cond+` ORDER BY i.library_id, i.sort_title, i.id LIMIT ?`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("db: read the review list: %w", err)
	}
	defer rows.Close()
	var out []ReviewItem
	for rows.Next() {
		var it ReviewItem
		var root, rel string
		if err := rows.Scan(&it.ID, &it.LibraryID, &it.Library, &it.Type, &it.Title, &it.SortTitle, &it.Year, &root, &rel,
			&it.State, &it.Confidence, &it.Note, &it.SeriesID, &it.SeriesTitle, &it.Season); err != nil {
			return nil, err
		}
		if root != "" && rel != "" {
			it.Path = path.Join(root, rel)
		}
		if it.Type == "episode" {
			it.State = "review"
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ReviewCounts counts the review list's unmatched and its items on review.
func (d *DB) ReviewCounts(ctx context.Context) (unmatched, review int, err error) {
	err = d.r.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM items i WHERE `+onReview+`),
		(SELECT count(*) FROM items i WHERE `+strings.NewReplacer("?1", "0", "?2", "1").Replace(onReview)+`)`, true, false).
		Scan(&unmatched, &review)
	if err != nil {
		return 0, 0, fmt.Errorf("db: count the review list: %w", err)
	}
	return unmatched, review, nil
}

// Override is a manual match kept by folder (docs/03-data-model.md,
// section 3): a provider's id, or provider "none" for an item left
// unmatched on purpose.
type Override struct {
	Provider, Value             string
	SeasonOffset, EpisodeOffset int
	Numbering                   string // "", seasons or absolute
}

// ErrNotMatchable is an item that takes no match: an episode with a
// number, a season, a collection or a video.
var ErrNotMatchable = errors.New("db: this item takes no match")

// providerIDs are the providers whose ids name a title, which a manual
// match replaces and forgetting a match removes; a film's collection
// goes with them.
const providerIDs = `('tmdb', 'imdb', 'tvdb', 'anidb', 'anilist', 'mal', 'tmdb_collection')`

// SetOverride matches a film or a series by hand, or leaves it unmatched
// with provider "none", and keeps that for its folder in overrides, so
// that a build that makes the item again finds it (06 §6.1, §6.7). A
// match replaces the item's ids with the one given, makes it manual and
// asks for its metadata again; leaving it unmatched removes its ids and
// what was fetched for it, and makes it ignored. An episode without a
// number takes only "none", which leaves it as it is, off the review
// list, on the item itself. Ids of items never change. ErrNotFound: no
// such item; ErrNotMatchable: another type.
func (d *DB) SetOverride(ctx context.Context, id int64, o Override, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		t, err := matchable(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.typ == "episode" {
			if o.Provider != "none" {
				return ErrNotMatchable
			}
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'ignored' WHERE id = ?`, id); err != nil {
				return err
			}
			return tx.stampItems(ctx, []int64{id})
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO overrides (root_id, path_key, provider, value, season_offset, episode_offset,
			numbering, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (root_id, path_key) DO UPDATE SET provider = excluded.provider, value = excluded.value,
			  season_offset = excluded.season_offset, episode_offset = excluded.episode_offset, numbering = excluded.numbering,
			  created_at = excluded.created_at`,
			t.root, t.key, o.Provider, o.Value, o.SeasonOffset, o.EpisodeOffset, nullString(o.Numbering), now); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM external_ids WHERE item_id = ? AND provider IN `+providerIDs, id); err != nil {
			return err
		}
		if o.Provider == "none" {
			if err := dropProvided(ctx, tx, id); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'ignored', match_confidence = NULL, match_note = NULL
				WHERE id = ?`, id); err != nil {
				return err
			}
			return tx.stampItems(ctx, []int64{id})
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, ?, ?)`, id, o.Provider, o.Value); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'manual', match_confidence = NULL, match_note = NULL,
			refreshed_at = NULL WHERE id = ?`, id); err != nil {
			return err
		}
		return tx.stampItems(ctx, []int64{id})
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotMatchable) {
		return fmt.Errorf("db: match item %d by hand: %w", id, err)
	}
	return err
}

// ForgetMatch forgets a film's or a series' match: its folder's row in
// overrides, its ids, and what was fetched for it and its seasons and
// episodes, so that it is unmatched, titled from its names at the next
// build, and searched for again. An episode without a number goes back on
// the review list. Ids of items never change. ErrNotFound: no such item;
// ErrNotMatchable: another type.
func (d *DB) ForgetMatch(ctx context.Context, id int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		t, err := matchable(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.typ == "episode" {
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'unmatched' WHERE id = ?`, id); err != nil {
				return err
			}
			return tx.stampItems(ctx, []int64{id})
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM overrides WHERE root_id = ? AND path_key = ?`, t.root, t.key); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM external_ids WHERE item_id = ? AND provider IN `+providerIDs, id); err != nil {
			return err
		}
		if err := dropProvided(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET match_state = 'unmatched', match_confidence = NULL, match_note = NULL
			WHERE id = ?`, id); err != nil {
			return err
		}
		return tx.stampItems(ctx, []int64{id})
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotMatchable) {
		return fmt.Errorf("db: forget the match of item %d: %w", id, err)
	}
	return err
}

// matchTarget is an item as a manual match reads it.
type matchTarget struct {
	typ  string
	root int64
	key  string
}

// matchable reads an item that takes a manual match: a film or a series
// with a folder, or an episode without a number.
func matchable(ctx context.Context, tx *Tx, id int64) (matchTarget, error) {
	var t matchTarget
	var root sql.NullInt64
	var key sql.NullString
	var number sql.NullInt64
	err := tx.tx.QueryRowContext(ctx, `SELECT type, root_id, path_key, episode_number FROM items WHERE id = ?`, id).
		Scan(&t.typ, &root, &key, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.root, t.key = root.Int64, key.String
	switch {
	case (t.typ == "movie" || t.typ == "series") && t.key != "":
	case t.typ == "episode" && !number.Valid:
	default:
		return t, ErrNotMatchable
	}
	return t, nil
}

// dropProvided removes what was fetched from a provider for a film or a
// series, and for a series' seasons and episodes, as D53's drop does: the
// next build titles them from their names again.
func dropProvided(ctx context.Context, tx *Tx, id int64) error {
	rows, err := tx.tx.QueryContext(ctx, `SELECT id, type, title FROM items WHERE (id = ?1 OR series_id = ?1 OR parent_id = ?1)
		AND refreshed_at IS NOT NULL ORDER BY id`, id)
	if err != nil {
		return err
	}
	var all []staleItem
	for rows.Next() {
		var s staleItem
		if err := rows.Scan(&s.id, &s.typ, &s.title); err != nil {
			rows.Close()
			return err
		}
		all = append(all, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return dropStaleItems(ctx, tx, all)
}
