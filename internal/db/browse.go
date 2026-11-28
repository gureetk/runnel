// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gureetk/runnel/internal/model"
)

// Card is the short form of an item (docs/04-api.md, section 2.1), as far
// as it is the same for everyone: what belongs to one user is the API's.
type Card struct {
	ID          int64
	Type        string
	LibraryID   int64
	RootID      int64 // films and series
	Title       string
	SortTitle   string
	Year        int
	ParentID    int64 // seasons and episodes
	SeriesID    int64
	SeriesTitle string
	Season      int
	Episode     int
	// Unnumbered is an episode without a number (D51), whose Episode is 0.
	Unnumbered bool
	// Place is where an episode stands in its season: its number, the
	// decimal of one without a number whose name gives one, or after every
	// number (episodePlace).
	Place      float64
	EpisodeEnd int // a file that holds several episodes
	Absolute   int // the running number, in a series numbered in one
	Available  bool
	Episodes   int // a series: its episodes outside the specials; a season: its episodes
	Seasons    int // a series: its seasons outside the specials
	Images     map[string]CardImage
	AddedAt    int64
	Seq        int64

	// What metadata brings (docs/06-library-and-metadata.md, section 6.3):
	// empty, or zero, until it has arrived. Genres are a film's or a
	// series'. OriginalTitle is shown on a detail only.
	Overview, Date, ContentRating, OriginalTitle string
	RuntimeMS                                    int64
	Rating                                       float64
	Genres                                       []string
	// CollectionID is a film's collection, when its library shows one.
	// A collection's count of films is in Episodes.
	CollectionID int64
}

// CardImage is an image as a card shows it: its key, the widths stored,
// its aspect ratio and its placeholder colour.
type CardImage struct {
	Key    string   `json:"key"`
	W      []int    `json:"w"`
	Aspect *float64 `json:"ar"`
	Color  *string  `json:"color"`
}

// cardColumns reads a card. available is whether any of the item's own
// files is there, or for a series or a season any of its episodes'. A
// folder that is away does not change it: that is said once, for the
// folder (docs/04-api.md, section 2.1).
const cardColumns = `i.id, i.type, i.library_id, i.root_id, i.title, i.sort_title, i.year, i.parent_id, i.series_id,
  (SELECT s.title FROM items s WHERE s.id = i.series_id), i.season_number, i.episode_number,
  i.episode_number_end, i.absolute_number, COALESCE(i.episode_number, i.episode_order, 1e9),
  CASE i.type
    WHEN 'series' THEN EXISTS (SELECT 1 FROM items e JOIN item_files l ON l.item_id = e.id JOIN files f ON f.id = l.file_id
                               WHERE e.series_id = i.id AND e.type = 'episode' AND f.missing_since IS NULL)
    WHEN 'season' THEN EXISTS (SELECT 1 FROM items e JOIN item_files l ON l.item_id = e.id JOIN files f ON f.id = l.file_id
                               WHERE e.parent_id = i.id AND f.missing_since IS NULL)
    WHEN 'collection' THEN EXISTS (SELECT 1 FROM items e JOIN item_files l ON l.item_id = e.id JOIN files f ON f.id = l.file_id
                               WHERE e.collection_id = i.id AND f.missing_since IS NULL)
    ELSE EXISTS (SELECT 1 FROM item_files l JOIN files f ON f.id = l.file_id WHERE l.item_id = i.id AND f.missing_since IS NULL)
  END,
  CASE i.type
    WHEN 'series' THEN (SELECT count(*) FROM items e WHERE e.series_id = i.id AND e.type = 'episode' AND e.season_number > 0)
    WHEN 'season' THEN (SELECT count(*) FROM items e WHERE e.parent_id = i.id AND e.type = 'episode')
    WHEN 'collection' THEN (SELECT count(*) FROM items e WHERE e.collection_id = i.id)
    ELSE 0 END,
  CASE i.type
    WHEN 'series' THEN (SELECT count(*) FROM items e WHERE e.series_id = i.id AND e.type = 'season' AND e.season_number > 0)
    ELSE 0 END,
  (SELECT json_group_object(im.kind, json_object('key', im.key, 'w', json(im.widths), 'ar', im.aspect, 'color', im.color))
     FROM images im WHERE im.item_id = i.id AND im.rank = 0),
  i.added_at, i.updated_seq,
  i.overview, i.release_date, i.content_rating, i.original_title, i.runtime_ms, i.rating,
  CASE WHEN i.type IN ('movie', 'series') THEN (SELECT json_group_array(g.genre) FROM
    (SELECT genre FROM item_genres WHERE item_id = i.id ORDER BY genre) g) END,
  i.collection_id`

func scanCards(rows *sql.Rows) ([]Card, error) { return scanCardsWith(rows, nil) }

// scanCardsWith reads cards from a query of cardColumns followed by one
// more column, which it scans into what extra returns for each row. A nil
// extra reads cardColumns alone.
func scanCardsWith(rows *sql.Rows, extra func() any) ([]Card, error) {
	defer rows.Close()
	var out []Card
	for rows.Next() {
		var e any
		if extra != nil {
			e = extra()
		}
		c, err := scanCard(rows, e)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanCard reads the row a query of cardColumns is on, and the column
// after them into extra when extra is not nil.
func scanCard(rows *sql.Rows, extra any) (Card, error) {
	var c Card
	var root, year, parent, series, season, episode, end, absolute sql.NullInt64
	var seriesTitle, images sql.NullString
	var overview, date, contentRating, original, genres sql.NullString
	var runtime, collection sql.NullInt64
	var rating sql.NullFloat64
	dest := []any{&c.ID, &c.Type, &c.LibraryID, &root, &c.Title, &c.SortTitle, &year, &parent, &series,
		&seriesTitle, &season, &episode, &end, &absolute, &c.Place, &c.Available, &c.Episodes, &c.Seasons, &images, &c.AddedAt, &c.Seq,
		&overview, &date, &contentRating, &original, &runtime, &rating, &genres, &collection}
	if extra != nil {
		dest = append(dest, extra)
	}
	if err := rows.Scan(dest...); err != nil {
		return Card{}, err
	}
	if images.String != "" && images.String != "{}" {
		if err := json.Unmarshal([]byte(images.String), &c.Images); err != nil {
			return Card{}, fmt.Errorf("db: read a card's images: %w", err)
		}
	}
	if genres.String != "" && genres.String != "[]" {
		if err := json.Unmarshal([]byte(genres.String), &c.Genres); err != nil {
			return Card{}, fmt.Errorf("db: read a card's genres: %w", err)
		}
	}
	c.Overview, c.Date, c.ContentRating, c.OriginalTitle = overview.String, date.String, contentRating.String, original.String
	c.RuntimeMS, c.Rating, c.CollectionID = runtime.Int64, rating.Float64, collection.Int64
	c.RootID, c.Year, c.ParentID, c.SeriesID, c.SeriesTitle = root.Int64, int(year.Int64), parent.Int64, series.Int64, seriesTitle.String
	c.Season, c.Episode, c.EpisodeEnd, c.Absolute = int(season.Int64), int(episode.Int64), int(end.Int64), int(absolute.Int64)
	c.Unnumbered = c.Type == "episode" && !episode.Valid
	return c, nil
}

// afterEveryNumber is the place of an episode without a number whose name
// gives no decimal: after the season's numbered episodes, which then go by
// title (D51).
const afterEveryNumber = 1e9

// episodePlace is, as SQL, where the episode a stands in its season: its
// number; for one without a number, the decimal its name gives, which puts
// 8.5 after 8; else after every number. It is never NULL for an episode,
// so that it can stand in a cursor's row value. cardColumns spells it out
// for the alias i.
func episodePlace(a string) string {
	return "COALESCE(" + a + ".episode_number, " + a + ".episode_order, 1e9)"
}

// ItemCounts counts the films and series of each library.
func (d *DB) ItemCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT library_id, count(*) FROM items WHERE type IN ('movie', 'series') GROUP BY library_id`)
	if err != nil {
		return nil, fmt.Errorf("db: count items: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var lib int64
		var n int
		if err := rows.Scan(&lib, &n); err != nil {
			return nil, err
		}
		out[lib] = n
	}
	return out, rows.Err()
}

// ItemsByTitle is a page of a library's films or series, sorted by title,
// after the row (afterTitle, afterID), as docs/03-data-model.md,
// section 6, has it.
func (d *DB) ItemsByTitle(ctx context.Context, libraryID int64, typ, afterTitle string, afterID int64, limit int) ([]Card, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i
		WHERE i.library_id = ? AND i.type = ? AND (i.sort_title, i.id) > (?, ?)
		ORDER BY i.sort_title, i.id LIMIT ?`, libraryID, typ, afterTitle, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: read items: %w", err)
	}
	return scanCards(rows)
}

// CountByType counts a library's items of one type.
func (d *DB) CountByType(ctx context.Context, libraryID int64, typ string) (int, error) {
	var n int
	err := d.r.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE library_id = ? AND type = ?`, libraryID, typ).Scan(&n)
	return n, err
}

// EpisodeKey is where a page of episodes continues: in season and
// episode order, specials last.
type EpisodeKey struct {
	Specials bool
	Season   int
	Place    float64
	Sort     string
	ID       int64
}

// EpisodesOfSeries is a page of a series' episodes after key.
func (d *DB) EpisodesOfSeries(ctx context.Context, seriesID int64, after EpisodeKey, limit int) ([]Card, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i
		WHERE i.series_id = ? AND i.type = 'episode'
		  AND (i.season_number = 0, i.season_number, `+episodePlace("i")+`, i.sort_title, i.id) > (?, ?, ?, ?, ?)
		ORDER BY i.season_number = 0, i.season_number, `+episodePlace("i")+`, i.sort_title, i.id LIMIT ?`,
		seriesID, after.Specials, after.Season, after.Place, after.Sort, after.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: read episodes: %w", err)
	}
	return scanCards(rows)
}

// CountEpisodes counts a series' episodes.
func (d *DB) CountEpisodes(ctx context.Context, seriesID int64) (int, error) {
	var n int
	err := d.r.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE series_id = ? AND type = 'episode'`, seriesID).Scan(&n)
	return n, err
}

// SeasonKey is where a page of seasons continues: in season order,
// specials last.
type SeasonKey struct {
	Specials bool
	Season   int
	ID       int64
}

// SeasonsOf is a page of a series' seasons after key.
func (d *DB) SeasonsOf(ctx context.Context, seriesID int64, after SeasonKey, limit int) ([]Card, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i
		WHERE i.parent_id = ? AND i.type = 'season' AND (i.season_number = 0, i.season_number, i.id) > (?, ?, ?)
		ORDER BY i.season_number = 0, i.season_number, i.id LIMIT ?`, seriesID, after.Specials, after.Season, after.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: read seasons: %w", err)
	}
	return scanCards(rows)
}

// NumberKey is where a page of a season's episodes continues.
type NumberKey struct {
	Place    float64
	Absolute int
	Sort     string
	ID       int64
}

// EpisodesOfSeason is a page of a season's episodes after key, in order.
func (d *DB) EpisodesOfSeason(ctx context.Context, seasonID int64, after NumberKey, limit int) ([]Card, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i
		WHERE i.parent_id = ? AND i.type = 'episode'
		  AND (`+episodePlace("i")+`, COALESCE(i.absolute_number, 0), i.sort_title, i.id) > (?, ?, ?, ?)
		ORDER BY `+episodePlace("i")+`, COALESCE(i.absolute_number, 0), i.sort_title, i.id LIMIT ?`,
		seasonID, after.Place, after.Absolute, after.Sort, after.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("db: read episodes: %w", err)
	}
	return scanCards(rows)
}

// CountChildren counts an item's children of one type.
func (d *DB) CountChildren(ctx context.Context, parentID int64, typ string) (int, error) {
	var n int
	err := d.r.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE parent_id = ? AND type = ?`, parentID, typ).Scan(&n)
	return n, err
}

// Neighbours are the episodes before and after one in its series' order:
// season and place, specials last; an episode without a number is among
// them (D51). An empty card stands for none.
func (d *DB) Neighbours(ctx context.Context, c Card) (prev, next Card, err error) {
	here := []any{c.SeriesID, c.Season == 0, c.Season, c.Place, c.Absolute, c.SortTitle, c.ID}
	for _, q := range []struct {
		cmp, dir string
		out      *Card
	}{{"<", " DESC", &prev}, {">", "", &next}} {
		rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i
			WHERE i.series_id = ? AND i.type = 'episode'
			  AND (i.season_number = 0, i.season_number, `+episodePlace("i")+`, COALESCE(i.absolute_number, 0), i.sort_title, i.id) `+q.cmp+` (?, ?, ?, ?, ?, ?)
			ORDER BY i.season_number = 0`+q.dir+`, i.season_number`+q.dir+`, `+episodePlace("i")+q.dir+`,
			  COALESCE(i.absolute_number, 0)`+q.dir+`, i.sort_title`+q.dir+`, i.id`+q.dir+` LIMIT 1`, here...)
		if err != nil {
			return Card{}, Card{}, fmt.Errorf("db: read neighbours: %w", err)
		}
		cards, err := scanCards(rows)
		if err != nil {
			return Card{}, Card{}, err
		}
		if len(cards) == 1 {
			*q.out = cards[0]
		}
	}
	return prev, next, nil
}

// Match is an item's match to a metadata provider, with its ids there.
type Match struct {
	State       string
	Confidence  float64 // 0 when not known
	Numbering   string  // a series': seasons or absolute
	ExternalIDs map[string]string
}

// MatchOf reads an item's match and its provider ids, or ErrNotFound.
func (d *DB) MatchOf(ctx context.Context, id int64) (Match, error) {
	m := Match{ExternalIDs: map[string]string{}}
	var conf sql.NullFloat64
	var numbering sql.NullString
	err := d.r.QueryRowContext(ctx, `SELECT match_state, match_confidence, numbering FROM items WHERE id = ?`, id).Scan(&m.State, &conf, &numbering)
	if errors.Is(err, sql.ErrNoRows) {
		return Match{}, ErrNotFound
	}
	if err != nil {
		return Match{}, fmt.Errorf("db: read match: %w", err)
	}
	m.Confidence, m.Numbering = conf.Float64, numbering.String
	rows, err := d.r.QueryContext(ctx, `SELECT provider, value FROM external_ids WHERE item_id = ? AND provider <> 'tmdb_collection'`, id)
	if err != nil {
		return Match{}, fmt.Errorf("db: read external ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p, v string
		if err := rows.Scan(&p, &v); err != nil {
			return Match{}, err
		}
		m.ExternalIDs[p] = v
	}
	return m, rows.Err()
}

// Item is one item's card, or ErrNotFound.
func (d *DB) Item(ctx context.Context, id int64) (Card, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i WHERE i.id = ?`, id)
	if err != nil {
		return Card{}, fmt.Errorf("db: read item: %w", err)
	}
	cards, err := scanCards(rows)
	if err != nil {
		return Card{}, err
	}
	if len(cards) == 0 {
		return Card{}, ErrNotFound
	}
	return cards[0], nil
}

// FileDetail is one file of an item, as the detail shows it.
type FileDetail struct {
	ID               int64
	Label            string
	Part             int
	Available        bool
	Container        string
	Size             int64
	DurationMS       int64
	BitRate          int64
	AttachmentsBytes int64
	Streams          []model.Stream
	Chapters         []model.Chapter
	Tracks           []ExternalTrack // files beside it
}

// ItemFiles lists the files that play an item, with their streams and
// chapters. A file not probed yet, or that ffprobe could not read, is
// left out: it is not offered for playback
// (docs/06-library-and-metadata.md, section 3).
func (d *DB) ItemFiles(ctx context.Context, itemID int64) ([]FileDetail, error) {
	out, err := d.fileDetails(ctx,
		`SELECT `+fileColumns+`
		 FROM item_files l JOIN files f ON f.id = l.file_id JOIN roots r ON r.id = f.root_id
		 WHERE l.item_id = ? AND f.probe_version > 0 AND f.probe_error IS NULL
		 ORDER BY l.part, f.id`, itemID)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b FileDetail) int {
		if c := compareLabels(a.Label, b.Label); c != 0 {
			return c
		}
		return cmp.Compare(a.Part, b.Part)
	})
	return out, nil
}

// FileDetailOf is one file as the detail shows it, with the label and
// part it has in its item, or ErrNotFound for a file that does not exist
// or is not offered for playback: an extra, a file not probed yet, or one
// that ffprobe could not read.
func (d *DB) FileDetailOf(ctx context.Context, fileID int64) (FileDetail, error) {
	out, err := d.fileDetails(ctx,
		`SELECT `+fileColumns+`
		 FROM files f JOIN roots r ON r.id = f.root_id LEFT JOIN item_files l ON l.file_id = f.id
		 WHERE f.id = ? AND f.kind = 'video' AND f.probe_version > 0 AND f.probe_error IS NULL
		 LIMIT 1`, fileID)
	if err != nil {
		return FileDetail{}, err
	}
	if len(out) == 0 {
		return FileDetail{}, ErrNotFound
	}
	return out[0], nil
}

// fileColumns reads a FileDetail, from files f, roots r and item_files l.
const fileColumns = `f.id, l.label, COALESCE(l.part, 0), f.missing_since IS NULL AND r.available = 1, f.container, f.size,
  f.duration_ms, f.bit_rate, f.attachments_bytes`

// fileDetails runs a query of fileColumns and reads each file's streams
// and chapters.
func (d *DB) fileDetails(ctx context.Context, query string, args ...any) ([]FileDetail, error) {
	rows, err := d.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: read files: %w", err)
	}
	var out []FileDetail
	for rows.Next() {
		var f FileDetail
		var label, container sql.NullString
		var duration, bitRate sql.NullInt64
		if err := rows.Scan(&f.ID, &label, &f.Part, &f.Available, &container, &f.Size, &duration, &bitRate, &f.AttachmentsBytes); err != nil {
			rows.Close()
			return nil, err
		}
		f.Label, f.Container, f.DurationMS, f.BitRate = label.String, container.String, duration.Int64, bitRate.Int64
		out = append(out, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Streams, err = d.streams(ctx, out[i].ID); err != nil {
			return nil, err
		}
		if out[i].Chapters, err = d.chapters(ctx, out[i].ID); err != nil {
			return nil, err
		}
		if out[i].Tracks, err = d.tracksOf(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// compareLabels orders versions (docs/04-api.md, section 2.2): the main
// version, without a label, first; then labels that name a resolution, a
// number with a p or an i, from the highest; then the others by name.
func compareLabels(a, b string) int {
	rank := func(l string) (int, int) {
		if l == "" {
			return 0, 0
		}
		if n := len(l); n > 1 && strings.ContainsRune("pPiI", rune(l[n-1])) {
			if v, err := strconv.Atoi(l[:n-1]); err == nil {
				return 1, -v
			}
		}
		return 2, 0
	}
	ra, va := rank(a)
	rb, vb := rank(b)
	if ra != rb {
		return cmp.Compare(ra, rb)
	}
	if va != vb {
		return cmp.Compare(va, vb)
	}
	return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
}

func (d *DB) streams(ctx context.Context, fileID int64) ([]model.Stream, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT idx, kind, codec, profile, lang, title, is_default, is_forced, is_hearing_impaired,
		   width, height, pix_fmt, bit_depth, level, fps_num, fps_den, hdr,
		   channels, channel_layout, sample_rate, bit_rate, filename, mime, size
		 FROM streams WHERE file_id = ? ORDER BY idx`, fileID)
	if err != nil {
		return nil, fmt.Errorf("db: read streams: %w", err)
	}
	defer rows.Close()
	var out []model.Stream
	for rows.Next() {
		var s model.Stream
		var codec, profile, lang, title, pix, hdr, layout, filename, mime sql.NullString
		var width, height, depth, level, fpsNum, fpsDen, channels, rate, bitRate, size sql.NullInt64
		if err := rows.Scan(&s.Index, &s.Kind, &codec, &profile, &lang, &title, &s.Default, &s.Forced, &s.HearingImpaired,
			&width, &height, &pix, &depth, &level, &fpsNum, &fpsDen, &hdr,
			&channels, &layout, &rate, &bitRate, &filename, &mime, &size); err != nil {
			return nil, err
		}
		s.Codec, s.Profile, s.Lang, s.Title, s.PixFmt, s.HDR = codec.String, profile.String, lang.String, title.String, pix.String, hdr.String
		s.ChannelLayout, s.Filename, s.MIME = layout.String, filename.String, mime.String
		s.Width, s.Height, s.BitDepth = int(width.Int64), int(height.Int64), int(depth.Int64)
		s.FPSNum, s.FPSDen, s.Channels, s.SampleRate = int(fpsNum.Int64), int(fpsDen.Int64), int(channels.Int64), int(rate.Int64)
		s.BitRate, s.Size = bitRate.Int64, size.Int64
		if level.Valid {
			l := int(level.Int64)
			s.Level = &l
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) chapters(ctx context.Context, fileID int64) ([]model.Chapter, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT idx, start_ms, end_ms, title FROM chapters WHERE file_id = ? ORDER BY idx`, fileID)
	if err != nil {
		return nil, fmt.Errorf("db: read chapters: %w", err)
	}
	defer rows.Close()
	var out []model.Chapter
	for rows.Next() {
		var c model.Chapter
		var end sql.NullInt64
		var title sql.NullString
		if err := rows.Scan(&c.Index, &c.StartMS, &end, &title); err != nil {
			return nil, err
		}
		if end.Valid {
			c.EndMS = &end.Int64
		}
		c.Title = title.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// ErrOutsideRoot is returned for a stored path that would leave its root.
var ErrOutsideRoot = errors.New("db: the file's path leaves its root")

// RawFile is what serving a file as it is on disk needs.
type RawFile struct {
	Path        string // root and relative path joined, checked to be inside the root
	Container   string
	Fingerprint string
	Available   bool // not missing, and its root is not away
}

// RawFileOf finds a file by id, or ErrNotFound. The path from the
// database is joined to its root and checked to be inside it before
// anything opens it (docs/02-architecture.md, section 10).
func (d *DB) RawFileOf(ctx context.Context, id int64) (RawFile, error) {
	var root, rel string
	var f RawFile
	var container, fp sql.NullString
	err := d.r.QueryRowContext(ctx,
		`SELECT r.path, f.rel_path, f.container, f.fingerprint, f.missing_since IS NULL AND r.available = 1
		 FROM files f JOIN roots r ON r.id = f.root_id WHERE f.id = ?`, id).
		Scan(&root, &rel, &container, &fp, &f.Available)
	if errors.Is(err, sql.ErrNoRows) {
		return RawFile{}, ErrNotFound
	}
	if err != nil {
		return RawFile{}, fmt.Errorf("db: read file: %w", err)
	}
	f.Container, f.Fingerprint = container.String, fp.String
	f.Path = filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(f.Path, strings.TrimSuffix(root, "/")+"/") {
		return RawFile{}, ErrOutsideRoot
	}
	return f, nil
}
