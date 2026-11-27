// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/testmedia"
)

// fakeFFmpeg writes a shell script that plays ffmpeg for the check: it
// lists the encoders given, writes each encode's arguments into log, and
// fails an encode whose arguments hold fail, or sleeps in one that holds
// slow.
func fakeFFmpeg(t *testing.T, encoders, fail, slow string) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "args")
	path = filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
if [ "$2" = -encoders ]; then
  echo "Encoders:"
  echo " V..... = Video"
  echo " ------"
  for e in ` + encoders + `; do echo " V....D $e  an encoder"; done
  exit 0
fi
echo "$*" >> ` + log + `
case "$*" in
  *` + fail + `*) echo "[hevc_vaapi @ 0x1] No usable encoding profile found." >&2; echo "second line" >&2; exit 1 ;;
  *` + slow + `*) exec sleep 5 ;;
esac
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, log
}

func errors_(r Result) []string {
	var out []string
	for _, row := range r.Rows {
		out = append(out, row.Error)
	}
	return out
}

// TestTheCheckOnAGPU: with a device, each hardware target encodes with the
// arguments a transcode uses, and one that fails says why.
func TestTheCheckOnAGPU(t *testing.T) {
	ffmpeg, log := fakeFFmpeg(t, "hevc_vaapi h264_vaapi libx264", "main10", "no-such-thing")
	at := time.UnixMilli(1795000001000)
	res := Check{FFmpeg: ffmpeg, HWAccel: HWAuto, Device: "/dev/null", Now: func() time.Time { return at }}.Run(context.Background())
	want := []Row{
		{V: "hevc", Depth: 10, How: "hardware", Encoder: "hevc_vaapi", Error: "[hevc_vaapi @ 0x1] No usable encoding profile found."},
		{V: "hevc", Depth: 8, How: "hardware", Encoder: "hevc_vaapi", Works: true},
		{V: "h264", Depth: 8, How: "hardware", Encoder: "h264_vaapi", Works: true},
		{V: "h264", Depth: 8, How: "software", Encoder: "libx264", Works: true},
	}
	if !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("rows\n got %+v\nwant %+v", res.Rows, want)
	}
	if !res.CheckedAt.Equal(at) || res.HW() != "vaapi" {
		t.Errorf("checked at %v, hw %s", res.CheckedAt, res.HW())
	}
	video, depth10 := res.Video()
	if !reflect.DeepEqual(video, []string{"hevc", "h264"}) || len(depth10) != 0 {
		t.Errorf("video %v, depth10 %v", video, depth10)
	}
	if h, s := res.Can("h264", 8); !h || !s {
		t.Errorf("h264: hardware %v, software %v", h, s)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	pattern := "-hide_banner -loglevel error -nostdin -vaapi_device /dev/null -f lavfi -i testsrc2=size=1280x720:rate=24 -t 1 "
	wantArgs := []string{
		pattern + "-vf format=p010,hwupload -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0 -f null -",
		pattern + "-vf format=nv12,hwupload -c:v hevc_vaapi -profile:v main -rc_mode CQP -qp 24 -bf 0 -f null -",
		pattern + "-vf format=nv12,hwupload -c:v h264_vaapi -profile:v high -rc_mode CQP -qp 23 -bf 0 -f null -",
		"-hide_banner -loglevel error -nostdin -f lavfi -i testsrc2=size=1280x720:rate=24 -t 1 -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p -f null -",
	}
	if !reflect.DeepEqual(lines, wantArgs) {
		t.Errorf("encodes\n got %q\nwant %q", lines, wantArgs)
	}
}

// TestWhatTheCheckLeavesOut: no encode runs for a target that cannot
// work, and each says why.
func TestWhatTheCheckLeavesOut(t *testing.T) {
	ffmpeg, log := fakeFFmpeg(t, "hevc_vaapi libx264", "never", "never")
	cases := []struct {
		name  string
		check Check
		want  []string
	}{
		{"RUNNEL_HWACCEL=none", Check{FFmpeg: ffmpeg, HWAccel: HWNone, Device: "/dev/null"},
			[]string{"RUNNEL_HWACCEL is none", "RUNNEL_HWACCEL is none", "RUNNEL_HWACCEL is none", ""}},
		{"no device", Check{FFmpeg: ffmpeg, HWAccel: HWAuto, Device: "/nonexistent/renderD128"},
			[]string{"no render device at /nonexistent/renderD128", "no render device at /nonexistent/renderD128", "no render device at /nonexistent/renderD128", ""}},
		{"a file, not a device", Check{FFmpeg: ffmpeg, HWAccel: HWVAAPI, Device: log + ".none"},
			nil},
		{"an encoder this ffmpeg lacks", Check{FFmpeg: ffmpeg, HWAccel: HWAuto, Device: "/dev/null"},
			[]string{"", "", "this ffmpeg has no h264_vaapi", ""}},
		{"no ffmpeg", Check{HWAccel: HWAuto, Device: "/dev/null"},
			[]string{"ffmpeg is not on PATH", "ffmpeg is not on PATH", "ffmpeg is not on PATH", "ffmpeg is not on PATH"}},
	}
	notDevice := log + ".none"
	if err := os.WriteFile(notDevice, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases[2].want = []string{notDevice + " is not a device", notDevice + " is not a device", notDevice + " is not a device", ""}
	for _, c := range cases {
		res := c.check.Run(context.Background())
		if got := errors_(res); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if c.name != "an encoder this ffmpeg lacks" && res.HW() != "none" {
			t.Errorf("%s: hw %s", c.name, res.HW())
		}
	}
}

func TestASlowEncodeIsCut(t *testing.T) {
	ffmpeg, _ := fakeFFmpeg(t, "libx264", "never", "libx264")
	res := Check{FFmpeg: ffmpeg, HWAccel: HWNone, Timeout: 200 * time.Millisecond}.Run(context.Background())
	if got := res.Rows[3].Error; got != "the test encode took longer than 200ms" {
		t.Errorf("error %q", got)
	}
}

// TestTheCheckWithThisFFmpeg runs the check on the ffmpeg on PATH, with no
// render device: software H.264 is all there is.
func TestTheCheckWithThisFFmpeg(t *testing.T) {
	ffmpeg, _ := testmedia.Tools(t)
	if !testmedia.HasEncoder(t, "libx264") {
		t.Skip("this ffmpeg has no libx264")
	}
	res := Check{FFmpeg: ffmpeg, HWAccel: HWAuto, Device: filepath.Join(t.TempDir(), "renderD128")}.Run(context.Background())
	sw := res.Rows[3]
	if !sw.Works || sw.Error != "" {
		t.Fatalf("libx264: %+v", sw)
	}
	video, depth10 := res.Video()
	if !reflect.DeepEqual(video, []string{"h264"}) || len(depth10) != 0 || res.HW() != "none" {
		t.Errorf("video %v, depth10 %v, hw %s", video, depth10, res.HW())
	}
}

func TestACheckWaitsForNoTranscode(t *testing.T) {
	ffmpeg, _ := fakeFFmpeg(t, "libx264", "never", "never")
	e := NewEncoders(Check{FFmpeg: ffmpeg, HWAccel: HWNone, Device: "/dev/null"})
	if got := e.Last(); !got.CheckedAt.IsZero() || len(got.Rows) != 0 {
		t.Fatalf("before any check: %+v", got)
	}
	busy := true
	e.Busy = func() bool { return busy }
	if _, err := e.Check(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("while busy: %v", err)
	}
	busy = false
	res, err := e.Check(context.Background())
	if err != nil || !res.Rows[3].Works || !reflect.DeepEqual(e.Last(), res) {
		t.Fatalf("check: %+v, %v", res, err)
	}
	if e.Hardware().Device != "/dev/null" {
		t.Errorf("hardware %+v", e.Hardware())
	}
}
