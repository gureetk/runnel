// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/gureetk/runnel/internal/db"
)

// etag sets the ETag of what a request reads now, built from the server's
// instance, the change sequence and the user (docs/04-api.md, section 1),
// and answers 304 when the request's If-None-Match holds it. It reads the
// sequence before the response's data, so that a change made while the
// response is built moves the tag of the next request. It reports whether
// the handler is to go on.
func (s *Server) etag(w http.ResponseWriter, r *http.Request) bool {
	seq, err := s.db.Seq(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	instance := s.info.InstanceID
	if len(instance) > 8 {
		instance = instance[:8]
	}
	tag := `W/"` + instance + "-" + strconv.FormatInt(seq, 10) + "-" + strconv.FormatInt(callerOf(r.Context()).user.ID, 10) + `"`
	h := w.Header()
	h.Set("ETag", tag)
	h.Set("Cache-Control", "private, no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return false
	}
	return true
}

// etagMatches compares the tags of an If-None-Match header with one tag,
// weakly, as a GET is compared.
func etagMatches(header, tag string) bool {
	want := strings.TrimPrefix(tag, "W/")
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || (t != "" && strings.TrimPrefix(t, "W/") == want) {
			return true
		}
	}
	return false
}

type changedStateBody struct {
	ItemID int64 `json:"item_id"`
	stateBody
	Seq int64 `json:"seq"`
}

type changedPrefsBody struct {
	SeriesID int64 `json:"series_id"`
	prefsBody
	Seq int64 `json:"seq"`
}

type changedListBody struct {
	ItemID   int64  `json:"item_id"`
	ListedAt *int64 `json:"listed_at"`
	Seq      int64  `json:"seq"`
}

type changedLibraryBody struct {
	ID    int64           `json:"id"`
	Name  string          `json:"name"`
	Kind  string          `json:"kind"`
	Roots []rootStateBody `json:"roots"`
	Seq   int64           `json:"seq"`
}

// stream writes JSON to a response as it goes, and keeps the first error
// and a count of the rows written.
type stream struct {
	w    *bufio.Writer
	err  error
	rows int
}

func (st *stream) raw(s string) error {
	if st.err == nil {
		_, st.err = st.w.WriteString(s)
	}
	return st.err
}

func (st *stream) value(v any) error {
	if st.err != nil {
		return st.err
	}
	b, err := json.Marshal(v)
	if err != nil {
		st.err = err
		return err
	}
	_, st.err = st.w.Write(b)
	return st.err
}

// array writes "name":[…], calling each to write the elements with put.
func (st *stream) array(name string, each func(put func(any) error) error) error {
	st.raw(`,"` + name + `":[`)
	first := true
	err := each(func(v any) error {
		if !first {
			st.raw(",")
		}
		first = false
		st.rows++
		return st.value(v)
	})
	if err != nil && st.err == nil {
		st.err = err
	}
	return st.raw("]")
}

// handleChanges is GET /changes (docs/04-api.md, section 5): one page of
// what changed for the signed-in user, read in one read transaction and
// written as it is read, so that a page of a large library costs no more
// memory than a row at a time.
func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "since" && k != "limit" {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the parameters are since and limit")
			return
		}
	}
	since, limit := int64(0), 500
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "since is a sequence number, 0 for everything")
			return
		}
		since = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 2000 {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "limit is 1 to 2000")
			return
		}
		limit = n
	}
	ctx := r.Context()
	page, err := s.db.Changes(ctx, callerOf(ctx).user.ID, since, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer page.Close()
	libs, ids, err := page.Libraries(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	st := &stream{w: bufio.NewWriterSize(w, 16<<10)}
	st.raw(`{"since":` + strconv.FormatInt(page.Since, 10) + `,"upto":` + strconv.FormatInt(page.Upto, 10) +
		`,"latest":` + strconv.FormatInt(page.Latest, 10) + `,"reset":` + strconv.FormatBool(page.Reset))
	st.array("items", func(put func(any) error) error {
		return page.Items(ctx, func(c db.Card) error { return put(cardJSON(c, personal{})) })
	})
	st.array("state", func(put func(any) error) error {
		return page.States(ctx, func(c db.ChangedState) error {
			return put(changedStateBody{ItemID: c.ItemID, stateBody: *stateJSON(c.State), Seq: c.Seq})
		})
	})
	st.array("prefs", func(put func(any) error) error {
		return page.Prefs(ctx, func(p db.ChangedPrefs) error {
			return put(changedPrefsBody{SeriesID: p.SeriesID, prefsBody: prefsJSON(p.Prefs), Seq: p.Seq})
		})
	})
	st.array("list", func(put func(any) error) error {
		return page.List(ctx, func(l db.ChangedListing) error {
			return put(changedListBody{ItemID: l.ItemID, ListedAt: int64OrNil(l.ListedAt), Seq: l.Seq})
		})
	})
	st.array("deleted", func(put func(any) error) error {
		return page.Deleted(ctx, func(id int64) error { return put(id) })
	})
	st.array("libraries", func(put func(any) error) error {
		for _, l := range libs {
			b := changedLibraryBody{ID: l.ID, Name: l.Name, Kind: l.Kind, Roots: []rootStateBody{}, Seq: l.Seq}
			for _, root := range l.Roots {
				b.Roots = append(b.Roots, rootStateBody{ID: root.ID, Available: root.Available})
			}
			if err := put(b); err != nil {
				return err
			}
		}
		return nil
	})
	st.raw(`,"library_ids":`)
	st.value(ids)
	st.raw("}\n")
	if st.err == nil {
		st.err = st.w.Flush()
	}
	if st.err != nil {
		// The status is sent: a page cut short must not read as whole.
		s.log.Warn("changes: a page was cut short", "id", requestID(ctx), "err", st.err)
		panic(http.ErrAbortHandler)
	}
	s.synced(st.rows, page.Upto == page.Latest)
}

// freeAfterRows is how many rows of /changes are written before the end
// of a sync hands the memory it freed back to the system.
const freeAfterRows = 5000

// synced counts the rows /changes has written, and at the end of a sync,
// once they pass freeAfterRows, hands the memory freed back to the system,
// as a batch of scans does (docs/02-architecture.md, section 8).
func (s *Server) synced(rows int, ended bool) {
	s.changesMu.Lock()
	s.changesRows += rows
	free := ended && s.changesRows >= freeAfterRows
	if free {
		s.changesRows = 0
	}
	s.changesMu.Unlock()
	if free {
		debug.FreeOSMemory()
	}
}
