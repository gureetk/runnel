// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/play"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/scan"
	"github.com/gureetk/runnel/internal/tasks"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

func (f *fakeEncoders) Hardware() play.Hardware { return play.Hardware{} }

var softwareH264 = play.Result{CheckedAt: time.UnixMilli(1795000001000), Rows: []play.Row{
	{V: "hevc", Depth: 10, How: "hardware", Encoder: "hevc_vaapi", Error: "no render device at /dev/dri/renderD128"},
	{V: "hevc", Depth: 8, How: "hardware", Encoder: "hevc_vaapi", Error: "no render device at /dev/dri/renderD128"},
	{V: "h264", Depth: 8, How: "hardware", Encoder: "h264_vaapi", Error: "no render device at /dev/dri/renderD128"},
	{V: "h264", Depth: 8, How: "software", Encoder: "libx264", Works: true},
}}

// longFilm is the path of the transcode fixture's minute-long film.
const longFilm = "Long Water (2020)/Long Water (2020).mkv"

// newTranscodes is the browse fixture with a film of a minute, with two
// audio tracks, and a transcoder on this ffmpeg that encodes H.264 in
// software, as this machine's check found.
func newTranscodes(t *testing.T, slots int) (*browse, *play.Transcoder) {
	t.Helper()
	b := newBrowse(t)
	ffmpeg, ffprobe := tm.Tools(t)
	if !tm.HasEncoder(t, "libx264") {
		t.Skip("this ffmpeg has no libx264")
	}
	p := filepath.Join(b.films, longFilm)
	os.MkdirAll(filepath.Dir(p), 0o755)
	tm.Make(t, p, tm.Join([]string{"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=60"},
		tm.Tone("440", "60"), tm.Tone("660", "60"),
		[]string{"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-c:a", "aac"})...)
	tm.Settle(t, b.films)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := scan.New(b.db, logger)
	q := tasks.New(b.db, tasks.NewGate(), logger)
	q.Register(scan.NewProbes(b.db, probe.New(ffprobe, 2), logger).Kind())
	s.Built = func(ctx context.Context, id int64) error { _, err := library.Build(ctx, b.db, id, 1); return err }
	lib, _ := b.db.Library(ctx, 1)
	if _, err := s.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}
	if err := q.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	f := &fakeEncoders{res: softwareH264}
	b.encoders = f
	tr := play.NewTranscoder(play.TranscoderConfig{FFmpeg: ffmpeg, Slots: slots, Log: logger}, f)
	t.Cleanup(tr.Close)
	b.transcodes = tr
	return b, tr
}

// ffmpegsOf counts the processes whose command line holds path.
func ffmpegsOf(path string) int {
	n := 0
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		b, err := os.ReadFile("/proc/" + d.Name() + "/cmdline")
		if err == nil && bytes.Contains(b, []byte(path)) && bytes.Contains(b, []byte("pipe:1")) {
			n++
		}
	}
	return n
}

func TestTranscodeParameters(t *testing.T) {
	b, _ := newTranscodes(t, 1)
	long := b.fileOf(t, "Long Water")
	short := b.fileOf(t, "Paper Comets")
	var e apiError
	for _, c := range []struct {
		file   int64
		query  string
		status int
		code   string
	}{
		{long.ID, "", 400, "bad_request"},
		{long.ID, "v=av1", 400, "bad_request"},
		{long.ID, "v=h264&depth=12", 400, "bad_request"},
		{long.ID, "v=h264&max_h=0", 400, "bad_request"},
		{long.ID, "v=h264&max_h=tall", 400, "bad_request"},
		{long.ID, "v=h264&start_ms=-1", 400, "bad_request"},
		{long.ID, "v=h264&seek=nearest", 400, "bad_request"},
		{long.ID, "v=h264&audio=0", 400, "bad_request"}, // the video stream
		{long.ID, "v=h264&audio=7", 400, "bad_request"},
		{long.ID, "v=h264&subs=some", 400, "bad_request"},
		{long.ID, "v=h264&fonts=yes", 400, "bad_request"},
		{long.ID, "v=h264&session=" + strings.Repeat("s", 65), 400, "bad_request"},
		{long.ID, "v=hevc", 409, "unsupported"},
		{long.ID, "v=h264&depth=10", 409, "unsupported"},
		{99999, "v=h264", 404, "not_found"},
		{short.ID, "v=h264&audio=2", 400, "bad_request"}, // it has one audio stream
	} {
		e = apiError{}
		rec := b.call(t, "GET", "/api/v1/files/"+idStr(c.file)+"/transcode.mkv?"+c.query, b.token, "", nil, &e)
		if rec.Code != c.status || e.Error.Code != c.code {
			t.Errorf("%s: %d %+v, want %d %s", c.query, rec.Code, e, c.status, c.code)
		}
	}
	// 409 says what the server can encode.
	var u struct {
		Supported struct {
			Video   []string `json:"video"`
			Depth10 []string `json:"depth10"`
		} `json:"supported"`
	}
	b.call(t, "GET", "/api/v1/files/"+idStr(long.ID)+"/transcode.mkv?v=hevc", b.token, "", nil, &u)
	if strings.Join(u.Supported.Video, ",") != "h264" || u.Supported.Depth10 == nil || len(u.Supported.Depth10) != 0 {
		t.Errorf("supported %+v", u.Supported)
	}
	if rec := b.call(t, "GET", "/api/v1/files/1/transcode.mkv", "", "", nil, nil); rec.Code != 401 {
		t.Errorf("without a token: %d", rec.Code)
	}
}

// TestATranscodeStreams over a real connection: Matroska as ffmpeg writes
// it, with its headers, and the start-time log gets the server's side.
func TestATranscodeStreams(t *testing.T) {
	b, tr := newTranscodes(t, 1)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	long := b.fileOf(t, "Long Water")
	g := &countGate{}
	b.gate = g
	b.firstByteHold = 200 * time.Millisecond

	resp, err := http.Get(srv.URL + "/api/v1/files/" + idStr(long.ID) + "/transcode.mkv?v=h264&start_ms=50000&audio=2&session=one&t=" + b.token)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	h := resp.Header
	if resp.StatusCode != 200 || h.Get("Content-Type") != "video/x-matroska" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Runnel-Encoder") != "libx264" || h.Get("X-Runnel-Warmed") != "0" || resp.ContentLength != -1 || h.Get("Accept-Ranges") != "" {
		t.Fatalf("%d %v, length %d", resp.StatusCode, h, resp.ContentLength)
	}
	if !bytes.HasPrefix(body, []byte{0x1a, 0x45, 0xdf, 0xa3}) || len(body) < 50<<10 {
		t.Fatalf("%d bytes, % x", len(body), body[:min(8, len(body))])
	}
	// The ten seconds from 50 s, with the one audio track asked for.
	_, ffprobe := tm.Tools(t)
	out := filepath.Join(t.TempDir(), "out.mkv")
	os.WriteFile(out, body, 0o644)
	got, err := probe.New(ffprobe, 1).Probe(context.Background(), out)
	if err != nil || len(got.Streams) != 2 || got.Streams[0].Codec != "h264" || got.Streams[1].Kind != "audio" {
		t.Fatalf("the stream's streams: %+v %v", got.Streams, err)
	}
	waitUntil(t, "the slot is free", func() bool { return tr.Running() == 0 })
	waitUntil(t, "the gate is open", func() bool { return g.held() == 0 })

	// The session's first timed report logs the first byte and the encoder.
	b.call(t, "POST", "/api/v1/progress", b.token, "", map[string]any{"session": "one", "item_id": b.film(t, "Long Water").ID,
		"file_id": long.ID, "position_ms": 52000, "state": "playing", "mode": "transcode",
		"timings": map[string]any{"pressed_to_loaded_ms": 300, "pressed_to_playing_ms": 450}}, nil)
	var log struct {
		Rows []struct {
			Mode            string  `json:"mode"`
			ServerFirstByte *int64  `json:"server_first_byte_ms"`
			Encoder         *string `json:"encoder"`
			Warmed          bool    `json:"warmed"`
		} `json:"rows"`
	}
	b.call(t, "GET", "/api/v1/admin/playlog", b.adminToken, local, nil, &log)
	if len(log.Rows) != 1 || log.Rows[0].ServerFirstByte == nil || *log.Rows[0].ServerFirstByte < 0 ||
		log.Rows[0].Encoder == nil || *log.Rows[0].Encoder != "libx264" || log.Rows[0].Mode != "transcode" || log.Rows[0].Warmed {
		t.Fatalf("playlog %+v", log.Rows)
	}
}

// TestTranscodeSlotsAndDisconnects: a second transcode while the slot is
// held is 503 busy; ffmpeg is gone within a second of its client going,
// in 20 tries, and the slot comes back.
func TestTranscodeSlotsAndDisconnects(t *testing.T) {
	b, tr := newTranscodes(t, 1)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	long := b.fileOf(t, "Long Water")
	path := filepath.Join(b.films, longFilm)
	url := srv.URL + "/api/v1/files/" + idStr(long.ID) + "/transcode.mkv?v=h264&t=" + b.token
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", url+"&start_ms="+idStr(int64(i)*2000), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(resp.Body, make([]byte, 64<<10)); err != nil || resp.StatusCode != 200 {
			t.Fatalf("try %d: %d %v", i, resp.StatusCode, err)
		}
		if i == 0 {
			var e apiError
			rec := b.call(t, "GET", "/api/v1/files/"+idStr(long.ID)+"/transcode.mkv?v=h264", b.token, "", nil, &e)
			if rec.Code != 503 || e.Error.Code != "busy" || rec.Header().Get("Retry-After") != "5" {
				t.Fatalf("a second transcode: %d %+v %v", rec.Code, e, rec.Header())
			}
		}
		if n := ffmpegsOf(path); n != 1 {
			t.Fatalf("try %d: %d ffmpeg children while streaming", i, n)
		}
		cancel()
		resp.Body.Close()
		gone := time.Now()
		for ffmpegsOf(path) > 0 || tr.Running() > 0 {
			if time.Since(gone) > time.Second {
				t.Fatalf("try %d: ffmpeg still there a second after its client went", i)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
