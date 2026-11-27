// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/play"
)

// fakeEncoders answers with a fixed result, and counts the checks.
type fakeEncoders struct {
	res    play.Result
	busy   bool
	checks int
}

func (f *fakeEncoders) Last() play.Result { return f.res }

func (f *fakeEncoders) Check(context.Context) (play.Result, error) {
	if f.busy {
		return play.Result{}, play.ErrBusy
	}
	f.checks++
	return f.res, nil
}

func TestEncodersAPI(t *testing.T) {
	ts := newTestServer(t)
	ts.addUser(t, "ada", "correct horse", true)
	noor := ts.signIn(t, "noor", false, "tv")
	type body struct {
		CheckedAt *int64 `json:"checked_at"`
		HW        string `json:"hw"`
		Encoders  []struct {
			V       string  `json:"v"`
			Depth   int     `json:"depth"`
			How     string  `json:"how"`
			Encoder string  `json:"encoder"`
			Works   bool    `json:"works"`
			Error   *string `json:"error"`
		} `json:"encoders"`
	}
	var b body
	if rec := ts.call(t, "GET", "/api/v1/admin/encoders", ts.adminToken, local, nil, &b); rec.Code != 200 ||
		b.CheckedAt != nil || b.HW != "none" || b.Encoders == nil || len(b.Encoders) != 0 {
		t.Fatalf("with nothing checked: %d %+v", rec.Code, b)
	}

	at := time.UnixMilli(1795000001000)
	f := &fakeEncoders{res: play.Result{CheckedAt: at, Rows: []play.Row{
		{V: "hevc", Depth: 10, How: "hardware", Encoder: "hevc_vaapi", Error: "no render device at /dev/dri/renderD128"},
		{V: "h264", Depth: 8, How: "software", Encoder: "libx264", Works: true},
	}}}
	ts.encoders = f
	for _, call := range []struct{ method, path string }{{"GET", "/api/v1/admin/encoders"}, {"POST", "/api/v1/admin/encoders/check"}} {
		b = body{}
		if rec := ts.call(t, call.method, call.path, ts.adminToken, local, nil, &b); rec.Code != 200 {
			t.Fatalf("%s %s: %d", call.method, call.path, rec.Code)
		}
		if b.CheckedAt == nil || *b.CheckedAt != 1795000001000 || b.HW != "none" || len(b.Encoders) != 2 {
			t.Fatalf("%s: %+v", call.path, b)
		}
		e := b.Encoders
		if e[0].V != "hevc" || e[0].Depth != 10 || e[0].How != "hardware" || e[0].Encoder != "hevc_vaapi" || e[0].Works ||
			e[0].Error == nil || *e[0].Error != "no render device at /dev/dri/renderD128" {
			t.Errorf("%s: the hardware row %+v", call.path, e[0])
		}
		if !e[1].Works || e[1].Error != nil {
			t.Errorf("%s: the software row %+v", call.path, e[1])
		}
	}
	if f.checks != 1 {
		t.Errorf("%d checks", f.checks)
	}
	for _, want := range []string{`msg="encoders checked"`, "hw=none", `hevc10="no render device at /dev/dri/renderD128"`, "h264_software=works"} {
		if !strings.Contains(ts.log.String(), want) {
			t.Errorf("the log has no %s: %s", want, ts.log.String())
		}
	}

	f.busy = true
	var e apiError
	if rec := ts.call(t, "POST", "/api/v1/admin/encoders/check", ts.adminToken, local, nil, &e); rec.Code != 503 || e.Error.Code != "busy" {
		t.Errorf("during a transcode: %d %+v", rec.Code, e)
	}
	for _, call := range []struct{ method, path string }{{"GET", "/api/v1/admin/encoders"}, {"POST", "/api/v1/admin/encoders/check"}} {
		if rec := ts.call(t, call.method, call.path, noor, "", nil, nil); rec.Code != 403 {
			t.Errorf("%s by someone who is no admin: %d", call.path, rec.Code)
		}
	}
}
