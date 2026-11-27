// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type sessionRow struct {
	Session string `json:"session"`
	User    *struct {
		Name string `json:"name"`
	} `json:"user"`
	Device *struct {
		Name   string `json:"name"`
		Client string `json:"client"`
	} `json:"device"`
	Item *struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"item"`
	FileID      int64   `json:"file_id"`
	State       *string `json:"state"`
	PositionMS  *int64  `json:"position_ms"`
	Mode        string  `json:"mode"`
	StartedInMS *int64  `json:"started_in_ms"`
	Transcode   *struct {
		State       string `json:"state"`
		V           string `json:"v"`
		Depth       int    `json:"depth"`
		Encoder     string `json:"encoder"`
		Memory      int64  `json:"memory"`
		Warmed      bool   `json:"warmed"`
		FirstByteMS *int64 `json:"first_byte_ms"`
	} `json:"transcode"`
}

// TestSessionsForAdmins: a direct play, a transcode with its reports, and
// a warm-up, each a row; stopping a transcode or a warm-up ends its
// ffmpeg, and a direct play cannot be stopped (docs/04-api.md, section
// 8.7).
func TestSessionsForAdmins(t *testing.T) {
	b, tr := newTranscodes(t, 2)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	long := b.fileOf(t, "Long Water")
	film := b.film(t, "Long Water").ID
	short := b.fileOf(t, "Paper Comets")
	noor := b.signIn(t, "noor", false, "tv")
	sessions := func() map[string]sessionRow {
		t.Helper()
		var page struct{ Sessions []sessionRow }
		if rec := b.call(t, "GET", "/api/v1/admin/sessions", b.adminToken, local, nil, &page); rec.Code != 200 || page.Sessions == nil {
			t.Fatalf("sessions: %d", rec.Code)
		}
		out := map[string]sessionRow{}
		for _, r := range page.Sessions {
			out[r.Session] = r
		}
		return out
	}
	if len(sessions()) != 0 {
		t.Fatal("sessions before any playback")
	}

	// Noor transcodes, and reports it.
	resp, err := http.Get(srv.URL + "/api/v1/files/" + idStr(long.ID) + "/transcode.mkv?v=h264&start_ms=5000&session=tv1&t=" + noor)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	b.call(t, "POST", "/api/v1/progress", noor, "", map[string]any{"session": "tv1", "item_id": film, "file_id": long.ID,
		"position_ms": 6000, "state": "playing", "mode": "transcode", "timings": map[string]any{"pressed_to_playing_ms": 410}}, nil)
	// Ada plays a film as it is, and has a warm-up waiting.
	b.call(t, "POST", "/api/v1/progress", b.token, "", map[string]any{"session": "laptop1", "item_id": b.film(t, "Paper Comets").ID,
		"file_id": short.ID, "position_ms": 0, "state": "paused", "mode": "direct"}, nil)
	var a prepareAnswer
	b.call(t, "POST", "/api/v1/files/"+idStr(long.ID)+"/prepare", b.token, "", map[string]any{"mode": "transcode", "v": "h264"}, &a)
	time.Sleep(2100 * time.Millisecond) // the children's memory has been read

	got := sessions()
	if len(got) != 3 {
		t.Fatalf("%d sessions: %+v", len(got), got)
	}
	tv := got["tv1"]
	if tv.User == nil || tv.User.Name != "noor" || tv.Device == nil || tv.Device.Name != "Test device" || tv.Item == nil ||
		tv.Item.Title != "Long Water" || tv.FileID != long.ID || tv.State == nil || *tv.State != "playing" ||
		tv.PositionMS == nil || *tv.PositionMS != 6000 || tv.Mode != "transcode" || tv.StartedInMS == nil || *tv.StartedInMS != 410 {
		t.Errorf("the transcode's row %+v", tv)
	}
	if x := tv.Transcode; x == nil || x.State != "streaming" || x.V != "h264" || x.Depth != 8 || x.Encoder != "libx264" ||
		x.Memory <= 0 || x.Warmed || x.FirstByteMS == nil {
		t.Errorf("the transcode %+v", tv.Transcode)
	}
	lp := got["laptop1"]
	if lp.Transcode != nil || lp.Mode != "direct" || lp.Item == nil || lp.Item.Title != "Paper Comets" || *lp.State != "paused" ||
		lp.StartedInMS != nil {
		t.Errorf("the direct play's row %+v", lp)
	}
	wu := got[a.Ticket]
	if wu.Transcode == nil || (wu.Transcode.State != "held" && wu.Transcode.State != "warming") || wu.State != nil ||
		wu.Item == nil || wu.Item.ID != film || wu.User == nil || wu.User.Name != "ada" || wu.Transcode.Memory <= 0 {
		t.Errorf("the warm-up's row %+v %+v", wu, wu.Transcode)
	}
	var stats struct {
		InUse map[string]int `json:"in_use"`
	}
	b.call(t, "GET", "/api/v1/admin/stats", b.adminToken, local, nil, &stats)
	if stats.InUse["slots"] != 2 || stats.InUse["warmups"] != 1 {
		t.Errorf("in use %v", stats.InUse)
	}

	// Stopping the transcode ends its stream, and its ffmpeg.
	if rec := b.call(t, "DELETE", "/api/v1/admin/sessions/tv1", b.adminToken, local, nil, nil); rec.Code != 204 {
		t.Fatalf("stop: %d", rec.Code)
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end")
	}
	if rec := b.call(t, "DELETE", "/api/v1/admin/sessions/"+a.Ticket, b.adminToken, local, nil, nil); rec.Code != 204 {
		t.Fatalf("stop the warm-up: %d", rec.Code)
	}
	waitUntil(t, "no ffmpeg is left", func() bool { return tr.Running() == 0 })
	if got := sessions(); got["tv1"].Transcode != nil || len(got) != 2 {
		t.Errorf("after the stops: %+v", got)
	}
	var e apiError
	if rec := b.call(t, "DELETE", "/api/v1/admin/sessions/laptop1", b.adminToken, local, nil, &e); rec.Code != 409 || e.Error.Code != "conflict" {
		t.Errorf("stop a direct play: %d %+v", rec.Code, e)
	}
	if rec := b.call(t, "DELETE", "/api/v1/admin/sessions/nothing", b.adminToken, local, nil, &e); rec.Code != 404 {
		t.Errorf("stop nothing: %d", rec.Code)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/admin/sessions"}, {"DELETE", "/api/v1/admin/sessions/tv1"}} {
		if rec := b.call(t, c.method, c.path, noor, "", nil, nil); rec.Code != 403 {
			t.Errorf("%s %s by someone who is no admin: %d", c.method, c.path, rec.Code)
		}
	}
	// A stopped report leaves the list.
	b.call(t, "POST", "/api/v1/progress", b.token, "", map[string]any{"session": "laptop1", "item_id": b.film(t, "Paper Comets").ID,
		"file_id": short.ID, "position_ms": 0, "state": "stopped", "mode": "direct"}, nil)
	if _, ok := sessions()["laptop1"]; ok {
		t.Error("a stopped session is listed")
	}
}
