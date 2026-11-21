// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"fmt"

	"github.com/gureetk/runnel/internal/model"
)

// ProbeTarget is a file to probe: its id, its path on disk, and the size
// and modification time the database holds for it. A result is stored
// only while those are still the same, so that a probe that raced a
// change on disk is dropped rather than stored over the newer file.
type ProbeTarget struct {
	FileID  int64
	Path    string
	Size    int64
	MtimeNS int64
}

// SaveProbe stores what a probe found: the file's columns, its streams and
// its chapters, all replaced, and the fingerprint. It reports false, and
// stores nothing, when the file changed since it was picked. Items that
// play the file take a new sequence number (docs/03-data-model.md,
// section 5).
func (d *DB) SaveProbe(ctx context.Context, t ProbeTarget, p model.Probe, fingerprint string, version int, now int64) (bool, error) {
	saved := false
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx,
			`UPDATE files SET fingerprint = ?, container = ?, duration_ms = ?, bit_rate = ?,
			   attachments_bytes = ?, probe_version = ?, probe_error = NULL, probed_at = ?
			 WHERE id = ? AND size = ? AND mtime_ns = ?`,
			nullString(fingerprint), nullString(p.Container), nullInt(p.DurationMS), nullInt(p.BitRate),
			p.AttachmentsBytes, version, now, t.FileID, t.Size, t.MtimeNS)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		saved = true
		if err := clearStreams(ctx, tx, t.FileID); err != nil {
			return err
		}
		stmt, err := tx.tx.PrepareContext(ctx,
			`INSERT INTO streams (file_id, idx, kind, codec, profile, lang, title,
			   is_default, is_forced, is_hearing_impaired,
			   width, height, pix_fmt, bit_depth, level, fps_num, fps_den, hdr,
			   channels, channel_layout, sample_rate, bit_rate, filename, mime, size)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, s := range p.Streams {
			var level any
			if s.Level != nil {
				level = *s.Level
			}
			if _, err := stmt.ExecContext(ctx, t.FileID, s.Index, s.Kind,
				nullString(s.Codec), nullString(s.Profile), nullString(s.Lang), nullString(s.Title),
				s.Default, s.Forced, s.HearingImpaired,
				nullInt(int64(s.Width)), nullInt(int64(s.Height)), nullString(s.PixFmt), nullInt(int64(s.BitDepth)),
				level, nullInt(int64(s.FPSNum)), nullInt(int64(s.FPSDen)), nullString(s.HDR),
				nullInt(int64(s.Channels)), nullString(s.ChannelLayout), nullInt(int64(s.SampleRate)), nullInt(s.BitRate),
				nullString(s.Filename), nullString(s.MIME), nullInt(s.Size)); err != nil {
				return fmt.Errorf("stream %d: %w", s.Index, err)
			}
		}
		for _, c := range p.Chapters {
			var end any
			if c.EndMS != nil {
				end = *c.EndMS
			}
			if _, err := tx.tx.ExecContext(ctx,
				`INSERT INTO chapters (file_id, idx, start_ms, end_ms, title) VALUES (?, ?, ?, ?, ?)`,
				t.FileID, c.Index, c.StartMS, end, nullString(c.Title)); err != nil {
				return fmt.Errorf("chapter %d: %w", c.Index, err)
			}
		}
		return bumpItemsOfFile(ctx, tx, t.FileID)
	})
	if err != nil {
		return false, fmt.Errorf("db: save probe of file %d: %w", t.FileID, err)
	}
	return saved, nil
}

// SaveProbeError records that ffprobe could not read the file. The file is
// marked probed with this version, so it is tried again only when its size
// or time changes, or the version is raised
// (docs/06-library-and-metadata.md, section 3).
func (d *DB) SaveProbeError(ctx context.Context, t ProbeTarget, msg, fingerprint string, version int, now int64) (bool, error) {
	saved := false
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx,
			`UPDATE files SET fingerprint = ?, container = NULL, duration_ms = NULL, bit_rate = NULL,
			   attachments_bytes = 0, probe_version = ?, probe_error = ?, probed_at = ?
			 WHERE id = ? AND size = ? AND mtime_ns = ?`,
			nullString(fingerprint), version, msg, now, t.FileID, t.Size, t.MtimeNS)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		saved = true
		if err := clearStreams(ctx, tx, t.FileID); err != nil {
			return err
		}
		return bumpItemsOfFile(ctx, tx, t.FileID)
	})
	if err != nil {
		return false, fmt.Errorf("db: save probe error of file %d: %w", t.FileID, err)
	}
	return saved, nil
}

func clearStreams(ctx context.Context, tx *Tx, fileID int64) error {
	if _, err := tx.tx.ExecContext(ctx, `DELETE FROM streams WHERE file_id = ?`, fileID); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(ctx, `DELETE FROM chapters WHERE file_id = ?`, fileID)
	return err
}

// bumpItemsOfFile stamps the items that play a file with this
// transaction's sequence number: a file change is a change to its items.
func bumpItemsOfFile(ctx context.Context, tx *Tx, fileID int64) error {
	var linked bool
	if err := tx.tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM item_files WHERE file_id = ?)`, fileID).Scan(&linked); err != nil {
		return err
	}
	if !linked {
		return nil
	}
	seq, err := tx.Seq(ctx)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx,
		`UPDATE items SET updated_seq = ? WHERE id IN (SELECT item_id FROM item_files WHERE file_id = ?)`, seq, fileID)
	return err
}

// nullInt stores 0 as NULL: unknown.
func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
