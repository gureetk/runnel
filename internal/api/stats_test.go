// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"
)

func TestPlaylogAndStats(t *testing.T) {
	b := newBrowse(t)
	film := b.film(t, "Paper Comets")
	file := b.fileOf(t, "Paper Comets").ID
	for i, s := range []string{"first", "second"} {
		b.call(t, "POST", "/api/v1/progress", b.token, "", map[string]any{"session": s, "item_id": film.ID, "file_id": file,
			"position_ms": 0, "state": "playing", "mode": "direct",
			"timings": map[string]any{"pressed_to_loaded_ms": 100 + i, "pressed_to_playing_ms": 200 + i},
			"stats":   map[string]any{"decoder": "mediacodec", "hardware": true, "dropped_frames": 0, "warmed": i == 1}}, nil)
	}
	type row struct {
		ID   int64 `json:"id"`
		User *struct {
			ID   *int64 `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		Device          *string `json:"device"`
		Client          *string `json:"client"`
		ItemID          *int64  `json:"item_id"`
		Title           *string `json:"title"`
		Mode            string  `json:"mode"`
		Warmed          bool    `json:"warmed"`
		ClientLoadedMS  *int64  `json:"client_loaded_ms"`
		ClientPlayingMS *int64  `json:"client_playing_ms"`
		ServerFirstByte *int64  `json:"server_first_byte_ms"`
		Decoder         *string `json:"decoder"`
		Hardware        *bool   `json:"hardware"`
		Encoder         *string `json:"encoder"`
	}
	var page struct {
		Rows []row  `json:"rows"`
		Next string `json:"next"`
	}
	if rec := b.call(t, "GET", "/api/v1/admin/playlog?limit=1", b.token, "", nil, &page); rec.Code != 200 || len(page.Rows) != 1 || page.Next == "" {
		t.Fatalf("playlog %d %+v", rec.Code, page)
	}
	r := page.Rows[0]
	if r.User == nil || r.User.ID == nil || r.User.Name != "ada" || *r.Device != "Test device" || *r.Client != "runnel-test/0" || *r.ItemID != film.ID ||
		*r.Title != "Paper Comets" || r.Mode != "direct" || !r.Warmed || *r.ClientLoadedMS != 101 || *r.ClientPlayingMS != 201 ||
		r.ServerFirstByte != nil || *r.Decoder != "mediacodec" || !*r.Hardware || r.Encoder != nil {
		t.Fatalf("the newest row %+v", r)
	}
	newest := r.ID
	page.Next, page.Rows = "", nil
	b.call(t, "GET", "/api/v1/admin/playlog?limit=1&cursor="+mustCursor(t, b, newest), b.token, "", nil, &page)
	if len(page.Rows) != 1 || page.Rows[0].ID >= newest || page.Rows[0].Warmed || page.Next != "" {
		t.Fatalf("the older row %+v", page)
	}

	var stats struct {
		Version   string           `json:"version"`
		StartedAt int64            `json:"started_at"`
		Memory    map[string]int64 `json:"memory"`
		Database  map[string]int64 `json:"database"`
		Counts    map[string]int   `json:"counts"`
		Limits    map[string]any   `json:"limits"`
	}
	if rec := b.call(t, "GET", "/api/v1/admin/stats", b.token, "", nil, &stats); rec.Code != 200 {
		t.Fatalf("stats %d", rec.Code)
	}
	if stats.Version != "0.0.0-test" || stats.StartedAt != 1_800_000_000_000 || stats.Memory["rss"] <= 0 || stats.Database["size"] <= 0 ||
		stats.Database["seq"] <= 0 || stats.Counts["libraries"] != 2 || stats.Counts["movies"] != 4 || stats.Counts["series"] != 1 ||
		stats.Counts["episodes"] != 4 || stats.Counts["files"] != 8 || stats.Counts["users"] != 1 || stats.Limits != nil {
		t.Fatalf("stats %+v", stats)
	}

	bo := b.signIn(t, "bo", false, "laptop")
	for _, target := range []string{"/api/v1/admin/playlog", "/api/v1/admin/stats"} {
		if rec := b.call(t, "GET", target, bo, "", nil, nil); rec.Code != 403 {
			t.Errorf("%s for a non-admin: %d", target, rec.Code)
		}
	}
	for _, target := range []string{"/api/v1/admin/playlog?limit=0", "/api/v1/admin/playlog?cursor=junk", "/api/v1/admin/playlog?since=1"} {
		if rec := b.call(t, "GET", target, b.token, "", nil, nil); rec.Code != 400 {
			t.Errorf("%s: %d", target, rec.Code)
		}
	}

	// A row keeps the person's name once the person is removed, and loses
	// their id, which is never given again.
	idOf := func(token string) string {
		var me struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		b.call(t, "GET", "/api/v1/auth/me", token, "", nil, &me)
		return idStr(me.User.ID)
	}
	b.call(t, "PATCH", "/api/v1/admin/users/"+idOf(bo), b.token, "", map[string]any{"is_admin": true}, nil)
	if rec := b.call(t, "DELETE", "/api/v1/admin/users/"+idOf(b.token), bo, "", nil, nil); rec.Code != 204 {
		t.Fatalf("remove ada: %d", rec.Code)
	}
	page.Rows = nil
	b.call(t, "GET", "/api/v1/admin/playlog", bo, "", nil, &page)
	if len(page.Rows) != 2 || page.Rows[0].User == nil || page.Rows[0].User.ID != nil || page.Rows[0].User.Name != "ada" {
		t.Fatalf("after ada went: %+v", page.Rows)
	}
}

// mustCursor is the cursor the playlog gives after a row.
func mustCursor(t *testing.T, b *browse, id int64) string {
	t.Helper()
	return encodeCursor(playlogCursor{I: id})
}
