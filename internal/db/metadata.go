// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Metadata is what a provider says of a film or a series, as it is written
// onto the item (docs/06-library-and-metadata.md, section 6.3).
type Metadata struct {
	Title, SortTitle string
	OriginalTitle    string // "" when it is the title
	Overview         string
	Date             string  // YYYY-MM-DD, or ""; the year is read from it
	RuntimeMS        int64   // 0 when not known
	Rating           float64 // 0 when not known
	ContentRating    string
	Genres           []string
	Aliases          []string          // alternative titles, for the search index
	IDs              map[string]string // other providers' ids it gives: imdb, tvdb, tmdb_collection; "" takes one away
	Airing           bool              // a series that still airs
}

// SeasonMetadata is what a provider says of one season and its episodes.
type SeasonMetadata struct {
	Number          int
	Title, Overview string
	Date            string
	Episodes        []EpisodeMetadata
}

// EpisodeMetadata is what a provider says of one episode.
type EpisodeMetadata struct {
	Number          int
	Title, Overview string
	Date            string
	RuntimeMS       int64
	Rating          float64
	Still           string // the provider's path of its picture, or ""
}

// SeasonWritten is what ApplySeason wrote onto: the season, 0 when the
// series holds none of that number, and each episode with the provider's
// still of its first number, for its thumb.
type SeasonWritten struct {
	SeasonID int64
	Stills   map[int64]string
}

// shown is an item's metadata as a card or a detail shows it: what,
// written again, stamps the item when it differs.
type shown struct {
	title, sort, original, overview, date, contentRating string
	year                                                 int
	runtime                                              int64
	rating                                               float64
	genres                                               []string
	ids                                                  map[string]string
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}

// rowQuerier is a connection or a transaction that reads one row.
type rowQuerier interface {
	querier
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readShown reads what of an item a card and a detail show.
func readShown(ctx context.Context, q rowQuerier, id int64) (typ string, s shown, err error) {
	var original, overview, date, cr sql.NullString
	var year, runtime sql.NullInt64
	var rating sql.NullFloat64
	err = q.QueryRowContext(ctx, `SELECT type, title, sort_title, original_title, overview, release_date, content_rating,
		year, runtime_ms, rating FROM items WHERE id = ?`, id).
		Scan(&typ, &s.title, &s.sort, &original, &overview, &date, &cr, &year, &runtime, &rating)
	if err != nil {
		return "", shown{}, err
	}
	s.original, s.overview, s.date, s.contentRating = original.String, overview.String, date.String, cr.String
	s.year, s.runtime, s.rating = int(year.Int64), runtime.Int64, rating.Float64
	if s.genres, err = genresOf(ctx, q, id); err != nil {
		return "", shown{}, err
	}
	s.ids = map[string]string{}
	rows, err := q.QueryContext(ctx, `SELECT provider, value FROM external_ids WHERE item_id = ?`, id)
	if err != nil {
		return "", shown{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, v string
		if err := rows.Scan(&p, &v); err != nil {
			return "", shown{}, err
		}
		s.ids[p] = v
	}
	return typ, s, rows.Err()
}

func genresOf(ctx context.Context, q querier, id int64) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT genre FROM item_genres WHERE item_id = ? ORDER BY genre`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ApplyMetadata writes what a provider says of a film or a series onto it:
// its title, which the build then leaves as it is, the rest of what a card
// and a detail show, its genres, the other providers' ids it gives, and
// whether a series still airs; and it marks the item refreshed at now. The
// search index takes its title, its original title and its alternative
// titles. The item is stamped when anything that a client sees changed
// (docs/03-data-model.md, section 5). ErrNotFound: no such item.
func (d *DB) ApplyMetadata(ctx context.Context, id int64, m Metadata, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		typ, old, err := readShown(ctx, tx.tx, id)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		next := old
		if m.Title != "" {
			next.title, next.sort = m.Title, m.SortTitle
		}
		next.original = m.OriginalTitle
		if next.original == next.title {
			next.original = ""
		}
		next.overview, next.date, next.contentRating = m.Overview, m.Date, m.ContentRating
		if y := yearOf(m.Date); y != 0 {
			next.year = y
		}
		next.runtime, next.rating = m.RuntimeMS, m.Rating
		next.genres = slices.DeleteFunc(slices.Clone(m.Genres), func(g string) bool { return g == "" })
		slices.Sort(next.genres)
		next.genres = slices.Compact(next.genres)
		next.ids = maps.Clone(old.ids)
		for p, v := range m.IDs {
			switch {
			case p == "tmdb":
			case v == "":
				delete(next.ids, p)
			default:
				next.ids[p] = v
			}
		}
		var airing any
		if typ == "series" && m.Airing {
			airing = 1
		}
		if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET title = ?, sort_title = ?, original_title = ?, overview = ?,
			release_date = ?, year = ?, runtime_ms = ?, rating = ?, content_rating = ?, airing = ?, refreshed_at = ? WHERE id = ?`,
			next.title, next.sort, nullString(next.original), nullString(next.overview), nullString(next.date),
			nullInt(int64(next.year)), nullInt(next.runtime), nullFloat(next.rating), nullString(next.contentRating),
			airing, now, id); err != nil {
			return err
		}
		if !slices.Equal(old.genres, next.genres) {
			if _, err := tx.tx.ExecContext(ctx, `DELETE FROM item_genres WHERE item_id = ?`, id); err != nil {
				return err
			}
			for _, g := range next.genres {
				if _, err := tx.tx.ExecContext(ctx, `INSERT INTO item_genres (item_id, genre) VALUES (?, ?)`, id, g); err != nil {
					return err
				}
			}
		}
		for _, p := range slices.Sorted(maps.Keys(old.ids)) {
			if _, kept := next.ids[p]; !kept {
				if _, err := tx.tx.ExecContext(ctx, `DELETE FROM external_ids WHERE item_id = ? AND provider = ?`, id, p); err != nil {
					return err
				}
			}
		}
		for _, p := range slices.Sorted(maps.Keys(next.ids)) {
			if old.ids[p] != next.ids[p] {
				if _, err := tx.tx.ExecContext(ctx, `INSERT INTO external_ids (item_id, provider, value) VALUES (?, ?, ?)
					ON CONFLICT (item_id, provider) DO UPDATE SET value = excluded.value`, id, p, next.ids[p]); err != nil {
					return err
				}
			}
		}
		// The search table keeps no text to compare with, so its row is
		// written again each time; that is no change a client sees.
		if err := reindex(ctx, tx, id, next.title, next.original, m.Aliases); err != nil {
			return err
		}
		if !sameShown(old, next) {
			return tx.stampItems(ctx, []int64{id})
		}
		return nil
	})
	if err != nil && err != ErrNotFound {
		return fmt.Errorf("db: write the metadata of item %d: %w", id, err)
	}
	return err
}

func sameShown(a, b shown) bool {
	return a.title == b.title && a.sort == b.sort && a.original == b.original && a.overview == b.overview &&
		a.date == b.date && a.contentRating == b.contentRating && a.year == b.year && a.runtime == b.runtime &&
		a.rating == b.rating && slices.Equal(a.genres, b.genres) && maps.Equal(a.ids, b.ids)
}

// reindex writes an item's row of the search table again
// (docs/03-data-model.md, section 4, Search index).
func reindex(ctx context.Context, tx *Tx, id int64, title, original string, aliases []string) error {
	if _, err := tx.tx.ExecContext(ctx, `DELETE FROM items_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(ctx, `INSERT INTO items_fts (rowid, title, original_title, aliases) VALUES (?, ?, ?, ?)`,
		id, title, nullString(original), nullString(strings.Join(aliases, "\n")))
	return err
}

// ApplySeason writes what a provider says of a season onto the season of
// a series that has that number, and onto its numbered episodes: each
// takes the title, overview, date, runtime and rating of the provider's
// episode with its number, and one whose file holds several episodes takes
// their titles joined, the first's overview, date and rating, and their
// runtimes added. Every episode and the season are marked refreshed at
// now, the provider's or not, so that one the provider does not have is
// not asked for again before the season's next refresh. An episode without
// a number (D51), and the episodes of a series numbered in one running
// count, which wait for its mapping to seasons (section 6.5 there), are
// left as they are. A series that holds no such season is left as it is.
func (d *DB) ApplySeason(ctx context.Context, seriesID int64, m SeasonMetadata, sortTitle func(string) string, now int64) (SeasonWritten, error) {
	out := SeasonWritten{Stills: map[int64]string{}}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		out = SeasonWritten{Stills: map[int64]string{}}
		var seasonID int64
		err := tx.tx.QueryRowContext(ctx, `SELECT id FROM items WHERE parent_id = ? AND type = 'season' AND season_number = ?`,
			seriesID, m.Number).Scan(&seasonID)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		out.SeasonID = seasonID
		var stamp []int64
		if changed, err := writeShown(ctx, tx, seasonID, m.Title, sortTitle, m.Overview, m.Date, 0, 0, now); err != nil {
			return err
		} else if changed {
			stamp = append(stamp, seasonID)
		}
		var numbering sql.NullString
		if err := tx.tx.QueryRowContext(ctx, `SELECT numbering FROM items WHERE id = ?`, seriesID).Scan(&numbering); err != nil {
			return err
		}
		if numbering.String == "absolute" {
			return tx.stampItems(ctx, stamp)
		}
		byNumber := map[int]EpisodeMetadata{}
		for _, e := range m.Episodes {
			if _, ok := byNumber[e.Number]; !ok {
				byNumber[e.Number] = e
			}
		}
		type ep struct {
			id         int64
			first, end int
		}
		var eps []ep
		rows, err := tx.tx.QueryContext(ctx, `SELECT id, episode_number, episode_number_end FROM items
			WHERE parent_id = ? AND type = 'episode' AND episode_number IS NOT NULL ORDER BY episode_number, id`, seasonID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e ep
			var end sql.NullInt64
			if err := rows.Scan(&e.id, &e.first, &end); err != nil {
				rows.Close()
				return err
			}
			e.end = max(e.first, int(end.Int64))
			eps = append(eps, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range eps {
			var titles []string
			var got []EpisodeMetadata
			for n := e.first; n <= e.end; n++ {
				if pe, ok := byNumber[n]; ok {
					got = append(got, pe)
					if pe.Title != "" {
						titles = append(titles, pe.Title)
					}
				}
			}
			if len(got) == 0 {
				// The provider has none of its numbers: it keeps its title,
				// and waits for the season's next refresh.
				if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET refreshed_at = ? WHERE id = ?`, now, e.id); err != nil {
					return err
				}
				continue
			}
			if got[0].Number == e.first && got[0].Still != "" {
				out.Stills[e.id] = got[0].Still
			}
			var runtime int64
			for _, pe := range got {
				runtime += pe.RuntimeMS
			}
			changed, err := writeShown(ctx, tx, e.id, strings.Join(titles, " / "), sortTitle, got[0].Overview, got[0].Date, runtime, got[0].Rating, now)
			if err != nil {
				return err
			}
			if changed {
				stamp = append(stamp, e.id)
			}
		}
		return tx.stampItems(ctx, stamp)
	})
	if err != nil {
		return SeasonWritten{}, fmt.Errorf("db: write the metadata of season %d of item %d: %w", m.Number, seriesID, err)
	}
	return out, nil
}

// writeShown writes a season's or an episode's metadata, marks it
// refreshed, and reports whether what a client sees changed. An empty
// title keeps the one it has. The search table follows an episode's title;
// seasons are not in it.
func writeShown(ctx context.Context, tx *Tx, id int64, title string, sortTitle func(string) string, overview, date string,
	runtime int64, rating float64, now int64) (bool, error) {
	typ, old, err := readShown(ctx, tx.tx, id)
	if err != nil {
		return false, err
	}
	next := old
	if title != "" {
		next.title, next.sort = title, sortTitle(title)
	}
	next.overview, next.date, next.runtime, next.rating = overview, date, runtime, rating
	if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET title = ?, sort_title = ?, overview = ?, release_date = ?,
		runtime_ms = ?, rating = ?, refreshed_at = ? WHERE id = ?`,
		next.title, next.sort, nullString(next.overview), nullString(next.date), nullInt(next.runtime), nullFloat(next.rating), now, id); err != nil {
		return false, err
	}
	if typ == "episode" && next.title != old.title {
		if err := reindex(ctx, tx, id, next.title, "", nil); err != nil {
			return false, err
		}
	}
	return !sameShown(old, next), nil
}

// DropStale drops what was read from a provider onto items that have not
// been refreshed since before (D53): every column a provider fills, the
// genres, whether a series airs, the provider's images, whose files the
// sweep deletes, and the time it was refreshed. Titles and
// years go back to what the names say at the next build, which the nightly
// walk after it runs; a collection, which has no name, is titled
// "Collection" until it is fetched again. The providers' ids stay, since
// they are the match. It returns how many items it dropped, each stamped.
func (d *DB) DropStale(ctx context.Context, before int64) (int, error) {
	var n int
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		rows, err := tx.tx.QueryContext(ctx, `SELECT id, type, title FROM items WHERE refreshed_at < ? ORDER BY id`, before)
		if err != nil {
			return err
		}
		type stale struct {
			id         int64
			typ, title string
		}
		var all []stale
		for rows.Next() {
			var s stale
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
		ids := make([]int64, len(all))
		for i, s := range all {
			ids[i] = s.id
			if s.typ == "collection" {
				// A collection has no name of its own to fall back to.
				s.title = "Collection"
				if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET title = ?, sort_title = ? WHERE id = ?`,
					s.title, strings.ToLower(s.title), s.id); err != nil {
					return err
				}
			}
			if _, err := tx.tx.ExecContext(ctx, `UPDATE items SET original_title = NULL, overview = NULL, release_date = NULL,
				runtime_ms = NULL, rating = NULL, content_rating = NULL, airing = NULL, refreshed_at = NULL WHERE id = ?`, s.id); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `DELETE FROM item_genres WHERE item_id = ?`, s.id); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `DELETE FROM images WHERE item_id = ? AND source <> 'local'`, s.id); err != nil {
				return err
			}
			if s.typ != "season" {
				if err := reindex(ctx, tx, s.id, s.title, "", nil); err != nil {
					return err
				}
			}
		}
		n = len(ids)
		return tx.stampItems(ctx, ids)
	})
	if err != nil {
		return 0, fmt.Errorf("db: drop metadata not refreshed for long: %w", err)
	}
	return n, nil
}
