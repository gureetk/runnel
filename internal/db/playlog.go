// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// PlayLogKeep is how many rows the start-time log keeps (P7).
const PlayLogKeep = 1000

// PlayStart is what the first progress report of a playback tells about
// its start (docs/04-api.md, section 7.6).
type PlayStart struct {
	At                  int64
	UserID, TokenID     int64
	ItemID, FileID      int64
	Mode                string
	Warmed              bool
	LoadedMS, PlayingMS *int64 // Play pressed to the file loaded, and to the first frame
	Decoder             string
	Hardware            *bool
	DroppedFrames       *int64
	// A transcode's request to its first byte sent, and its encoder.
	ServerFirstByteMS *int64
	Encoder           string
}

// playDetail is the snapshot a row of the start-time log keeps, so that it
// reads the same after the device signs out or the item goes
// (docs/03-data-model.md, section 4).
type playDetail struct {
	User          string `json:"user,omitempty"`
	Device        string `json:"device,omitempty"`
	Client        string `json:"client,omitempty"`
	Title         string `json:"title,omitempty"`
	Decoder       string `json:"decoder,omitempty"`
	Hardware      *bool  `json:"hardware,omitempty"`
	DroppedFrames *int64 `json:"dropped_frames,omitempty"`
	Encoder       string `json:"encoder,omitempty"`
}

// LogPlay writes a row of the start-time log and keeps the newest
// PlayLogKeep rows.
func (d *DB) LogPlay(ctx context.Context, p PlayStart) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		detail := playDetail{Decoder: p.Decoder, Hardware: p.Hardware, DroppedFrames: p.DroppedFrames, Encoder: p.Encoder}
		var user, device, client sql.NullString
		if err := tx.tx.QueryRowContext(ctx, `SELECT (SELECT name FROM users WHERE id = ?),
			  (SELECT device_name FROM tokens WHERE id = ?), (SELECT client FROM tokens WHERE id = ?)`,
			p.UserID, p.TokenID, p.TokenID).Scan(&user, &device, &client); err != nil {
			return err
		}
		detail.User, detail.Device, detail.Client = user.String, device.String, client.String
		var typ, title string
		var series sql.NullString
		var season, episode, end sql.NullInt64
		err := tx.tx.QueryRowContext(ctx, `SELECT i.type, i.title, s.title, i.season_number, i.episode_number, i.episode_number_end
			FROM items i LEFT JOIN items s ON s.id = i.series_id WHERE i.id = ?`, p.ItemID).
			Scan(&typ, &title, &series, &season, &episode, &end)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case typ == "episode":
			detail.Title = loggedEpisode(series.String, season.Int64, episode.Int64, end.Int64)
		default:
			detail.Title = title
		}
		js, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO play_log
			  (at, user_id, item_id, file_id, mode, warmed, server_first_byte_ms, client_loaded_ms, client_playing_ms, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.At, p.UserID, p.ItemID, p.FileID, p.Mode, p.Warmed, p.ServerFirstByteMS, p.LoadedMS, p.PlayingMS, string(js)); err != nil {
			return err
		}
		_, err = tx.tx.ExecContext(ctx, `DELETE FROM play_log WHERE id <= (SELECT MAX(id) FROM play_log) - ?`, PlayLogKeep)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: log a playback's start: %w", err)
	}
	return nil
}

// PlayRow is a row of the start-time log, with its snapshot.
type PlayRow struct {
	ID                int64
	At                int64
	UserID            int64
	ItemID, FileID    int64
	Mode              string
	Warmed            bool
	ServerFirstByteMS *int64
	LoadedMS          *int64
	PlayingMS         *int64
	User, Device      string
	Client, Title     string
	Decoder           string
	Hardware          *bool
	DroppedFrames     *int64
	Encoder           string
}

// PlayLog reads the start-time log, newest first: at most limit rows with
// an id below before, or from the newest when before is 0.
func (d *DB) PlayLog(ctx context.Context, before int64, limit int) ([]PlayRow, error) {
	if before == 0 {
		before = math.MaxInt64
	}
	rows, err := d.r.QueryContext(ctx, `SELECT id, at, user_id, item_id, file_id, mode, warmed,
		  server_first_byte_ms, client_loaded_ms, client_playing_ms, detail
		FROM play_log WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("db: read the start-time log: %w", err)
	}
	defer rows.Close()
	var out []PlayRow
	for rows.Next() {
		var p PlayRow
		var user, item, file sql.NullInt64
		var first, loaded, playing sql.NullInt64
		var detail sql.NullString
		if err := rows.Scan(&p.ID, &p.At, &user, &item, &file, &p.Mode, &p.Warmed, &first, &loaded, &playing, &detail); err != nil {
			return nil, err
		}
		p.UserID, p.ItemID, p.FileID = user.Int64, item.Int64, file.Int64
		p.ServerFirstByteMS, p.LoadedMS, p.PlayingMS = ptrOf(first), ptrOf(loaded), ptrOf(playing)
		var snap playDetail
		if detail.String != "" {
			if err := json.Unmarshal([]byte(detail.String), &snap); err != nil {
				return nil, fmt.Errorf("db: read a start-time row's snapshot: %w", err)
			}
		}
		p.User, p.Device, p.Client, p.Title = snap.User, snap.Device, snap.Client, snap.Title
		p.Decoder, p.Hardware, p.DroppedFrames, p.Encoder = snap.Decoder, snap.Hardware, snap.DroppedFrames, snap.Encoder
		out = append(out, p)
	}
	return out, rows.Err()
}

func ptrOf(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// loggedEpisode is how the start-time log names an episode, as
// docs/04-api.md, section 8.7, shows it: "Harbor Lantern · S1 E5".
func loggedEpisode(series string, season, episode, end int64) string {
	t := series + " · S" + strconv.FormatInt(season, 10) + " E" + strconv.FormatInt(episode, 10)
	if end > episode {
		t += "–" + strconv.FormatInt(end, 10)
	}
	return t
}

// Stats is what GET /admin/stats tells about the database
// (docs/04-api.md, section 8.7).
type Stats struct {
	Size                                              int64 // bytes in the database file, without its write-ahead log
	Seq                                               int64
	Libraries, Movies, Series, Episodes, Files, Users int
}

// StatsOf counts what the database holds.
func (d *DB) StatsOf(ctx context.Context) (Stats, error) {
	var s Stats
	err := d.r.QueryRowContext(ctx, `SELECT
		  (SELECT page_count FROM pragma_page_count()) * (SELECT page_size FROM pragma_page_size()),
		  (SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'seq'),
		  (SELECT count(*) FROM libraries),
		  (SELECT count(*) FROM items WHERE type = 'movie'),
		  (SELECT count(*) FROM items WHERE type = 'series'),
		  (SELECT count(*) FROM items WHERE type = 'episode'),
		  (SELECT count(*) FROM files),
		  (SELECT count(*) FROM users)`).Scan(&s.Size, &s.Seq, &s.Libraries, &s.Movies, &s.Series, &s.Episodes, &s.Files, &s.Users)
	if err != nil {
		return Stats{}, fmt.Errorf("db: count for stats: %w", err)
	}
	return s, nil
}
