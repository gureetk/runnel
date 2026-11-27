// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gureetk/runnel/internal/play"
)

// Encoders is what the server found it can encode (docs/05-playback.md,
// section 8).
type Encoders interface {
	// Last is the last check's result: no rows before the first.
	Last() play.Result
	// Check runs the check again, or refuses with play.ErrBusy while a
	// transcode runs.
	Check(ctx context.Context) (play.Result, error)
}

type encoderRow struct {
	V       string  `json:"v"`
	Depth   int     `json:"depth"`
	How     string  `json:"how"`
	Encoder string  `json:"encoder"`
	Works   bool    `json:"works"`
	Error   *string `json:"error"`
}

type encodersBody struct {
	CheckedAt *int64       `json:"checked_at"`
	HW        string       `json:"hw"`
	Encoders  []encoderRow `json:"encoders"`
}

func encodersJSON(res play.Result) encodersBody {
	b := encodersBody{HW: res.HW(), Encoders: []encoderRow{}}
	if !res.CheckedAt.IsZero() {
		at := res.CheckedAt.UnixMilli()
		b.CheckedAt = &at
	}
	for _, r := range res.Rows {
		row := encoderRow{V: r.V, Depth: r.Depth, How: r.How, Encoder: r.Encoder, Works: r.Works}
		if r.Error != "" {
			e := r.Error
			row.Error = &e
		}
		b.Encoders = append(b.Encoders, row)
	}
	return b
}

// lastEncoders is the last check's result; nothing without Encoders.
func (s *Server) lastEncoders() play.Result {
	if s.encoders == nil {
		return play.Result{}
	}
	return s.encoders.Last()
}

// handleEncoders is GET /admin/encoders (docs/04-api.md, section 8.7).
func (s *Server) handleEncoders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, encodersJSON(s.lastEncoders()))
}

// handleCheckEncoders is POST /admin/encoders/check: the check again, which
// takes a few seconds, and its result.
func (s *Server) handleCheckEncoders(w http.ResponseWriter, r *http.Request) {
	if s.encoders == nil {
		writeJSON(w, http.StatusOK, encodersJSON(play.Result{}))
		return
	}
	res, err := s.encoders.Check(r.Context())
	if errors.Is(err, play.ErrBusy) {
		s.fail(w, r, http.StatusServiceUnavailable, "busy", "a transcode is running: check again when it ends")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	attrs := []any{"hw", res.HW()}
	for _, row := range res.Rows {
		if row.Works {
			attrs = append(attrs, play.RowName(row), "works")
		} else {
			attrs = append(attrs, play.RowName(row), row.Error)
		}
	}
	s.log.Info("encoders checked", attrs...)
	writeJSON(w, http.StatusOK, encodersJSON(res))
}
