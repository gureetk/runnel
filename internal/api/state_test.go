// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// episodes lists the series' episodes in order: 1x1, 1x2, 2x1, 0x1.
func (b *browse) episodes(t *testing.T) []cardBody {
	t.Helper()
	var page itemsPage
	b.call(t, "GET", "/api/v1/items?series="+idStr(b.series(t).ID), b.token, "", nil, &page)
	if len(page.Items) != 4 {
		t.Fatalf("episodes %+v", page)
	}
	return page.Items
}

func (b *browse) detail(t *testing.T, token string, id int64) detailBody {
	t.Helper()
	var d detailBody
	if rec := b.call(t, "GET", "/api/v1/items/"+idStr(id), token, "", nil, &d); rec.Code != 200 {
		t.Fatalf("detail of %d: %d", id, rec.Code)
	}
	return d
}

func (b *browse) state(t *testing.T, token string, id int64) stateBody {
	t.Helper()
	d := b.detail(t, token, id)
	if d.State == nil {
		t.Fatalf("no state on %+v", d)
	}
	return *d.State
}

func ptr[T any](v T) *T { return &v }

// sameJSON compares two values as their JSON, so that pointers compare by
// what they point at.
func sameJSON(a, b any) bool { return string(mustJSON(a)) == string(mustJSON(b)) }

func TestProgress(t *testing.T) {
	b := newBrowse(t)
	film := b.film(t, "Paper Comets")
	file := b.fileOf(t, "Paper Comets").ID
	if film.State == nil || *film.State != (stateBody{}) {
		t.Fatalf("a film never played has a state of zeros: %+v", film.State)
	}
	report := func(session, state string, pos int64, extra map[string]any) int {
		t.Helper()
		body := map[string]any{"session": session, "item_id": film.ID, "file_id": file, "position_ms": pos,
			"state": state, "mode": "direct", "audio_idx": 1, "sub_idx": nil}
		for k, v := range extra {
			body[k] = v
		}
		return b.call(t, "POST", "/api/v1/progress", b.token, "", body, nil).Code
	}
	timed := map[string]any{"timings": map[string]any{"pressed_to_loaded_ms": 310, "pressed_to_playing_ms": 640},
		"stats": map[string]any{"decoder": "mediacodec", "hardware": true, "dropped_frames": 0, "warmed": true}}

	// Below 30 s nothing is kept. A playing session writes once every 10 s:
	// the second report is too soon, but its timings are logged.
	if code := report("s1", "playing", 10_000, nil); code != 204 {
		t.Fatalf("progress: %d", code)
	}
	if code := report("s1", "playing", 40_000, timed); code != 204 || b.state(t, b.token, film.ID) != (stateBody{}) {
		t.Fatalf("a report 0 s after the last one was written: %d %+v", code, b.state(t, b.token, film.ID))
	}
	b.advance(10 * time.Second)
	report("s1", "playing", 50_000, timed)
	at := b.now().UnixMilli()
	if s := b.state(t, b.token, film.ID); !sameJSON(s, stateBody{PositionMS: 50_000, LastPlayedAt: &at}) {
		t.Fatalf("after 10 s: %+v", s)
	}
	// Pausing always writes.
	report("s1", "paused", 51_000, nil)
	if s := b.state(t, b.token, film.ID); s.PositionMS != 51_000 {
		t.Fatalf("paused: %+v", s)
	}
	// The file runs a second, so a stop here is past 90% of it.
	report("s1", "stopped", 52_000, nil)
	if s := b.state(t, b.token, film.ID); !sameJSON(s, stateBody{Played: true, PlayCount: 1, LastPlayedAt: &at}) {
		t.Fatalf("stopped at the end: %+v", s)
	}

	// The session's first timed report made one row of the start-time log.
	rows, err := b.db.PlayLog(context.Background(), 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("start-time log %+v %v", rows, err)
	}
	if r := rows[0]; r.Title != "Paper Comets" || r.Device != "Test device" || r.Client != "runnel-test/0" || r.User != "ada" ||
		r.Mode != "direct" || !r.Warmed || *r.LoadedMS != 310 || *r.PlayingMS != 640 || r.Decoder != "mediacodec" ||
		r.ItemID != film.ID || r.FileID != file {
		t.Fatalf("start-time row %+v", r)
	}

	// The film's card shows the user's state, and another user's is apart.
	if c := b.film(t, "Paper Comets"); c.State == nil || !c.State.Played {
		t.Fatalf("card %+v", c.State)
	}
	b.addUser(t, "bo", "battery staple", false)
	var other loginAnswer
	b.call(t, "POST", "/api/v1/auth/login", "", "", login("bo", "battery staple", "d2"), &other)
	if s := b.state(t, other.Token, film.ID); s != (stateBody{}) {
		t.Fatalf("another user's state: %+v", s)
	}

	series := b.series(t)
	ep := b.episodes(t)[0]
	for _, c := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"session": ""}, 400},
		{map[string]any{"session": strings.Repeat("s", 65)}, 400},
		{map[string]any{"item_id": nil}, 400},
		{map[string]any{"position_ms": -1}, 400},
		{map[string]any{"state": "seeking"}, 400},
		{map[string]any{"mode": "hls"}, 400},
		{map[string]any{"timings": map[string]any{"pressed_to_loaded_ms": -5}}, 400},
		{map[string]any{"item_id": series.ID}, 400},
		{map[string]any{"item_id": ep.ID}, 400}, // the film's file does not play the episode
		{map[string]any{"item_id": 99999}, 404},
	} {
		body := map[string]any{"session": "bad", "item_id": film.ID, "file_id": file, "position_ms": 40_000, "state": "paused", "mode": "direct"}
		for k, v := range c.body {
			body[k] = v
		}
		var e apiError
		if rec := b.call(t, "POST", "/api/v1/progress", b.token, "", body, &e); rec.Code != c.want {
			t.Errorf("%v: %d %+v, want %d", c.body, rec.Code, e, c.want)
		}
	}
	if code := b.call(t, "POST", "/api/v1/progress", "", "", map[string]any{}, nil).Code; code != 401 {
		t.Fatalf("without a token: %d", code)
	}
}

func TestPlayedAndUndo(t *testing.T) {
	b := newBrowse(t)
	eps := b.episodes(t)
	series := b.detail(t, b.token, b.series(t).ID)
	season1 := (*series.Seasons)[0]
	if series.Counts == nil || *series.Counts.Unplayed != 3 || *season1.Counts.Unplayed != 2 {
		t.Fatalf("counts before %+v %+v", series.Counts, season1.Counts)
	}

	// A season, with what it replaced for Undo.
	var prev struct {
		Previous []previousEntry `json:"previous"`
	}
	if rec := b.call(t, "PUT", "/api/v1/items/"+idStr(season1.ID)+"/played?return=previous", b.token, "", nil, &prev); rec.Code != 200 {
		t.Fatalf("mark a season: %d", rec.Code)
	}
	if len(prev.Previous) != 2 || prev.Previous[0].ItemID != eps[0].ID || prev.Previous[1].ItemID != eps[1].ID ||
		prev.Previous[0].stateBody != (stateBody{}) {
		t.Fatalf("previous %+v", prev)
	}
	now := b.now().UnixMilli()
	if s := b.state(t, b.token, eps[1].ID); !sameJSON(s, stateBody{Played: true, PlayCount: 1, LastPlayedAt: &now}) {
		t.Fatalf("marked %+v", s)
	}
	var seasons itemsPage
	b.call(t, "GET", "/api/v1/items?parent="+idStr(series.ID), b.token, "", nil, &seasons)
	var unplayed []int
	for _, s := range seasons.Items {
		unplayed = append(unplayed, *s.Counts.Unplayed)
	}
	if string(mustJSON(unplayed)) != "[0,1,1]" || *b.series(t).Counts.Unplayed != 1 {
		t.Fatalf("unplayed by season %v, series %+v", unplayed, b.series(t).Counts)
	}

	libraries := func() (films, shows int) {
		t.Helper()
		var libs struct{ Libraries []libraryBody }
		b.call(t, "GET", "/api/v1/libraries", b.token, "", nil, &libs)
		return libs.Libraries[0].Unplayed, libs.Libraries[1].Unplayed
	}
	if f, s := libraries(); f != 4 || s != 1 {
		t.Fatalf("unplayed films %d, series %d", f, s)
	}
	// Many at once; an id that no item has is skipped. With every episode
	// outside the specials played, the series no longer counts.
	if code := b.call(t, "PUT", "/api/v1/items/played", b.token, "", map[string]any{"ids": []int64{eps[2].ID, 99999}}, nil).Code; code != 204 {
		t.Fatalf("mark many: %d", code)
	}
	if f, s := libraries(); f != 4 || s != 0 {
		t.Fatalf("unplayed films %d, series %d", f, s)
	}

	// Undo.
	if code := b.call(t, "PUT", "/api/v1/items/state", b.token, "", map[string]any{"states": prev.Previous}, nil).Code; code != 204 {
		t.Fatalf("undo: %d", code)
	}
	if s := b.state(t, b.token, eps[0].ID); s != (stateBody{}) {
		t.Fatalf("after Undo %+v", s)
	}
	if code := b.call(t, "DELETE", "/api/v1/items/played", b.token, "", map[string]any{"ids": []int64{eps[2].ID}}, nil).Code; code != 204 {
		t.Fatalf("unmark many: %d", code)
	}
	if s := b.state(t, b.token, eps[2].ID); s.Played || s.PlayCount != 1 {
		t.Fatalf("unmarked %+v", s)
	}
	var none struct {
		Previous []previousEntry `json:"previous"`
	}
	if rec := b.call(t, "DELETE", "/api/v1/items/"+idStr(eps[0].ID)+"/played?return=previous", b.token, "", nil, &none); rec.Code != 200 ||
		none.Previous == nil || len(none.Previous) != 0 {
		t.Fatalf("unmarking what is not played: %d %+v", rec.Code, none)
	}

	many := make([]int64, 1001)
	states := make([]map[string]any, 2001)
	for i := range states {
		states[i] = map[string]any{"item_id": eps[0].ID}
	}
	for _, c := range []struct {
		method, target string
		body           any
		want           int
	}{
		{"PUT", "/api/v1/items/99999/played", nil, 404},
		{"DELETE", "/api/v1/items/abc/played", nil, 404},
		{"PUT", "/api/v1/items/" + idStr(eps[0].ID) + "/played?return=all", nil, 400},
		{"PUT", "/api/v1/items/played", map[string]any{"ids": many}, 400},
		{"PUT", "/api/v1/items/played", map[string]any{"ids": "all"}, 400},
		{"PUT", "/api/v1/items/state", map[string]any{"states": states}, 400},
		{"PUT", "/api/v1/items/state", map[string]any{"states": []map[string]any{{"item_id": series.ID}}}, 400},
		{"PUT", "/api/v1/items/state", map[string]any{"states": []map[string]any{{"item_id": eps[0].ID, "played": true, "position_ms": 5}}}, 400},
		{"PUT", "/api/v1/items/state", map[string]any{"states": []map[string]any{{"played": true}}}, 400},
		{"PUT", "/api/v1/items/state", map[string]any{"states": []map[string]any{{"item_id": eps[0].ID, "play_count": -1}}}, 400},
		{"PUT", "/api/v1/items/state", map[string]any{"states": []map[string]any{{"item_id": 99999, "played": true}}}, 204},
	} {
		var e apiError
		out := any(&e)
		if c.want == 204 {
			out = nil
		}
		if rec := b.call(t, c.method, c.target, b.token, "", c.body, out); rec.Code != c.want {
			t.Errorf("%s %s: %d %+v, want %d", c.method, c.target, rec.Code, e, c.want)
		}
	}
}

func TestTrackPrefs(t *testing.T) {
	b := newBrowse(t)
	series := b.series(t)
	ep := b.episodes(t)[1]
	film := b.film(t, "Paper Comets")
	prefsOf := func(id int64) prefsBody {
		t.Helper()
		d := b.detail(t, b.token, id)
		if d.Prefs == nil {
			t.Fatalf("no prefs on %d", id)
		}
		return *d.Prefs
	}
	same := func(p prefsBody, audio, sub *string, mode string) bool {
		eq := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
		return eq(p.AudioLang, audio) && eq(p.SubLang, sub) && p.SubMode != nil && *p.SubMode == mode
	}
	if p := prefsOf(ep.ID); !same(p, nil, nil, "auto") {
		t.Fatalf("a new user starts on the defaults: %+v", p)
	}
	if d := b.detail(t, b.token, (*b.detail(t, b.token, series.ID).Seasons)[0].ID); d.Prefs != nil {
		t.Fatalf("a season has no prefs: %+v", d.Prefs)
	}

	if code := b.call(t, "PUT", "/api/v1/auth/me/prefs", b.token, "", map[string]any{"audio_lang": "jpn", "sub_lang": "eng", "sub_mode": "on"}, nil).Code; code != 204 {
		t.Fatalf("defaults: %d", code)
	}
	if p := prefsOf(film.ID); !same(p, ptr("jpn"), ptr("eng"), "on") {
		t.Fatalf("a film follows the defaults: %+v", p)
	}
	if code := b.call(t, "PUT", "/api/v1/series/"+idStr(series.ID)+"/prefs", b.token, "", map[string]any{"audio_lang": "eng", "sub_lang": nil, "sub_mode": "off"}, nil).Code; code != 204 {
		t.Fatalf("a series' choice: %d", code)
	}
	if p := prefsOf(ep.ID); !same(p, ptr("eng"), nil, "off") {
		t.Fatalf("an episode follows its series' choice: %+v", p)
	}
	if p := prefsOf(series.ID); !same(p, ptr("eng"), nil, "off") {
		t.Fatalf("the series: %+v", p)
	}
	if p := prefsOf(film.ID); !same(p, ptr("jpn"), ptr("eng"), "on") {
		t.Fatalf("a film: %+v", p)
	}
	var all struct {
		Defaults prefsBody         `json:"defaults"`
		Series   []seriesPrefsBody `json:"series"`
	}
	b.call(t, "GET", "/api/v1/auth/me/prefs", b.token, "", nil, &all)
	if !same(all.Defaults, ptr("jpn"), ptr("eng"), "on") || len(all.Series) != 1 || all.Series[0].SeriesID != series.ID ||
		all.Series[0].Title != "Harbor Lantern" || !same(all.Series[0].prefsBody, ptr("eng"), nil, "off") {
		t.Fatalf("all prefs %+v", all)
	}

	// A reset: the series follows the defaults again.
	if code := b.call(t, "DELETE", "/api/v1/series/"+idStr(series.ID)+"/prefs", b.token, "", nil, nil).Code; code != 204 {
		t.Fatalf("reset: %d", code)
	}
	if p := prefsOf(ep.ID); !same(p, ptr("jpn"), ptr("eng"), "on") {
		t.Fatalf("after a reset: %+v", p)
	}
	b.call(t, "GET", "/api/v1/auth/me/prefs", b.token, "", nil, &all)
	if len(all.Series) != 0 {
		t.Fatalf("after a reset %+v", all.Series)
	}

	good := map[string]any{"audio_lang": "eng", "sub_mode": "auto"}
	for _, c := range []struct {
		method, target string
		body           any
		want           int
	}{
		{"PUT", "/api/v1/series/" + idStr(film.ID) + "/prefs", good, 404},
		{"PUT", "/api/v1/series/99999/prefs", good, 404},
		{"DELETE", "/api/v1/series/abc/prefs", nil, 404},
		{"PUT", "/api/v1/series/" + idStr(series.ID) + "/prefs", map[string]any{"audio_lang": "eng"}, 400},
		{"PUT", "/api/v1/series/" + idStr(series.ID) + "/prefs", map[string]any{"sub_mode": "sometimes"}, 400},
		{"PUT", "/api/v1/series/" + idStr(series.ID) + "/prefs", map[string]any{"audio_lang": "e n", "sub_mode": "on"}, 400},
		{"PUT", "/api/v1/auth/me/prefs", map[string]any{"sub_lang": strings.Repeat("x", 17), "sub_mode": "on"}, 400},
		{"PUT", "/api/v1/auth/me/prefs", map[string]any{"sub_mode": nil}, 400},
	} {
		var e apiError
		if rec := b.call(t, c.method, c.target, b.token, "", c.body, &e); rec.Code != c.want {
			t.Errorf("%s %s %v: %d %+v, want %d", c.method, c.target, c.body, rec.Code, e, c.want)
		}
	}
}

func TestSeriesResume(t *testing.T) {
	b := newBrowse(t)
	series := b.series(t)
	eps := b.episodes(t)
	resume := func() resumeBody {
		t.Helper()
		d := b.detail(t, b.token, series.ID)
		if d.Resume == nil {
			t.Fatalf("no resume on %+v", d)
		}
		return *d.Resume
	}
	if r := resume(); r != (resumeBody{ID: eps[0].ID, Season: 1, Episode: episodeNumber{N: 1}}) {
		t.Fatalf("nothing played: %+v", r)
	}
	var d detailBody
	b.call(t, "GET", "/api/v1/items/"+idStr(eps[1].ID), b.token, "", nil, &d)
	b.call(t, "POST", "/api/v1/progress", b.token, "", map[string]any{"session": "r1", "item_id": eps[1].ID,
		"file_id": (*d.Files)[0].ID, "position_ms": 40_000, "state": "paused", "mode": "direct"}, nil)
	if r := resume(); r != (resumeBody{ID: eps[1].ID, Season: 1, Episode: episodeNumber{N: 2}, PositionMS: 40_000}) {
		t.Fatalf("in progress: %+v", r)
	}
}
