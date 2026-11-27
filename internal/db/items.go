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

// DesiredFile is a file of an item, and how it plays the item.
type DesiredFile struct {
	ID    int64
	Label string // the version's label: "" for the main version, or when there is one version
	Part  int    // 0 for the whole item; 1 to n for the parts, in order
}

// DesiredFilm is a film item as its files say it should be.
type DesiredFilm struct {
	RootID int64
	Key    string // path_key: the folder, or the lone file
	Title  string
	Year   int
	Files  []DesiredFile
}

// DesiredSeries is a series item and its episodes, as its files say. Its
// season items are made from the episodes' seasons.
type DesiredSeries struct {
	RootID   int64
	Key      string
	Title    string
	Year     int
	Absolute bool // numbered in one running count
	Episodes []DesiredEpisode
}

// DesiredEpisode is one episode of a series.
type DesiredEpisode struct {
	Season, Episode, EpisodeEnd int
	// Absolute is the running number of an episode of a series numbered in
	// one running count, and then the episode's key. Season and Episode say
	// where it is shown until the count is mapped to the provider's seasons.
	Absolute int
	// Unnumbered is an episode without a number (D51), made of its one
	// file, which is its key. Order is the decimal its name gives, or 0;
	// Title is its name.
	Unnumbered bool
	Order      float64
	Title      string
	Files      []DesiredFile
}

// BuildResult counts what a build changed.
type BuildResult struct{ Added, Changed, Removed int }

type link struct {
	label string
	part  int
}

// existing is an item as the database holds it.
type existing struct {
	id                   int64
	typ                  string
	rootID               int64
	key                  string
	parentID, seriesID   int64
	season, episode, end int
	absolute             int
	unnumbered           bool    // an episode without a number
	order                float64 // its episode_order, or 0
	numbering            string
	title, sort          string
	year                 int
	addedAt              int64
	links                map[int64]link // file id -> how it plays the item
	lowest               int64          // the lowest file id the item plays; for a series, of its episodes
	matched              bool           // a desired item has it, or it was removed
}

// fields are what building sets on an item.
type fields struct {
	typ                  string
	rootID               int64 // films and series
	key                  string
	parentID, seriesID   int64 // seasons and episodes
	season, episode, end int
	absolute             int
	unnumbered           bool    // an episode without a number
	order                float64 // its episode_order, or 0
	numbering            string  // series
	title, sort          string
	year                 int
	addedAt              int64
	links                map[int64]link
}

// BuildItems makes the items of a library what its files say: films;
// series with their seasons and episodes; versions and parts. It adds,
// changes and deletes items in one transaction, and changes nothing when
// nothing differs, so that a second run takes no sequence value
// (docs/06-library-and-metadata.md, section 5). sortTitle makes an item's
// sort_title from its title.
//
// An item keeps its id while its files say it is the same item: a film or a
// series by its folder, a season by its series and number, an episode by
// its series and its key (its season and number, or its running number).
// Where a key is new, the item that played the files follows them, so that
// a renamed folder, a renamed file, a corrected parser or a change of
// numbering keeps the ids that watch state hangs on.
func (d *DB) BuildItems(ctx context.Context, libraryID int64, films []DesiredFilm, series []DesiredSeries, sortTitle func(string) string, now int64) (BuildResult, error) {
	var res BuildResult
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		b := builder{ctx: ctx, tx: tx, lib: libraryID, sortTitle: sortTitle, now: now, res: &res}
		if err := b.load(); err != nil {
			return err
		}
		return b.build(films, series)
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
	seasons   map[int64]map[int]*existing    // series id -> season number
	episodes  map[int64]map[string]*existing // series id -> episode key
	itemOf    map[int64]int64                // file id -> the item that plays it now
	firstSeen map[int64]int64                // file id -> first_seen_at
	written   map[int64]bool                 // items stamped in this build
	touched   map[int64]bool                 // seasons and series whose episodes changed
}

func keyOf(typ string, root int64, key string) string {
	return typ + "|" + strconv.FormatInt(root, 10) + "|" + key
}

func episodeKey(season, episode, absolute int) string {
	if absolute != 0 {
		return "a:" + strconv.Itoa(absolute)
	}
	return "s:" + strconv.Itoa(season) + ":" + strconv.Itoa(episode)
}

// unnumberedKey is the key of an episode without a number: the file it is
// made of (D51).
func unnumberedKey(file int64) string { return "u:" + strconv.FormatInt(file, 10) }

// keyOfDesired is a desired episode's key.
func keyOfDesired(ep DesiredEpisode) string {
	if ep.Unnumbered && len(ep.Files) > 0 {
		return unnumberedKey(ep.Files[0].ID)
	}
	return episodeKey(ep.Season, ep.Episode, ep.Absolute)
}

// keyOfExisting is an existing episode's key; one without a number is
// keyed by the lowest file it plays.
func keyOfExisting(e *existing) string {
	if e.unnumbered {
		return unnumberedKey(e.lowest)
	}
	return episodeKey(e.season, e.episode, e.absolute)
}

func (b *builder) load() error {
	b.items = map[int64]*existing{}
	b.byKey = map[string]*existing{}
	b.seasons = map[int64]map[int]*existing{}
	b.episodes = map[int64]map[string]*existing{}
	b.itemOf = map[int64]int64{}
	b.firstSeen = map[int64]int64{}
	b.written = map[int64]bool{}
	b.touched = map[int64]bool{}

	rows, err := b.tx.tx.QueryContext(b.ctx,
		`SELECT id, type, root_id, path_key, parent_id, series_id, season_number, episode_number,
		   episode_number_end, absolute_number, numbering, title, sort_title, year, added_at, episode_order
		 FROM items WHERE library_id = ? AND type IN ('movie', 'series', 'season', 'episode')`, b.lib)
	if err != nil {
		return err
	}
	for rows.Next() {
		e := existing{links: map[int64]link{}}
		var root, parent, series, season, episode, end, absolute, year sql.NullInt64
		var key, numbering sql.NullString
		var order sql.NullFloat64
		if err := rows.Scan(&e.id, &e.typ, &root, &key, &parent, &series, &season, &episode, &end, &absolute,
			&numbering, &e.title, &e.sort, &year, &e.addedAt, &order); err != nil {
			rows.Close()
			return err
		}
		e.rootID, e.key, e.parentID, e.seriesID = root.Int64, key.String, parent.Int64, series.Int64
		e.season, e.episode, e.end, e.absolute = int(season.Int64), int(episode.Int64), int(end.Int64), int(absolute.Int64)
		e.numbering, e.year = numbering.String, int(year.Int64)
		e.unnumbered, e.order = e.typ == "episode" && !episode.Valid, order.Float64
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
		case "season":
			if b.seasons[e.seriesID] == nil {
				b.seasons[e.seriesID] = map[int]*existing{}
			}
			b.seasons[e.seriesID][e.season] = e
		}
	}

	links, err := b.tx.tx.QueryContext(b.ctx,
		`SELECT l.item_id, l.file_id, l.label, l.part FROM item_files l JOIN items i ON i.id = l.item_id WHERE i.library_id = ?`, b.lib)
	if err != nil {
		return err
	}
	for links.Next() {
		var item, file int64
		var label sql.NullString
		var part int
		if err := links.Scan(&item, &file, &label, &part); err != nil {
			links.Close()
			return err
		}
		if e := b.items[item]; e != nil {
			e.links[file] = link{label.String, part}
			b.itemOf[file] = item
			if e.lowest == 0 || file < e.lowest {
				e.lowest = file
			}
		}
	}
	links.Close()
	if err := links.Err(); err != nil {
		return err
	}
	for _, e := range b.items {
		if s := b.items[e.seriesID]; e.typ == "episode" && s != nil && e.lowest != 0 && (s.lowest == 0 || e.lowest < s.lowest) {
			s.lowest = e.lowest
		}
		// Episodes are keyed once their files are known: one without a
		// number by its file.
		if e.typ == "episode" {
			if b.episodes[e.seriesID] == nil {
				b.episodes[e.seriesID] = map[string]*existing{}
			}
			b.episodes[e.seriesID][keyOfExisting(e)] = e
		}
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

func linksOf(files []DesiredFile) map[int64]link {
	out := make(map[int64]link, len(files))
	for _, f := range files {
		out[f.ID] = link{f.Label, f.Part}
	}
	return out
}

func idsOf(files []DesiredFile) []int64 {
	out := make([]int64, len(files))
	for i, f := range files {
		out[i] = f.ID
	}
	slices.Sort(out)
	return out
}

// addedAt is the earliest first sighting of the files.
func (b *builder) addedAt(files []DesiredFile) int64 {
	var at int64
	for _, f := range files {
		if t := b.firstSeen[f.ID]; at == 0 || t < at {
			at = t
		}
	}
	return at
}

// successor finds the existing item of type typ that the files were
// played by and whose own key nothing wants any more, so that the item
// follows its files. Where its files now make several items, it goes with
// the one that holds its lowest file.
func (b *builder) successor(typ string, files []int64, free func(*existing) bool) *existing {
	have := map[int64]bool{}
	for _, f := range files {
		have[f] = true
	}
	for _, f := range files {
		e := b.items[b.itemOf[f]]
		if typ == "series" && e != nil && e.typ == "episode" {
			e = b.items[e.seriesID]
		}
		if e == nil || e.typ != typ || e.matched || !free(e) || (e.lowest != 0 && !have[e.lowest]) {
			continue
		}
		return e
	}
	return nil
}

func (b *builder) build(films []DesiredFilm, series []DesiredSeries) error {
	// First match every desired item to the item it is, then write: an
	// episode can follow its files to another series.
	wantedFilm := map[string]bool{}
	for _, f := range films {
		wantedFilm[keyOf("movie", f.RootID, f.Key)] = true
	}
	filmItems := make([]*existing, len(films))
	for i, f := range films {
		e := b.byKey[keyOf("movie", f.RootID, f.Key)]
		if e == nil || e.matched {
			e = b.successor("movie", idsOf(f.Files), func(o *existing) bool { return !wantedFilm[keyOf(o.typ, o.rootID, o.key)] })
		}
		if e != nil {
			e.matched = true
		}
		filmItems[i] = e
	}

	wantedSeries := map[string]bool{}
	for _, s := range series {
		wantedSeries[keyOf("series", s.RootID, s.Key)] = true
	}
	seriesItems := make([]*existing, len(series))
	for i, s := range series {
		e := b.byKey[keyOf("series", s.RootID, s.Key)]
		if e == nil || e.matched {
			var all []int64
			for _, ep := range s.Episodes {
				all = append(all, idsOf(ep.Files)...)
			}
			slices.Sort(all)
			e = b.successor("series", all, func(o *existing) bool { return !wantedSeries[keyOf(o.typ, o.rootID, o.key)] })
		}
		if e != nil {
			e.matched = true
		}
		seriesItems[i] = e
	}

	wantedEpisode := map[string]bool{} // series id | episode key, for series that exist
	for i, s := range series {
		if si := seriesItems[i]; si != nil {
			for _, ep := range s.Episodes {
				wantedEpisode[strconv.FormatInt(si.id, 10)+"|"+keyOfDesired(ep)] = true
			}
		}
	}
	free := func(o *existing) bool {
		return !wantedEpisode[strconv.FormatInt(o.seriesID, 10)+"|"+keyOfExisting(o)]
	}
	episodeItems := make([][]*existing, len(series))
	for i, s := range series {
		episodeItems[i] = make([]*existing, len(s.Episodes))
		for j, ep := range s.Episodes {
			var e *existing
			if si := seriesItems[i]; si != nil {
				if e = b.episodes[si.id][keyOfDesired(ep)]; e != nil && e.matched {
					e = nil
				}
			}
			if e == nil {
				e = b.successor("episode", idsOf(ep.Files), free)
			}
			if e != nil {
				e.matched = true
			}
			episodeItems[i][j] = e
		}
	}

	// In a mixed library a folder read now as the other kind makes a new
	// item, and the item it made goes below. Its folder is cleared first,
	// so that the two never hold one key at once.
	for _, id := range slices.Sorted(maps.Keys(b.items)) {
		e := b.items[id]
		if e.matched || e.key == "" ||
			!(e.typ == "movie" && wantedSeries[keyOf("series", e.rootID, e.key)]) &&
				!(e.typ == "series" && wantedFilm[keyOf("movie", e.rootID, e.key)]) {
			continue
		}
		if _, err := b.tx.tx.ExecContext(b.ctx, `UPDATE items SET path_key = NULL WHERE id = ?`, e.id); err != nil {
			return err
		}
	}

	for i, f := range films {
		if _, err := b.put(filmItems[i], fields{typ: "movie", rootID: f.RootID, key: f.Key, title: f.Title,
			sort: b.sortTitle(f.Title), year: f.Year, addedAt: b.addedAt(f.Files), links: linksOf(f.Files)}); err != nil {
			return err
		}
	}

	for i, s := range series {
		// A series and a season are as new as their newest episode, so a new
		// episode brings its series to the top of Recently added.
		var newest int64
		seasonNewest := map[int]int64{}
		for _, ep := range s.Episodes {
			at := b.addedAt(ep.Files)
			newest = max(newest, at)
			seasonNewest[ep.Season] = max(seasonNewest[ep.Season], at)
		}
		numbering := "seasons"
		if s.Absolute {
			numbering = "absolute"
		}
		seriesID, err := b.put(seriesItems[i], fields{typ: "series", rootID: s.RootID, key: s.Key, title: s.Title,
			sort: b.sortTitle(s.Title), year: s.Year, numbering: numbering, addedAt: newest})
		if err != nil {
			return err
		}
		seasonIDs := map[int]int64{}
		for _, n := range slices.Sorted(maps.Keys(seasonNewest)) {
			var e *existing
			if seriesItems[i] != nil {
				if e = b.seasons[seriesItems[i].id][n]; e != nil && e.matched {
					e = nil
				}
			}
			if e != nil {
				e.matched = true
			}
			title := seasonTitle(n)
			id, err := b.put(e, fields{typ: "season", seriesID: seriesID, parentID: seriesID, season: n,
				title: title, sort: b.sortTitle(title), addedAt: seasonNewest[n]})
			if err != nil {
				return err
			}
			if e == nil {
				b.touched[seriesID] = true
			}
			seasonIDs[n] = id
		}
		for j, ep := range s.Episodes {
			e := episodeItems[i][j]
			parent := seasonIDs[ep.Season]
			if e == nil {
				b.touched[parent], b.touched[seriesID] = true, true
			} else if e.parentID != parent || e.seriesID != seriesID {
				b.touched[e.parentID], b.touched[e.seriesID] = true, true
				b.touched[parent], b.touched[seriesID] = true, true
			}
			title := episodeTitle(ep)
			if _, err := b.put(e, fields{typ: "episode", seriesID: seriesID, parentID: parent, season: ep.Season,
				episode: ep.Episode, end: ep.EpisodeEnd, absolute: ep.Absolute, unnumbered: ep.Unnumbered, order: ep.Order,
				title: title, sort: b.sortTitle(title),
				addedAt: b.addedAt(ep.Files), links: linksOf(ep.Files)}); err != nil {
				return err
			}
		}
	}

	// What no file makes any more goes, children before their parents, so
	// that nothing is deleted by a cascade without its tombstone.
	for _, typ := range []string{"episode", "season", "series", "movie"} {
		for _, id := range slices.Sorted(maps.Keys(b.items)) {
			if e := b.items[id]; e.typ == typ && !e.matched {
				if typ == "episode" {
					b.touched[e.parentID], b.touched[e.seriesID] = true, true
				}
				if err := b.remove(e); err != nil {
					return err
				}
			}
		}
	}

	// A season or a series whose episodes came or went has new counts.
	for _, id := range slices.Sorted(maps.Keys(b.touched)) {
		if e := b.items[id]; id != 0 && !b.written[id] && (e == nil || !e.removed()) {
			if err := b.stamp(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func seasonTitle(n int) string {
	if n == 0 {
		return "Specials"
	}
	return "Season " + strconv.Itoa(n)
}

func episodeTitle(ep DesiredEpisode) string {
	if ep.Unnumbered {
		return ep.Title
	}
	if ep.EpisodeEnd > ep.Episode {
		return "Episodes " + strconv.Itoa(ep.Episode) + "–" + strconv.Itoa(ep.EpisodeEnd)
	}
	if ep.Absolute != 0 {
		return "Episode " + strconv.Itoa(ep.Absolute)
	}
	return "Episode " + strconv.Itoa(ep.Episode)
}

// removed reports whether the item was deleted in this build.
func (e *existing) removed() bool { return e.typ == "" }

func (b *builder) stamp(id int64) error {
	seq, err := b.tx.Seq(b.ctx)
	if err != nil {
		return err
	}
	_, err = b.tx.tx.ExecContext(b.ctx, `UPDATE items SET updated_seq = ? WHERE id = ?`, seq, id)
	b.written[id] = true
	return err
}

// put writes an item: a new one when e is nil, otherwise the existing one
// where it differs. It returns the item's id.
func (b *builder) put(e *existing, f fields) (int64, error) {
	if f.links == nil {
		f.links = map[int64]link{}
	}
	if e == nil {
		seq, err := b.tx.Seq(b.ctx)
		if err != nil {
			return 0, err
		}
		id, err := b.tx.newID(b.ctx, "last_item_id", "items")
		if err != nil {
			return 0, err
		}
		if _, err := b.tx.tx.ExecContext(b.ctx,
			`INSERT INTO items (id, library_id, type, root_id, path_key, parent_id, series_id, season_number, episode_number,
			   episode_number_end, absolute_number, numbering, title, sort_title, year, added_at, updated_seq, episode_order)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, b.lib, f.typ, nullInt(f.rootID), nullString(f.key), nullInt(f.parentID), nullInt(f.seriesID),
			seasonOf(f), episodeOf(f), nullInt(int64(f.end)), nullInt(int64(f.absolute)), nullString(f.numbering),
			f.title, f.sort, nullInt(int64(f.year)), f.addedAt, seq, orderOf(f)); err != nil {
			return 0, err
		}
		if err := b.index(f.typ, id, f.title, false); err != nil {
			return 0, err
		}
		if err := b.link(id, nil, f.links); err != nil {
			return 0, err
		}
		b.written[id] = true
		b.res.Added++
		return id, nil
	}

	same := e.typ == f.typ && e.rootID == f.rootID && e.key == f.key && e.parentID == f.parentID && e.seriesID == f.seriesID &&
		e.season == f.season && e.episode == f.episode && e.end == f.end && e.absolute == f.absolute &&
		e.unnumbered == f.unnumbered && e.order == f.order &&
		e.numbering == f.numbering && e.title == f.title && e.sort == f.sort && e.year == f.year &&
		e.addedAt == f.addedAt && maps.Equal(e.links, f.links)
	if same {
		return e.id, nil
	}
	seq, err := b.tx.Seq(b.ctx)
	if err != nil {
		return 0, err
	}
	if _, err := b.tx.tx.ExecContext(b.ctx,
		`UPDATE items SET root_id = ?, path_key = ?, parent_id = ?, series_id = ?, season_number = ?, episode_number = ?,
		   episode_number_end = ?, absolute_number = ?, numbering = ?, title = ?, sort_title = ?, year = ?,
		   added_at = ?, updated_seq = ?, episode_order = ? WHERE id = ?`,
		nullInt(f.rootID), nullString(f.key), nullInt(f.parentID), nullInt(f.seriesID), seasonOf(f), episodeOf(f),
		nullInt(int64(f.end)), nullInt(int64(f.absolute)), nullString(f.numbering), f.title, f.sort,
		nullInt(int64(f.year)), f.addedAt, seq, orderOf(f), e.id); err != nil {
		return 0, err
	}
	if e.title != f.title {
		if err := b.index(f.typ, e.id, f.title, true); err != nil {
			return 0, err
		}
	}
	if err := b.link(e.id, e.links, f.links); err != nil {
		return 0, err
	}
	b.written[e.id] = true
	b.res.Changed++
	return e.id, nil
}

// seasonOf is the season number of a season or an episode, where 0 is the
// specials, and NULL for the rest.
func seasonOf(f fields) any {
	if f.typ != "season" && f.typ != "episode" {
		return nil
	}
	return f.season
}

func episodeOf(f fields) any {
	if f.typ != "episode" || f.unnumbered {
		return nil
	}
	return f.episode
}

// orderOf is an episode without a number's episode_order: its decimal, or
// NULL (D51).
func orderOf(f fields) any {
	if !f.unnumbered || f.order == 0 {
		return nil
	}
	return f.order
}

// index keeps the search table in step with an item's title
// (docs/03-data-model.md, section 4, Search index). Seasons, whose titles
// are their numbers, are not indexed.
func (b *builder) index(typ string, id int64, title string, replace bool) error {
	if typ == "season" {
		return nil
	}
	if replace {
		if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM items_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	_, err := b.tx.tx.ExecContext(b.ctx, `INSERT INTO items_fts (rowid, title) VALUES (?, ?)`, id, title)
	return err
}

// link makes an item's files the given ones, with their labels and parts.
func (b *builder) link(id int64, have, want map[int64]link) error {
	for f := range have {
		if _, ok := want[f]; !ok {
			if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM item_files WHERE item_id = ? AND file_id = ?`, id, f); err != nil {
				return err
			}
		}
	}
	for _, f := range slices.Sorted(maps.Keys(want)) {
		w := want[f]
		h, ok := have[f]
		switch {
		case !ok:
			// A file plays one item: take it from the one it played.
			if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM item_files WHERE file_id = ? AND item_id <> ?`, f, id); err != nil {
				return err
			}
			if _, err := b.tx.tx.ExecContext(b.ctx, `INSERT INTO item_files (item_id, file_id, label, part) VALUES (?, ?, ?, ?)`,
				id, f, nullString(w.label), w.part); err != nil {
				return err
			}
		case h != w:
			if _, err := b.tx.tx.ExecContext(b.ctx, `UPDATE item_files SET label = ?, part = ? WHERE item_id = ? AND file_id = ?`,
				nullString(w.label), w.part, id, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// remove deletes an item and writes its tombstone. Its children have gone
// before it; any that are left go with it, each with a tombstone of its own
// written before the item's (docs/03-data-model.md, section 5).
func (b *builder) remove(e *existing) error {
	var ids []int64
	rows, err := b.tx.tx.QueryContext(b.ctx, `SELECT id FROM items WHERE parent_id = ? OR series_id = ? ORDER BY type = 'season', id`, e.id, e.id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ids = append(ids, e.id)
	for _, id := range ids {
		seq, err := b.tx.Seq(b.ctx)
		if err != nil {
			return err
		}
		if _, err := b.tx.tx.ExecContext(b.ctx, `INSERT INTO tombstones (seq, item_id, at) VALUES (?, ?, ?)`, seq, id, b.now); err != nil {
			return err
		}
		if _, err := b.tx.tx.ExecContext(b.ctx, `DELETE FROM items_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		if gone := b.items[id]; gone != nil {
			gone.matched, gone.typ = true, ""
		}
		b.res.Removed++
	}
	_, err = b.tx.tx.ExecContext(b.ctx, `DELETE FROM items WHERE id = ?`, e.id)
	return err
}

// ItemSummary is an item as tests and reports read it back.
type ItemSummary struct {
	ID                   int64
	Type                 string
	RootID               int64
	PathKey              string
	ParentID, SeriesID   int64
	Season, Episode, End int
	Absolute             int
	Numbering            string
	Title                string
	SortTitle            string
	Year                 int
	AddedAt              int64
	UpdatedSeq           int64
	FileIDs              []int64
	Files                []DesiredFile // with their labels and parts, in file order
}

// LibraryItems lists the items of a library in id order, with the files
// that play each.
func (d *DB) LibraryItems(ctx context.Context, libraryID int64) ([]ItemSummary, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT i.id, i.type, i.root_id, i.path_key, i.parent_id, i.series_id, i.season_number, i.episode_number,
		   i.episode_number_end, i.absolute_number, i.numbering, i.title, i.sort_title, i.year, i.added_at, i.updated_seq,
		   (SELECT group_concat(file_id || ':' || part || ':' || COALESCE(label, ''), char(31))
		      FROM (SELECT file_id, part, label FROM item_files WHERE item_id = i.id ORDER BY file_id))
		 FROM items i WHERE i.library_id = ? ORDER BY i.id`, libraryID)
	if err != nil {
		return nil, fmt.Errorf("db: read items: %w", err)
	}
	defer rows.Close()
	var out []ItemSummary
	for rows.Next() {
		var it ItemSummary
		var root, parent, series, season, episode, end, absolute, year sql.NullInt64
		var key, numbering, files sql.NullString
		if err := rows.Scan(&it.ID, &it.Type, &root, &key, &parent, &series, &season, &episode, &end, &absolute,
			&numbering, &it.Title, &it.SortTitle, &year, &it.AddedAt, &it.UpdatedSeq, &files); err != nil {
			return nil, fmt.Errorf("db: read items: %w", err)
		}
		it.RootID, it.PathKey, it.ParentID, it.SeriesID = root.Int64, key.String, parent.Int64, series.Int64
		it.Season, it.Episode, it.End, it.Absolute = int(season.Int64), int(episode.Int64), int(end.Int64), int(absolute.Int64)
		it.Numbering, it.Year = numbering.String, int(year.Int64)
		for _, f := range strings.Split(files.String, "\x1f") {
			idPart, rest, ok := strings.Cut(f, ":")
			if !ok {
				continue
			}
			partStr, label, _ := strings.Cut(rest, ":")
			id, _ := strconv.ParseInt(idPart, 10, 64)
			part, _ := strconv.Atoi(partStr)
			it.FileIDs = append(it.FileIDs, id)
			it.Files = append(it.Files, DesiredFile{ID: id, Label: label, Part: part})
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
	rows, err := d.r.QueryContext(ctx, `SELECT seq, item_id FROM tombstones WHERE seq > ? ORDER BY seq, item_id LIMIT ?`, since, limit)
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
