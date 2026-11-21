// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ExternalTrack is a subtitle or audio file beside a video, as its name
// says (docs/06-library-and-metadata.md, section 4.7).
type ExternalTrack struct {
	ID              int64 // 0 until stored
	RelPath         string
	Kind            string // "subtitle" or "audio"
	Codec           string
	Lang            string
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool
	Size            int64
	MtimeNS         int64
}

// same reports whether two tracks differ in nothing stored.
func (t ExternalTrack) same(o ExternalTrack) bool {
	t.ID, o.ID = 0, 0
	return t == o
}

// SyncTracks makes the tracks of a root's videos those a scan found,
// keyed by the video's path below the root, for the videos the scan
// looked at: those for which inScope is true. A track that stays keeps its
// id; nothing is written where nothing differs. The items of a video whose
// tracks changed take a new sequence value.
func (d *DB) SyncTracks(ctx context.Context, rootID int64, inScope func(rel string) bool, found map[string][]ExternalTrack) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		videos := map[string]int64{}
		rows, err := tx.tx.QueryContext(ctx, `SELECT id, rel_path FROM files WHERE root_id = ? AND kind = 'video'`, rootID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var rel string
			if err := rows.Scan(&id, &rel); err != nil {
				rows.Close()
				return err
			}
			if inScope(rel) {
				videos[rel] = id
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		have := map[int64]map[string]ExternalTrack{}
		rows, err = tx.tx.QueryContext(ctx,
			`SELECT t.id, t.file_id, t.rel_path, t.kind, t.codec, t.lang, t.title, t.is_default, t.is_forced, t.is_hearing_impaired, t.size, t.mtime_ns
			 FROM external_tracks t JOIN files f ON f.id = t.file_id WHERE f.root_id = ?`, rootID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t ExternalTrack
			var file int64
			var codec, lang, title sql.NullString
			if err := rows.Scan(&t.ID, &file, &t.RelPath, &t.Kind, &codec, &lang, &title, &t.Default, &t.Forced, &t.HearingImpaired, &t.Size, &t.MtimeNS); err != nil {
				rows.Close()
				return err
			}
			t.Codec, t.Lang, t.Title = codec.String, lang.String, title.String
			if have[file] == nil {
				have[file] = map[string]ExternalTrack{}
			}
			have[file][t.RelPath] = t
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		var changed []int64
		for rel, file := range videos {
			want := map[string]ExternalTrack{}
			for _, t := range found[rel] {
				want[t.RelPath] = t
			}
			dirty := false
			for p, h := range have[file] {
				if _, ok := want[p]; !ok {
					if _, err := tx.tx.ExecContext(ctx, `DELETE FROM external_tracks WHERE id = ?`, h.ID); err != nil {
						return err
					}
					dirty = true
				}
			}
			for p, w := range want {
				h, ok := have[file][p]
				switch {
				case !ok:
					if _, err := tx.tx.ExecContext(ctx,
						`INSERT INTO external_tracks (file_id, rel_path, kind, codec, lang, title, is_default, is_forced, is_hearing_impaired, size, mtime_ns)
						 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						file, w.RelPath, w.Kind, nullString(w.Codec), nullString(w.Lang), nullString(w.Title), w.Default, w.Forced, w.HearingImpaired, w.Size, w.MtimeNS); err != nil {
						return err
					}
					dirty = true
				case !h.same(w):
					if _, err := tx.tx.ExecContext(ctx,
						`UPDATE external_tracks SET kind = ?, codec = ?, lang = ?, title = ?, is_default = ?, is_forced = ?, is_hearing_impaired = ?, size = ?, mtime_ns = ?
						 WHERE id = ?`,
						w.Kind, nullString(w.Codec), nullString(w.Lang), nullString(w.Title), w.Default, w.Forced, w.HearingImpaired, w.Size, w.MtimeNS, h.ID); err != nil {
						return err
					}
					dirty = true
				}
			}
			if dirty {
				changed = append(changed, file)
			}
		}
		return bumpItemsOfFiles(ctx, tx, changed)
	})
	if err != nil {
		return fmt.Errorf("db: tracks of root %d: %w", rootID, err)
	}
	return nil
}

// tracksOf lists a file's external tracks, by path.
func (d *DB) tracksOf(ctx context.Context, fileID int64) ([]ExternalTrack, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT id, rel_path, kind, codec, lang, title, is_default, is_forced, is_hearing_impaired, size, mtime_ns
		 FROM external_tracks WHERE file_id = ? ORDER BY rel_path, id`, fileID)
	if err != nil {
		return nil, fmt.Errorf("db: read tracks: %w", err)
	}
	defer rows.Close()
	var out []ExternalTrack
	for rows.Next() {
		var t ExternalTrack
		var codec, lang, title sql.NullString
		if err := rows.Scan(&t.ID, &t.RelPath, &t.Kind, &codec, &lang, &title, &t.Default, &t.Forced, &t.HearingImpaired, &t.Size, &t.MtimeNS); err != nil {
			return nil, err
		}
		t.Codec, t.Lang, t.Title = codec.String, lang.String, title.String
		out = append(out, t)
	}
	return out, rows.Err()
}

// TrackFile is what serving a track file needs.
type TrackFile struct {
	Path      string // joined to its root, and checked to be inside it
	Available bool   // its video is not missing, and its root is not away
}

// TrackFileOf finds an external track of a file, or ErrNotFound when the
// file has no such track. The stored path is joined to its root and
// checked to be inside it before anything opens it.
func (d *DB) TrackFileOf(ctx context.Context, fileID, trackID int64) (TrackFile, error) {
	var root, rel string
	var t TrackFile
	err := d.r.QueryRowContext(ctx,
		`SELECT r.path, t.rel_path, f.missing_since IS NULL AND r.available = 1
		 FROM external_tracks t JOIN files f ON f.id = t.file_id JOIN roots r ON r.id = f.root_id
		 WHERE t.id = ? AND t.file_id = ?`, trackID, fileID).Scan(&root, &rel, &t.Available)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackFile{}, ErrNotFound
	}
	if err != nil {
		return TrackFile{}, fmt.Errorf("db: read track: %w", err)
	}
	t.Path = filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(t.Path, strings.TrimSuffix(root, "/")+"/") {
		return TrackFile{}, ErrOutsideRoot
	}
	return t, nil
}
