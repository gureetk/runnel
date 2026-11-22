// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
)

// Work is what the API asks of the background queue.
type Work interface {
	// Paused reports whether a kind of work waits because a provider
	// asked the server to.
	Paused(kind string) bool
	// Wake tells the queue that tasks were queued.
	Wake()
}

// workKinds are the kinds of background work (docs/06-library-and-metadata.md,
// section 9).
var workKinds = map[string]bool{"probe": true, "match": true, "fetch": true, "image": true, "sidecar": true,
	"extract": true, "import": true}

type workBody struct {
	Kind      string  `json:"kind"`
	LibraryID *int64  `json:"library_id"`
	State     string  `json:"state"`
	PausedBy  *string `json:"paused_by"`
	Done      int     `json:"done"`
	Total     int     `json:"total"`
	Failed    int     `json:"failed"`
	Error     *string `json:"error"`
	Since     int64   `json:"since"`
}

// handleTasks is GET /admin/tasks: one entry for each kind of work in each
// library (docs/04-api.md, section 8.3).
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	work, err := s.db.WorkOf(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	playback := !s.gate.Open()
	body := struct {
		Work []workBody `json:"work"`
	}{Work: []workBody{}}
	for _, wk := range work {
		b := workBody{Kind: wk.Kind, LibraryID: int64OrNil(wk.LibraryID), Done: wk.Done, Total: wk.Total, Failed: wk.Failed,
			Error: strOrNil(wk.Error), Since: wk.Since}
		var by string
		switch {
		case wk.Queued+wk.Running == 0:
			b.State = "failed"
		case playback:
			b.State, by = "paused", "playback"
		case s.work != nil && s.work.Paused(wk.Kind):
			b.State, by = "paused", "provider"
		case wk.Running > 0:
			b.State = "running"
		default:
			b.State = "queued"
		}
		b.PausedBy = strOrNil(by)
		body.Work = append(body.Work, b)
	}
	writeJSON(w, http.StatusOK, body)
}

type workRequest struct {
	Kind      string `json:"kind"`
	LibraryID *int64 `json:"library_id"`
}

// handleWork is POST /admin/tasks/cancel, which drops what is queued and
// what failed of a kind in a library, and POST /admin/tasks/retry, which
// queues what failed again. Both answer how many tasks they dropped or
// queued.
func (s *Server) handleWork(retry bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req workRequest
		if !s.decode(w, r, &req) {
			return
		}
		if !workKinds[req.Kind] {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "kind is probe, match, fetch, image, sidecar, extract or import")
			return
		}
		library := int64(0)
		if req.LibraryID != nil {
			library = *req.LibraryID
		}
		var n int
		var err error
		if retry {
			n, err = s.db.RetryWork(r.Context(), req.Kind, library, s.now().UnixMilli())
		} else {
			n, err = s.db.CancelWork(r.Context(), req.Kind, library)
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if retry && n > 0 && s.work != nil {
			s.work.Wake()
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": n})
	}
}
