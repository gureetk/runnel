// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"sync"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// fakeWork is a queue whose kinds a test pauses, and that counts how
// often it was woken.
type fakeWork struct {
	mu     sync.Mutex
	paused map[string]bool
	woken  int
}

func (f *fakeWork) Paused(kind string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused[kind]
}

func (f *fakeWork) Wake() {
	f.mu.Lock()
	f.woken++
	f.mu.Unlock()
}

func TestTasksAPI(t *testing.T) {
	ts := newTestServer(t)
	work := &fakeWork{paused: map[string]bool{}}
	gate := &countGate{}
	ts.work, ts.gate = work, gate
	ctx := context.Background()
	lib, _ := ts.db.CreateLibrary(ctx, "Films", "movies", "default", []string{t.TempDir()}, 1)
	for i := int64(1); i <= 3; i++ {
		ts.db.AddTask(ctx, "probe", db.FileKey(i), lib.ID, "", 10+i)
	}
	ts.db.AddTask(ctx, "image", "image:1:poster", lib.ID, "", 20)
	ts.db.AddTask(ctx, "fetch", "item:1", lib.ID, "", 30)
	fetch, _, _ := ts.db.ClaimTask(ctx, "fetch", 40)
	ts.db.FinishTask(ctx, fetch, db.Failed, 0, "TMDB answered 401")
	ts.db.ClaimTask(ctx, "probe", 40)

	get := func() map[string]workBody {
		t.Helper()
		var body struct {
			Work []workBody `json:"work"`
		}
		if rec := ts.call(t, "GET", "/api/v1/admin/tasks", ts.adminToken, local, nil, &body); rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		}
		out := map[string]workBody{}
		for _, w := range body.Work {
			if w.LibraryID == nil || *w.LibraryID != lib.ID {
				t.Fatalf("%+v", w)
			}
			out[w.Kind] = w
		}
		return out
	}
	states := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for k, w := range get() {
			by := ""
			if w.PausedBy != nil {
				by = " by " + *w.PausedBy
			}
			out[k] = w.State + by
		}
		return out
	}

	w := get()
	if f := w["fetch"]; f.State != "failed" || f.Failed != 1 || f.Total != 1 || f.Error == nil || *f.Error != "TMDB answered 401" || f.Since != 30 {
		t.Fatalf("fetch: %+v", f)
	}
	if p := w["probe"]; p.State != "running" || p.Done != 0 || p.Total != 3 || p.Failed != 0 || p.Error != nil || p.Since != 11 {
		t.Fatalf("probe: %+v", p)
	}
	if s := states(); s["image"] != "queued" || len(s) != 3 {
		t.Fatalf("%v", s)
	}

	release := gate.Hold()
	if s := states(); s["image"] != "paused by playback" || s["probe"] != "paused by playback" || s["fetch"] != "failed" {
		t.Fatalf("while a playback starts: %v", s)
	}
	release()
	work.mu.Lock()
	work.paused["fetch"], work.paused["image"] = true, true
	work.mu.Unlock()
	if s := states(); s["image"] != "paused by provider" || s["fetch"] != "failed" || s["probe"] != "running" {
		t.Fatalf("while a provider asks to wait: %v", s)
	}

	var n map[string]int
	if rec := ts.call(t, "POST", "/api/v1/admin/tasks/cancel", ts.adminToken, local,
		map[string]any{"kind": "image", "library_id": lib.ID}, &n); rec.Code != 200 || n["count"] != 1 {
		t.Fatalf("cancel: %d %v", rec.Code, n)
	}
	if _, ok := get()["image"]; ok {
		t.Fatal("cancelled work is still listed")
	}
	if rec := ts.call(t, "POST", "/api/v1/admin/tasks/retry", ts.adminToken, local,
		map[string]any{"kind": "fetch", "library_id": lib.ID}, &n); rec.Code != 200 || n["count"] != 1 || work.woken != 1 {
		t.Fatalf("retry: %d %v, woken %d", rec.Code, n, work.woken)
	}
	if f := get()["fetch"]; f.State != "paused" || f.Failed != 0 || f.Error != nil {
		t.Fatalf("retried: %+v", f)
	}
	// Nothing to retry wakes nothing.
	ts.call(t, "POST", "/api/v1/admin/tasks/retry", ts.adminToken, local, map[string]any{"kind": "probe", "library_id": lib.ID}, &n)
	if n["count"] != 0 || work.woken != 1 {
		t.Fatalf("an empty retry: %v, woken %d", n, work.woken)
	}

	var e apiError
	for _, body := range []any{map[string]any{"kind": "music"}, map[string]any{}, "probe"} {
		if rec := ts.call(t, "POST", "/api/v1/admin/tasks/cancel", ts.adminToken, local, body, &e); rec.Code != 400 {
			t.Errorf("%v: %d", body, rec.Code)
		}
	}
}
