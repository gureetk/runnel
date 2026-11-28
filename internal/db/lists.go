// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ListQuery is a list of films and series (docs/04-api.md, section 4.3):
// a library's, a genre's, or what a user has on My list, narrowed by type
// and by what the user has played, in one of its sorts.
type ListQuery struct {
	UserID    int64
	LibraryID int64    // 0: every library
	Types     []string // "movie", "series", or both
	Genre     string
	Listed    bool // only what the user has on My list
	// Collection, when set, is a collection whose films are listed.
	Collection int64
	// Person, when set, is a person whose films and series are listed.
	Person int64
	Played int // -1: either; 0: films not played and series with an episode not played; 1: the others
	Sort   string
	Desc   bool
}

// Sorts are the orders a list can have. "listed" goes with Listed only.
var Sorts = map[string]bool{"title": true, "added": true, "date": true, "rating": true, "listed": true}

// sortExpr is the value a list sorts by. A series was added when its
// newest episode was. A date that is not known falls back to the year.
func (q ListQuery) sortExpr() (expr string, nullable bool) {
	switch q.Sort {
	case "added":
		return `(CASE i.type WHEN 'series' THEN COALESCE((SELECT max(e.added_at) FROM items e
			WHERE e.series_id = i.id AND e.type = 'episode'), i.added_at) ELSE i.added_at END)`, false
	case "date":
		return `COALESCE(i.release_date, CASE WHEN i.year IS NOT NULL THEN printf('%04d', i.year) END)`, true
	case "rating":
		return `i.rating`, true
	case "listed":
		return `l.listed_at`, false
	}
	return `i.sort_title`, false
}

// playedSQL is true for a film the user played, or a series with no
// episode outside the specials that the user has not played.
const playedSQL = `((i.type = 'movie' AND EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = ? AND s.item_id = i.id AND s.played = 1))
	OR (i.type = 'series' AND NOT EXISTS (SELECT 1 FROM items e WHERE e.series_id = i.id AND e.type = 'episode' AND e.season_number > 0
	    AND NOT EXISTS (SELECT 1 FROM user_state s WHERE s.user_id = ? AND s.item_id = e.id AND s.played = 1))))`

// from is the FROM and WHERE of the query, without paging.
func (q ListQuery) from() (string, []any) {
	var b strings.Builder
	var args []any
	b.WriteString(` FROM items i`)
	if q.Listed || q.Sort == "listed" {
		b.WriteString(` JOIN user_list l ON l.item_id = i.id AND l.user_id = ? AND l.listed_at IS NOT NULL`)
		args = append(args, q.UserID)
	}
	types := q.Types
	if len(types) == 0 {
		types = []string{"movie", "series"}
	}
	b.WriteString(` WHERE i.type IN (` + strings.TrimSuffix(strings.Repeat("?, ", len(types)), ", ") + `)`)
	for _, t := range types {
		args = append(args, t)
	}
	if q.LibraryID != 0 {
		b.WriteString(` AND i.library_id = ?`)
		args = append(args, q.LibraryID)
	}
	if q.Collection != 0 {
		b.WriteString(` AND i.collection_id = ?`)
		args = append(args, q.Collection)
	}
	if q.Person != 0 {
		b.WriteString(` AND EXISTS (SELECT 1 FROM credits c WHERE c.item_id = i.id AND c.person_id = ?)`)
		args = append(args, q.Person)
	}
	if q.Genre != "" {
		b.WriteString(` AND EXISTS (SELECT 1 FROM item_genres g WHERE g.item_id = i.id AND g.genre = ?)`)
		args = append(args, q.Genre)
	}
	switch q.Played {
	case 1:
		b.WriteString(` AND ` + playedSQL)
		args = append(args, q.UserID, q.UserID)
	case 0:
		b.WriteString(` AND NOT ` + playedSQL)
		args = append(args, q.UserID, q.UserID)
	}
	return b.String(), args
}

// ListKey is where a page of a list continues: after the row with this
// sort value, title and id. Null is set for a row whose sort value is not
// known: those come last, in either order. Rows with the same value go by
// title.
type ListKey struct {
	Null  bool
	Value any // a string or a number, as the sort has it
	Title string
	ID    int64
}

// ListItems reads a page of a list: after key when it is not nil, or from
// offset, at most limit cards, with the key of each.
func (d *DB) ListItems(ctx context.Context, q ListQuery, after *ListKey, offset, limit int) ([]Card, []ListKey, error) {
	expr, nullable := q.sortExpr()
	from, args := q.from()
	dir, cmp := "", ">"
	if q.Desc {
		dir, cmp = " DESC", "<"
	}
	where := ""
	if after != nil {
		const byTitle = `(i.sort_title, i.id) > (?, ?)`
		switch {
		case q.Sort == "title":
			where = ` AND (i.sort_title, i.id) ` + cmp + ` (?, ?)`
			args = append(args, after.Title, after.ID)
		case nullable && after.Null:
			where = ` AND ` + expr + ` IS NULL AND ` + byTitle
			args = append(args, after.Title, after.ID)
		case nullable:
			where = ` AND (` + expr + ` IS NULL OR ` + expr + ` ` + cmp + ` ? OR (` + expr + ` = ? AND ` + byTitle + `))`
			args = append(args, after.Value, after.Value, after.Title, after.ID)
		default:
			where = ` AND (` + expr + ` ` + cmp + ` ? OR (` + expr + ` = ? AND ` + byTitle + `))`
			args = append(args, after.Value, after.Value, after.Title, after.ID)
		}
	}
	order := ` ORDER BY ` + expr + dir + `, i.sort_title, i.id`
	switch {
	case q.Sort == "title":
		order = ` ORDER BY i.sort_title` + dir + `, i.id` + dir
	case nullable:
		order = ` ORDER BY ` + expr + ` IS NULL, ` + expr + dir + `, i.sort_title, i.id`
	}
	query := `SELECT ` + cardColumns + `, ` + expr + from + where + order + ` LIMIT ? OFFSET ?`
	rows, err := d.r.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, nil, fmt.Errorf("db: read a list: %w", err)
	}
	var values []any
	cards, err := scanCardsWith(rows, func() any {
		values = append(values, nil)
		return &values[len(values)-1]
	})
	if err != nil {
		return nil, nil, fmt.Errorf("db: read a list: %w", err)
	}
	keys := make([]ListKey, len(cards))
	for i, c := range cards {
		v := values[i]
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		keys[i] = ListKey{Null: v == nil, Value: v, Title: c.SortTitle, ID: c.ID}
	}
	return cards, keys, nil
}

// CountItems counts a list's rows.
func (d *DB) CountItems(ctx context.Context, q ListQuery) (int, error) {
	from, args := q.from()
	var n int
	if err := d.r.QueryRowContext(ctx, `SELECT count(*)`+from, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count a list: %w", err)
	}
	return n, nil
}

// Letter is where the titles that begin with a letter begin in a list
// sorted by title (docs/04-api.md, section 4.3).
type Letter struct {
	Letter string
	Offset int
	Count  int
}

// Letters is the letters of a list sorted by title: A to Z, # for titles
// that sort before A and ~ for those after Z, and the total.
func (d *DB) Letters(ctx context.Context, q ListQuery) ([]Letter, int, error) {
	from, args := q.from()
	rows, err := d.r.QueryContext(ctx, `WITH f AS (SELECT i.sort_title`+from+`),
		letters AS (
		  SELECT CASE WHEN substr(sort_title, 1, 1) < 'a' THEN '#'
		              WHEN substr(sort_title, 1, 1) > 'z' THEN '~'
		              ELSE upper(substr(sort_title, 1, 1)) END AS letter,
		         count(*) AS n, min(sort_title) AS first
		  FROM f GROUP BY letter)
		SELECT letter, n, COALESCE(sum(n) OVER (ORDER BY first ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING), 0)
		FROM letters ORDER BY first`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("db: read letters: %w", err)
	}
	defer rows.Close()
	out := []Letter{}
	total := 0
	for rows.Next() {
		var l Letter
		if err := rows.Scan(&l.Letter, &l.Count, &l.Offset); err != nil {
			return nil, 0, err
		}
		total += l.Count
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// ItemsByID reads the cards of some items, in the order of ids. An id
// that no item has is left out.
func (d *DB) ItemsByID(ctx context.Context, ids []int64) ([]Card, error) {
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM json_each(?) a JOIN items i ON i.id = a.value ORDER BY a.key`, string(list))
	if err != nil {
		return nil, fmt.Errorf("db: read items: %w", err)
	}
	cards, err := scanCards(rows)
	if err != nil {
		return nil, fmt.Errorf("db: read items: %w", err)
	}
	seen := map[int64]bool{}
	return slices.DeleteFunc(cards, func(c Card) bool {
		dup := seen[c.ID]
		seen[c.ID] = true
		return dup
	}), nil
}

// Search finds films and series, or other types, whose titles hold q
// (docs/04-api.md, section 4.5): by the search table, in relevance order,
// or for a query shorter than three characters by the start of the title.
func (d *DB) Search(ctx context.Context, q string, types []string, limit int) ([]Card, error) {
	in := `(` + strings.TrimSuffix(strings.Repeat("?, ", len(types)), ", ") + `)`
	var args []any
	var query string
	if len([]rune(q)) >= 3 {
		query = `SELECT ` + cardColumns + ` FROM items_fts f JOIN items i ON i.id = f.rowid
			WHERE items_fts MATCH ? AND i.type IN ` + in + ` ORDER BY f.rank, i.sort_title, i.id LIMIT ?`
		args = append(args, `"`+strings.ReplaceAll(q, `"`, `""`)+`"`)
	} else {
		pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q)) + `%`
		query = `SELECT ` + cardColumns + ` FROM items i
			WHERE (i.sort_title LIKE ? ESCAPE '\' OR i.title LIKE ? ESCAPE '\') AND i.type IN ` + in + `
			ORDER BY i.sort_title, i.id LIMIT ?`
		args = append(args, pattern, pattern)
	}
	for _, t := range types {
		args = append(args, t)
	}
	rows, err := d.r.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("db: search: %w", err)
	}
	cards, err := scanCards(rows)
	if err != nil {
		return nil, fmt.Errorf("db: search: %w", err)
	}
	return cards, nil
}

// Genre is a genre with how many films and series have it, and in which
// libraries.
type Genre struct {
	Name      string
	Items     int
	Libraries []int64
}

// Genres lists the genres of the films and series of one library, or of
// every library with libraryID 0, by name.
func (d *DB) Genres(ctx context.Context, libraryID int64) ([]Genre, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT g.genre, count(*), group_concat(DISTINCT i.library_id)
		FROM item_genres g JOIN items i ON i.id = g.item_id
		WHERE i.type IN ('movie', 'series') AND (?1 = 0 OR i.library_id = ?1)
		GROUP BY g.genre ORDER BY g.genre`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("db: read genres: %w", err)
	}
	defer rows.Close()
	out := []Genre{}
	for rows.Next() {
		var g Genre
		var libs string
		if err := rows.Scan(&g.Name, &g.Items, &libs); err != nil {
			return nil, err
		}
		for _, s := range strings.Split(libs, ",") {
			if id, err := strconv.ParseInt(s, 10, 64); err == nil {
				g.Libraries = append(g.Libraries, id)
			}
		}
		slices.Sort(g.Libraries)
		out = append(out, g)
	}
	return out, rows.Err()
}

// Home is the rows of the first screen (docs/04-api.md, section 4.2).
type Home struct {
	Continue, NextUp, Recent, List []Card
	NewEpisodes                    map[int64]int // a recent series: its episodes added since the row's start
}

// HomeOf reads a user's Home, of one library or of every one with
// libraryID 0: at most limit cards in each row. recentSince is when
// Recently added begins.
func (d *DB) HomeOf(ctx context.Context, userID, libraryID int64, recentSince int64, limit int) (Home, error) {
	h := Home{NewEpisodes: map[int64]int{}}
	var err error
	read := func(query string, args ...any) []Card {
		if err != nil {
			return nil
		}
		var rows *sql.Rows
		if rows, err = d.r.QueryContext(ctx, query, args...); err != nil {
			return nil
		}
		var cards []Card
		cards, err = scanCards(rows)
		return cards
	}
	h.Continue = read(`WITH top(id, t) AS (
		  SELECT s.item_id, s.last_played_at FROM user_state s JOIN items e ON e.id = s.item_id
		  WHERE s.user_id = ?1 AND s.played = 0 AND s.position_ms > 0 AND (?2 = 0 OR e.library_id = ?2)
		  ORDER BY s.last_played_at DESC, s.item_id DESC LIMIT ?3)
		SELECT `+cardColumns+` FROM top JOIN items i ON i.id = top.id ORDER BY top.t DESC, i.id DESC`, userID, libraryID, limit)
	h.NextUp = read(`WITH last_played AS (
		  SELECT e.series_id, e.season_number AS s, e.episode_number AS n, us.last_played_at AS t,
		         ROW_NUMBER() OVER (PARTITION BY e.series_id
		                            ORDER BY us.last_played_at DESC, e.season_number DESC, e.episode_number DESC) AS r
		  FROM user_state us JOIN items e ON e.id = us.item_id
		  WHERE us.user_id = ?1 AND us.played = 1 AND e.type = 'episode' AND e.episode_number IS NOT NULL
		    AND (?2 = 0 OR e.library_id = ?2))
		, nx(id, t) AS (
		  SELECT (SELECT c.id FROM items c
		          WHERE c.series_id = lp.series_id AND c.type = 'episode' AND c.season_number > 0
		            AND c.episode_number IS NOT NULL AND (c.season_number, c.episode_number) > (lp.s, lp.n)
		            AND NOT EXISTS (SELECT 1 FROM user_state u
		                            WHERE u.user_id = ?1 AND u.item_id = c.id AND (u.played = 1 OR u.position_ms > 0))
		          ORDER BY c.season_number, c.episode_number, COALESCE(c.absolute_number, 0), c.id
		          LIMIT 1), lp.t
		  FROM last_played lp WHERE lp.r = 1),
		top(id, t) AS (SELECT id, t FROM nx WHERE id IS NOT NULL ORDER BY t DESC, id DESC LIMIT ?3)
		SELECT `+cardColumns+` FROM top JOIN items i ON i.id = top.id ORDER BY top.t DESC, i.id DESC`, userID, libraryID, limit)
	if err == nil {
		var rows *sql.Rows
		// The libraries are named, so that the search goes by the index
		// on (library, type, added).
		rows, err = d.r.QueryContext(ctx, `WITH libs(id) AS (SELECT id FROM libraries WHERE ?2 = 0 OR id = ?2),
			r(id, t, n) AS (
			  SELECT id, added_at, 0 FROM items WHERE type = 'movie' AND added_at > ?1 AND library_id IN (SELECT id FROM libs)
			  UNION ALL
			  SELECT series_id, max(added_at), count(*) FROM items
			  WHERE type = 'episode' AND added_at > ?1 AND library_id IN (SELECT id FROM libs) GROUP BY series_id),
			top(id, t, n) AS (SELECT id, t, n FROM r ORDER BY t DESC, id DESC LIMIT ?3)
			SELECT `+cardColumns+`, top.n FROM top JOIN items i ON i.id = top.id ORDER BY top.t DESC, i.id DESC`,
			recentSince, libraryID, limit)
		if err == nil {
			var counts []int
			h.Recent, err = scanCardsWith(rows, func() any {
				counts = append(counts, 0)
				return &counts[len(counts)-1]
			})
			for i, c := range h.Recent {
				if c.Type == "series" {
					h.NewEpisodes[c.ID] = counts[i]
				}
			}
		}
	}
	if err == nil {
		h.List, _, err = d.ListItems(ctx, ListQuery{UserID: userID, LibraryID: libraryID, Listed: true, Played: -1,
			Sort: "listed", Desc: true}, nil, 0, limit)
	}
	if err != nil {
		return Home{}, fmt.Errorf("db: read Home: %w", err)
	}
	return h, nil
}
