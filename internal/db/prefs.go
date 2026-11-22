// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Prefs is a track choice (docs/04-api.md, section 7.8). An empty string
// is null. A series' choice without a SubMode was reset: the series
// follows the user's defaults.
type Prefs struct {
	AudioLang string
	SubLang   string
	SubMode   string // off, forced, auto or on
}

// SeriesPrefs is a series' own choice, with the series' title.
type SeriesPrefs struct {
	SeriesID int64
	Title    string
	Prefs
}

// SetSeriesPrefs stores a user's choice for a series, stamped with a
// sequence value so that /changes carries it. Prefs without a SubMode
// reset it: the row stays, with all three null (docs/03-data-model.md,
// section 4). A choice that is already stored writes nothing.
// ErrNotFound: no such series.
func (d *DB) SetSeriesPrefs(ctx context.Context, userID, seriesID int64, p Prefs) error {
	if p.SubMode == "" {
		p = Prefs{}
	}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var typ string
		err := tx.tx.QueryRowContext(ctx, `SELECT type FROM items WHERE id = ?`, seriesID).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && typ != "series") {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var audio, sub, mode sql.NullString
		err = tx.tx.QueryRowContext(ctx, `SELECT audio_lang, sub_lang, sub_mode FROM series_prefs WHERE user_id = ? AND series_id = ?`,
			userID, seriesID).Scan(&audio, &sub, &mode)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if p == (Prefs{}) {
				return nil // nothing to reset
			}
		case err != nil:
			return err
		case p == Prefs{audio.String, sub.String, mode.String}:
			return nil
		}
		seq, err := tx.Seq(ctx)
		if err != nil {
			return err
		}
		_, err = tx.tx.ExecContext(ctx, `INSERT INTO series_prefs (user_id, series_id, audio_lang, sub_lang, sub_mode, updated_seq)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (user_id, series_id) DO UPDATE SET audio_lang = excluded.audio_lang, sub_lang = excluded.sub_lang,
			  sub_mode = excluded.sub_mode, updated_seq = excluded.updated_seq`,
			userID, seriesID, nullString(p.AudioLang), nullString(p.SubLang), nullString(p.SubMode), seq)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("db: store a series' track choice: %w", err)
	}
	return nil
}

// SetDefaultPrefs stores a user's track defaults. /changes does not carry
// them (docs/04-api.md, section 7.8), but a detail's prefs show them, so a
// change takes a sequence value, on no row, and the ETags of details move.
func (d *DB) SetDefaultPrefs(ctx context.Context, userID int64, p Prefs) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `UPDATE users SET audio_lang = ?, sub_lang = ?, sub_mode = ?
			WHERE id = ? AND (audio_lang IS NOT ? OR sub_lang IS NOT ? OR sub_mode IS NOT ?)`,
			nullString(p.AudioLang), nullString(p.SubLang), p.SubMode, userID,
			nullString(p.AudioLang), nullString(p.SubLang), p.SubMode)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			_, err = tx.Seq(ctx)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("db: store track defaults: %w", err)
	}
	return nil
}

// PrefsFor is the track choice a user starts with: the user's choice for
// the series if there is one, otherwise the user's defaults. A series of
// 0, for a film, is the defaults.
func (d *DB) PrefsFor(ctx context.Context, userID, seriesID int64) (Prefs, error) {
	var own, audio, sub, mode sql.NullString
	var p Prefs
	var dAudio, dSub sql.NullString
	err := d.r.QueryRowContext(ctx, `SELECT p.sub_mode, p.audio_lang, p.sub_lang, p.sub_mode, u.audio_lang, u.sub_lang, u.sub_mode
		FROM users u LEFT JOIN series_prefs p ON p.user_id = u.id AND p.series_id = ? WHERE u.id = ?`, seriesID, userID).
		Scan(&own, &audio, &sub, &mode, &dAudio, &dSub, &p.SubMode)
	if errors.Is(err, sql.ErrNoRows) {
		return Prefs{}, ErrNotFound
	}
	if err != nil {
		return Prefs{}, fmt.Errorf("db: read track choice: %w", err)
	}
	if own.Valid {
		return Prefs{AudioLang: audio.String, SubLang: sub.String, SubMode: mode.String}, nil
	}
	p.AudioLang, p.SubLang = dAudio.String, dSub.String
	return p, nil
}

// AllPrefs is a user's track defaults and every series with a choice of
// its own, by title.
func (d *DB) AllPrefs(ctx context.Context, userID int64) (Prefs, []SeriesPrefs, error) {
	defaults, err := d.PrefsFor(ctx, userID, 0)
	if err != nil {
		return Prefs{}, nil, err
	}
	rows, err := d.r.QueryContext(ctx, `SELECT p.series_id, i.title, p.audio_lang, p.sub_lang, p.sub_mode
		FROM series_prefs p JOIN items i ON i.id = p.series_id
		WHERE p.user_id = ? AND p.sub_mode IS NOT NULL ORDER BY i.sort_title, i.id`, userID)
	if err != nil {
		return Prefs{}, nil, fmt.Errorf("db: read track choices: %w", err)
	}
	defer rows.Close()
	out := []SeriesPrefs{}
	for rows.Next() {
		var s SeriesPrefs
		var audio, sub sql.NullString
		if err := rows.Scan(&s.SeriesID, &s.Title, &audio, &sub, &s.SubMode); err != nil {
			return Prefs{}, nil, err
		}
		s.AudioLang, s.SubLang = audio.String, sub.String
		out = append(out, s)
	}
	return defaults, out, rows.Err()
}
