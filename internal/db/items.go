// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// LibraryFile is a video file of a library, as item building reads it.
type LibraryFile struct {
	ID      int64
	RootID  int64
	RelPath string
}

// LibraryFiles lists the video files of a library, extras left out,
// missing ones included: a missing file still plays its item, shown as
// unavailable, until its row goes.
func (d *DB) LibraryFiles(ctx context.Context, libraryID int64) ([]LibraryFile, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT f.id, f.root_id, f.rel_path FROM files f JOIN roots r ON r.id = f.root_id
		 WHERE r.library_id = ? AND f.kind = 'video' ORDER BY f.id`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("db: read files: %w", err)
	}
	defer rows.Close()
	var out []LibraryFile
	for rows.Next() {
		var f LibraryFile
		if err := rows.Scan(&f.ID, &f.RootID, &f.RelPath); err != nil {
			return nil, fmt.Errorf("db: read files: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DesiredFilm is a film item as its files say it should be.
type DesiredFilm struct {
	RootID  int64
	Key     string // path_key: the folder, or the lone file
	Title   string
	Year    int
	FileIDs []int64
}

// DesiredSeries is a series item and its episodes, as its files say.
type DesiredSeries struct {
	RootID   int64
	Key      string
	Title    string
	Year     int
	Episodes []DesiredEpisode
}

// DesiredEpisode is one episode of a series.
type DesiredEpisode struct {
	Season, Episode int
	FileIDs         []int64
}

// BuildResult counts what a build changed.
type BuildResult struct{ Added, Changed, Removed int }

// existing is an item as the database holds it.
type existing struct {
	id              int64
	typ             string
	rootID          int64
	key             string
	seriesID        int64
	season, episode int
	title, sort     string
	year            int
	addedAt         int64
	files           []int64 // sorted
	matched         bool
}

// fields are what building sets on an item.
type fields struct {
	typ             string
	rootID          int64 // 0 for episodes
	key             string
	seriesID        int64
	season, episode int
	title, sort     string
	year            int
	addedAt         int64
}

// BuildItems makes the items of a library what its files say: it adds,
// changes and deletes items, in one transaction, and changes nothing when
// nothing differs, so that a second run takes no sequence value
// (docs/06-library-and-metadata.md, section 5). sortTitle makes an item's
// sort_title from its title.
func (d *DB) BuildItems(ctx context.Context, libraryID int64, films []DesiredFilm, series []DesiredSeries, sortTitle func(string) string, now int64) (BuildResult, error) {
	var res BuildResult
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		b := builder{ctx: ctx, tx: tx, lib: libraryID, sortTitle: sortTitle, now: now, res: &res}
		if err := b.load(); err != nil {
			return err
		}
		if err := b.films(films); err != nil {
			return err
		}
		return b.series(series)
	})
	if err != nil {
		return BuildResult{}, fmt.Errorf("db: build items of library %d: %w", libraryID, err)
	}
	return res, nil
}

type builder struct {
	ctx       context.Context
	tx        *Tx
	lib       int64
	sortTitle func(string) string
	now       int64
	res       *BuildResult

	items     map[int64]*existing
	byKey     map[string]*existing           // "movie|root|key", "series|root|key"
	episodes  map[int64]map[[2]int]*existing // series id -> (season, episode)
	itemOf    map[int64]int64                // file id -> the item that plays it now
	firstSeen map[int64]int64                // file id -> first_seen_at
}

func keyOf(typ string, root int64, key string) string {
	return typ + "|" + strconv.FormatInt(root, 10) + "|" + key
}

func (b *builder) load() error {
	b.items = map[int64]*existing{}
	b.byKey = map[string]*existing{}
	b.episodes = map[int64]map[[2]int]*existing{}
	b.itemOf = map[int64]int64{}
	b.firstSeen = map[int64]int64{}

	rows, err := b.tx.tx.QueryContext(b.ctx,
		`SELECT id, type, root_id, path_key, series_id, season_number, episode_number, title, sort_title, year, added_at
		 FROM items WHERE library_id = ? AND type IN ('movie', 'series', 'episode')`, b.lib)
	if err != nil {
		return err
	}
	for rows.Next() {
		var e existing
		var root, series, season, episode, year sql.NullInt64
		var key sql.NullString
		if err := rows.Scan(&e.id, &e.typ, &root, &key, &series, &season, &episode, &e.title, &e.sort, &year, &e.addedAt); err != nil {
			rows.Close()
			return err
		}
		e.rootID, e.key, e.seriesID = root.Int64, key.String, series.Int64
		e.season, e.episode, e.year = int(season.Int64), int(episode.Int64), int(year.Int64)
		b.items[e.id] = &e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range b.items {
		switch e.typ {
		case "movie", "series":
			b.byKey[keyOf(e.typ, e.rootID, e.key)] = e
		case "episode":
			if b.episodes[e.seriesID] == nil {
				b.episodes[e.seriesID] = map[[2]int]*existing{}
			}
			b.episodes[e.seriesID][[2]int{e.season, e.episode}] = e
		}
	}

	links, err := b.tx.tx.QueryContext(b.ctx,
		`SELECT l.item_id, l.file_id FROM item_files l JOIN items i ON i.id = l.item_id WHERE i.library_id = ?`, b.lib)
	if err != nil {
		return err
	}
	for links.Next() {
		var item, file int64
		if err := links.Scan(&item, &file); err != nil {
			links.Close()
			return err
		}
		if e := b.items[item]; e != nil {
			e.files = append(e.files, file)
			b.itemOf[file] = item
		}
	}
	links.Close()
	for _, e := range b.items {
		slices.Sort(e.files)
	}

	seen, err := b.tx.tx.QueryContext(b.ctx,
		`SELECT f.id, f.first_seen_at FROM files f JOIN roots r ON r.id = f.root_id WHERE r.library_id = ?`, b.lib)
	if err != nil {
		return err
	}
	defer seen.Close()
	for seen.Next() {
		var id, at int64
		if err := seen.Scan(&id, &at); err != nil {
			return err
		}
		b.firstSeen[id] = at
	}
	return seen.Err()
}

// addedAt is the earliest first sighting of the files.
func (b *builder) addedAt(files []int64) int64 {
	var at int64
	for _, f := range files {
		if t := b.firstSeen[f]; at == 0 || t < at {
			at = t
		}
	}
	return at
}

// successor finds the existing item of type typ that played some of these
// files and whose own key no longer has anything: a folder that was
// renamed. The item keeps its id, and with it everything hung on it.
func (b *builder) successor(typ string, files []int64, wanted map[string]bool) *existing {
	for _, f := range files {
		e := b.items[b.itemOf[f]]
		if typ == "series" && e != nil && e.typ == "episode" {
			e = b.items[e.seriesID]
		}
		if e != nil && e.typ == typ && !e.matched && !wanted[keyOf(e.typ, e.rootID, e.key)] {
			return e
		}
	}
	return nil
}

// put writes an item: a new one when e is nil, otherwise the existing one
// where it differs. It reports the item's id and whether it changed.
func (b *builder) put(e *existing, f fields, files []int64) (int64, bool, error) {
	slices.Sort(files)
	if e == nil {
		seq, err := b.tx.Seq(b.ctx)
		if err != nil {
			return 0, false, err
		}
		r, err := b.tx.tx.ExecContext(b.ctx,
			`INSERT INTO items (library_id, type, root_id, path_key, series_id, season_number, episode_number,
			   title, sort_title, year, added_at, updated_seq)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.lib, f.typ, nullInt(f.rootID), nullString(f.key), nullInt(f.seriesID), seasonOrNull(f), episodeOrNull(f),
			f.title, f.sort, nullInt(int64(f.year)), f.addedAt, seq)
		if err != nil {
			return 0, false, err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		if err := b.index(id, f.title, false); err != nil {
			return 0, false, err
		}
		if err := b.link(id, nil, files); err != nil {
			return 0, false, err
		}
		b.res.Added++
		return id, true, nil
	}

	e.matched = true
	same := e.rootID == f.rootID && e.key == f.key && e.title == f.title && e.sort == f.sort &&
		e.year == f.year && e.addedAt == f.addedAt && e.season == f.season && e.episode == f.episode &&
		slices.Equal(e.files, files)
	if same {
		return e.id, false, nil
	}
	seq, err := b.tx.Seq(b.ctx)
	if err != nil {
		return 0, false, err
	}
	if _, err := b.tx.tx.ExecContext(b.ctx,
		`UPDATE items SET root_id = ?, path_key = ?, season_number = ?, episode_number = ?, title = ?, sort_title = ?,
		   year = ?, added_at = ?, updated_seq = ? WHERE id = ?`,
		nullInt(f.rootID), nullString(f.key), seasonOrNull(f), episodeOrNull(f), f.title, f.sort,
		nullInt(int64(f.year)), f.addedAt, seq, e.id); err != nil {
		return 0, false, err
	}
	if e.title != f.title {
		if err := b.index(e.id, f.title, true); err != nil {
			return 0, false, err
		}
	}
	if err := b.link(e.id, e.files, files); err != nil {
		return 0, false, err
	}
	b.res.Changed++
	return e.id, true, nil
}

func seasonOrNull(f fields) any {
	if f.typ != "episode" {
		return nil
	}
	return f.season
}

func episodeOrNull(f fields) any {
	if f.typ != "episode" {
		return nil
	}
	return f.episode
}

// index keeps the search table in step with an item's title
// (docs/03-data-model.md, section 4, Search index).
func (b *builder) index(id int64, title string, replace bool) error {
	if replace {
		if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM items_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	_, err := b.tx.tx.ExecContext(b.ctx, `INSERT INTO items_fts (rowid, title) VALUES (?, ?)`, id, title)
	return err
}

// link makes an item's files the given ones. In stage 1 every file is part
// 0 with no label: versions and parts come with the full item model.
func (b *builder) link(id int64, have, want []int64) error {
	for _, f := range have {
		if !slices.Contains(want, f) {
			if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM item_files WHERE item_id = ? AND file_id = ?`, id, f); err != nil {
				return err
			}
		}
	}
	for _, f := range want {
		if !slices.Contains(have, f) {
			// A file plays one item: take it from the one it played.
			if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM item_files WHERE file_id = ? AND item_id <> ?`, f, id); err != nil {
				return err
			}
			if _, err := b.tx.tx.ExecContext(b.ctx, `INSERT INTO item_files (item_id, file_id) VALUES (?, ?)`, id, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// remove deletes an item and writes its tombstone. A series takes its
// episodes with it, each with a tombstone of its own.
func (b *builder) remove(e *existing) error {
	seq, err := b.tx.Seq(b.ctx)
	if err != nil {
		return err
	}
	ids := []int64{e.id}
	if e.typ == "series" {
		for _, ep := range b.episodes[e.id] {
			if !ep.matched {
				ids = append(ids, ep.id)
			}
		}
	}
	for _, id := range ids {
		if _, err := b.tx.tx.ExecContext(b.ctx, `INSERT INTO tombstones (seq, item_id, at) VALUES (?, ?, ?)`, seq, id, b.now); err != nil {
			return err
		}
		if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM items_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		b.items[id].matched = true // handled
		b.res.Removed++
	}
	_, err = b.tx.tx.ExecContext(b.ctx, `DELETE FROM items WHERE id = ?`, e.id) // episodes follow by ON DELETE CASCADE
	return err
}

func (b *builder) films(films []DesiredFilm) error {
	wanted := map[string]bool{}
	for _, f := range films {
		wanted[keyOf("movie", f.RootID, f.Key)] = true
	}
	for _, f := range films {
		e := b.byKey[keyOf("movie", f.RootID, f.Key)]
		if e == nil {
			e = b.successor("movie", f.FileIDs, wanted)
		}
		if _, _, err := b.put(e, fields{typ: "movie", rootID: f.RootID, key: f.Key, title: f.Title,
			sort: b.sortTitle(f.Title), year: f.Year, addedAt: b.addedAt(f.FileIDs)}, f.FileIDs); err != nil {
			return err
		}
	}
	for _, e := range b.items {
		if e.typ == "movie" && !e.matched {
			if err := b.remove(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) series(series []DesiredSeries) error {
	wanted := map[string]bool{}
	for _, s := range series {
		wanted[keyOf("series", s.RootID, s.Key)] = true
	}
	for _, s := range series {
		var all []int64
		for _, ep := range s.Episodes {
			all = append(all, ep.FileIDs...)
		}
		e := b.byKey[keyOf("series", s.RootID, s.Key)]
		if e == nil {
			e = b.successor("series", all, wanted)
		}

		// The series first, so that its episodes have its id. Its
		// added_at is that of its newest episode, set once they are known.
		f := fields{typ: "series", rootID: s.RootID, key: s.Key, title: s.Title, sort: b.sortTitle(s.Title), year: s.Year}
		if e != nil {
			f.addedAt = e.addedAt
		}
		seriesID, _, err := b.put(e, f, nil)
		if err != nil {
			return err
		}

		episodesChanged := false
		var newest int64
		for _, ep := range s.Episodes {
			title := "Episode " + strconv.Itoa(ep.Episode)
			added := b.addedAt(ep.FileIDs)
			newest = max(newest, added)
			ex := b.episodes[seriesID][[2]int{ep.Season, ep.Episode}]
			_, changed, err := b.put(ex, fields{typ: "episode", seriesID: seriesID, season: ep.Season, episode: ep.Episode,
				title: title, sort: b.sortTitle(title), addedAt: added}, ep.FileIDs)
			if err != nil {
				return err
			}
			episodesChanged = episodesChanged || (changed && ex == nil)
		}
		for _, ex := range b.episodes[seriesID] {
			if !ex.matched {
				if err := b.remove(ex); err != nil {
					return err
				}
				episodesChanged = true
			}
		}

		// A new or removed episode changes the series' counts, so it
		// bumps the series (docs/03-data-model.md, section 5).
		current := b.items[seriesID]
		if current == nil || current.addedAt != newest || episodesChanged {
			seq, err := b.tx.Seq(b.ctx)
			if err != nil {
				return err
			}
			if _, err := b.tx.tx.ExecContext(b.ctx, `UPDATE items SET added_at = ?, updated_seq = ? WHERE id = ?`, newest, seq, seriesID); err != nil {
				return err
			}
		}
	}
	for _, e := range b.items {
		if e.typ == "series" && !e.matched {
			if err := b.remove(e); err != nil {
				return err
			}
		}
	}
	return nil
}

// ItemSummary is an item as tests and reports read it back.
type ItemSummary struct {
	ID              int64
	Type            string
	RootID          int64
	PathKey         string
	SeriesID        int64
	Season, Episode int
	Title           string
	SortTitle       string
	Year            int
	AddedAt         int64
	UpdatedSeq      int64
	FileIDs         []int64
}

// LibraryItems lists the items of a library in id order, with the files
// that play each.
func (d *DB) LibraryItems(ctx context.Context, libraryID int64) ([]ItemSummary, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT i.id, i.type, i.root_id, i.path_key, i.series_id, i.season_number, i.episode_number,
		   i.title, i.sort_title, i.year, i.added_at, i.updated_seq,
		   (SELECT group_concat(file_id) FROM (SELECT file_id FROM item_files WHERE item_id = i.id ORDER BY file_id))
		 FROM items i WHERE i.library_id = ? ORDER BY i.id`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("db: read items: %w", err)
	}
	defer rows.Close()
	var out []ItemSummary
	for rows.Next() {
		var it ItemSummary
		var root, series, season, episode, year sql.NullInt64
		var key, files sql.NullString
		if err := rows.Scan(&it.ID, &it.Type, &root, &key, &series, &season, &episode,
			&it.Title, &it.SortTitle, &year, &it.AddedAt, &it.UpdatedSeq, &files); err != nil {
			return nil, fmt.Errorf("db: read items: %w", err)
		}
		it.RootID, it.PathKey, it.SeriesID = root.Int64, key.String, series.Int64
		it.Season, it.Episode, it.Year = int(season.Int64), int(episode.Int64), int(year.Int64)
		for _, f := range strings.Split(files.String, ",") {
			if n, err := strconv.ParseInt(f, 10, 64); err == nil {
				it.FileIDs = append(it.FileIDs, n)
			}
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Tombstone is a deleted item, at the sequence value of its deletion.
type Tombstone struct{ Seq, ItemID int64 }

// TombstonesSince lists the deletions after a sequence value, in order
// (docs/03-data-model.md, section 6, what changed since a sequence).
func (d *DB) TombstonesSince(ctx context.Context, since int64, limit int) ([]Tombstone, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT seq, item_id FROM tombstones WHERE seq > ? ORDER BY seq LIMIT ?`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("db: read tombstones: %w", err)
	}
	defer rows.Close()
	var out []Tombstone
	for rows.Next() {
		var t Tombstone
		if err := rows.Scan(&t.Seq, &t.ItemID); err != nil {
			return nil, fmt.Errorf("db: read tombstones: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SearchTitles lists the ids of items whose title holds q, as the search
// table finds them: q is matched as a phrase, never as FTS syntax.
func (d *DB) SearchTitles(ctx context.Context, q string) ([]int64, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT rowid FROM items_fts WHERE items_fts MATCH ? ORDER BY rowid`,
		`"`+strings.ReplaceAll(q, `"`, `""`)+`"`)
	if err != nil {
		return nil, fmt.Errorf("db: search: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
