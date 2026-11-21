// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gureetk/runnel/internal/model"
)

// Card is the short form of an item, as far as stage 1 knows it
// (docs/04-api.md, section 2.1).
type Card struct {
	ID          int64
	Type        string
	LibraryID   int64
	RootID      int64 // films and series
	Title       string
	SortTitle   string
	Year        int
	SeriesID    int64 // episodes
	SeriesTitle string
	Season      int
	Episode     int
	AddedAt     int64
	Seq         int64
}

const cardColumns = `i.id, i.type, i.library_id, i.root_id, i.title, i.sort_title, i.year, i.series_id,
  (SELECT s.title FROM items s WHERE s.id = i.series_id), i.season_number, i.episode_number, i.added_at, i.updated_seq`

func scanCards(rows *sql.Rows) ([]Card, error) {
	defer rows.Close()
	var out []Card
	for rows.Next() {
		var c Card
		var root, year, series, season, episode sql.NullInt64
		var seriesTitle sql.NullString
		if err := rows.Scan(&c.ID, &c.Type, &c.LibraryID, &root, &c.Title, &c.SortTitle, &year, &series,
			&seriesTitle, &season, &episode, &c.AddedAt, &c.Seq); err != nil {
			return nil, err
		}
		c.RootID, c.Year, c.SeriesID, c.SeriesTitle = root.Int64, int(year.Int64), series.Int64, seriesTitle.String
		c.Season, c.Episode = int(season.Int64), int(episode.Int64)
		out = append(out, c)
	}
	return out, rows.Err()
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
	Specials        bool
	Season, Episode int
	ID              int64
}

// EpisodesOfSeries is a page of a series' episodes after key.
func (d *DB) EpisodesOfSeries(ctx context.Context, seriesID int64, after EpisodeKey, limit int) ([]Card, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT `+cardColumns+` FROM items i
		WHERE i.series_id = ? AND i.type = 'episode'
		  AND (i.season_number = 0, i.season_number, i.episode_number, i.id) > (?, ?, ?, ?)
		ORDER BY i.season_number = 0, i.season_number, i.episode_number, i.id LIMIT ?`,
		seriesID, after.Specials, after.Season, after.Episode, after.ID, limit)
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
}

// ItemFiles lists the files that play an item, with their streams and
// chapters. A file not probed yet, or that ffprobe could not read, is
// left out: it is not offered for playback
// (docs/06-library-and-metadata.md, section 3).
func (d *DB) ItemFiles(ctx context.Context, itemID int64) ([]FileDetail, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT f.id, l.label, l.part, f.missing_since IS NULL AND r.available = 1, f.container, f.size,
		   f.duration_ms, f.bit_rate, f.attachments_bytes
		 FROM item_files l JOIN files f ON f.id = l.file_id JOIN roots r ON r.id = f.root_id
		 WHERE l.item_id = ? AND f.probe_version > 0 AND f.probe_error IS NULL
		 ORDER BY l.part, f.id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("db: read item files: %w", err)
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
	}
	return out, nil
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
