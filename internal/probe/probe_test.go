// SPDX-License-Identifier: GPL-3.0-or-later

package probe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/model"
	tm "github.com/gureetk/runnel/internal/testmedia"
)

func newProber(t *testing.T) *Prober {
	_, ffprobe := tm.Tools(t)
	return New(ffprobe, 2)
}

func probeFile(t *testing.T, p *Prober, path string) model.Probe {
	t.Helper()
	got, err := p.Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("probe %s: %v", filepath.Base(path), err)
	}
	return got
}

func byKind(p model.Probe, kind string) []model.Stream {
	var out []model.Stream
	for _, s := range p.Streams {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// TestMatroska probes a Matroska file with everything a library file
// carries: two audio tracks with languages and a title, a styled and a
// plain subtitle track with their flags, an attached font, and chapters.
func TestMatroska(t *testing.T) {
	p := newProber(t)
	dir := t.TempDir()
	font := filepath.Join(dir, "font.ttf")
	os.WriteFile(font, []byte(strings.Repeat("not a real font ", 300)), 0o644) // 4,800 bytes
	ass := filepath.Join(dir, "subs.ass")
	os.WriteFile(ass, []byte("[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Sans,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Hello\n"), 0o644)
	meta := filepath.Join(dir, "chapters.txt")
	os.WriteFile(meta, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1000\ntitle=Opening\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=1000\nEND=2000\ntitle=Middle\n"), 0o644)

	video := []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"}
	wantDepth := 8
	if tm.HasEncoder(t, "libx265") {
		video = []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error"}
		wantDepth = 10
	}
	out := filepath.Join(dir, "Invented Film (2001).mkv")
	tm.Make(t, out, tm.Join(
		tm.Video("2"), tm.Tone("440", "2"), tm.Tone("660", "2"),
		[]string{"-i", ass, "-i", ass, "-i", meta},
		[]string{"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s", "-map", "4:s", "-map_chapters", "5"},
		video,
		[]string{"-c:a", "libopus", "-b:a", "48k", "-c:s:0", "ass", "-c:s:1", "srt"},
		[]string{"-metadata:s:a:0", "language=jpn", "-metadata:s:a:1", "language=eng", "-metadata:s:a:1", "title=Commentary"},
		[]string{"-metadata:s:s:0", "language=eng", "-metadata:s:s:0", "title=Full", "-metadata:s:s:1", "language=eng"},
		[]string{"-disposition:a:0", "default", "-disposition:a:1", "0", "-disposition:s:0", "default+forced", "-disposition:s:1", "hearing_impaired"},
		[]string{"-attach", font, "-metadata:s:t", "mimetype=font/ttf", "-metadata:s:t", "filename=Invented-Regular.ttf"},
	)...)

	got := probeFile(t, p, out)
	if got.Container != "matroska" {
		t.Errorf("container %q", got.Container)
	}
	if got.DurationMS < 1900 || got.DurationMS > 2200 || got.BitRate <= 0 {
		t.Errorf("duration %d ms, bit rate %d", got.DurationMS, got.BitRate)
	}
	if got.AttachmentsBytes != 4800 {
		t.Errorf("attachments %d bytes, want 4800", got.AttachmentsBytes)
	}

	v := byKind(got, "video")
	if len(v) != 1 || v[0].Index != 0 || v[0].Width != 320 || v[0].Height != 180 || v[0].BitDepth != wantDepth ||
		v[0].FPSNum != 25 || v[0].FPSDen != 1 || v[0].Level == nil || v[0].Profile == "" || v[0].HDR != "" {
		t.Errorf("video %+v", v)
	}
	a := byKind(got, "audio")
	if len(a) != 2 || a[0].Codec != "opus" || a[0].Lang != "jpn" || !a[0].Default || a[0].Channels != 1 || a[0].SampleRate != 48000 ||
		a[1].Lang != "eng" || a[1].Title != "Commentary" || a[1].Default {
		t.Errorf("audio %+v", a)
	}
	s := byKind(got, "subtitle")
	if len(s) != 2 || s[0].Codec != "ass" || s[0].Title != "Full" || !s[0].Default || !s[0].Forced || s[0].HearingImpaired ||
		s[1].Codec != "subrip" || !s[1].HearingImpaired || s[1].Forced {
		t.Errorf("subtitles %+v", s)
	}
	att := byKind(got, "attachment")
	if len(att) != 1 || att[0].Filename != "Invented-Regular.ttf" || att[0].MIME != "font/ttf" || att[0].Size != 4800 {
		t.Errorf("attachments %+v", att)
	}
	if len(got.Chapters) != 2 || got.Chapters[0].StartMS != 0 || got.Chapters[0].EndMS == nil || *got.Chapters[0].EndMS != 1000 ||
		got.Chapters[0].Title != "Opening" || got.Chapters[1].StartMS != 1000 || got.Chapters[1].Title != "Middle" {
		t.Errorf("chapters %+v", got.Chapters)
	}
}

func TestHDR(t *testing.T) {
	p := newProber(t)
	if !tm.HasEncoder(t, "libx265") {
		t.Skip("this ffmpeg has no libx265")
	}
	dir := t.TempDir()
	for transfer, want := range map[string]string{"smpte2084": "hdr10", "arib-std-b67": "hlg", "bt709": ""} {
		out := filepath.Join(dir, transfer+".mkv")
		tm.Make(t, out, tm.Join(tm.Video("1"),
			[]string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error:transfer=" + transfer, "-color_trc", transfer})...)
		v := byKind(probeFile(t, p, out), "video")
		if len(v) != 1 || v[0].HDR != want || v[0].BitDepth != 10 || v[0].Codec != "hevc" {
			t.Errorf("%s: %+v, want hdr %q", transfer, v, want)
		}
	}
}

// TestContainers probes one file in each container the scanner counts.
func TestContainers(t *testing.T) {
	p := newProber(t)
	dir := t.TempDir()
	cases := []struct {
		name, want string
		codecs     []string
	}{
		{"a.mkv", "matroska", []string{"-c:v", "libx264", "-c:a", "aac"}},
		{"a.webm", "webm", []string{"-c:v", "libvpx-vp9", "-deadline", "realtime", "-c:a", "libopus"}},
		{"a.mp4", "mp4", []string{"-c:v", "libx264", "-c:a", "aac"}},
		{"a.m4v", "mp4", []string{"-c:v", "libx264", "-c:a", "aac"}},
		{"a.mov", "mov", []string{"-c:v", "libx264", "-c:a", "aac"}},
		{"a.avi", "avi", []string{"-c:v", "mpeg4", "-c:a", "mp3"}},
		{"a.ts", "mpegts", []string{"-c:v", "libx264", "-c:a", "aac"}},
		{"a.m2ts", "mpegts", []string{"-c:v", "libx264", "-c:a", "aac", "-f", "mpegts"}},
		{"a.wmv", "asf", []string{"-c:v", "wmv2", "-c:a", "wmav2"}},
		{"a.mpg", "mpeg", []string{"-c:v", "mpeg2video", "-c:a", "mp2"}},
	}
	for _, c := range cases {
		codecs := c.codecs
		if c.name == "a.avi" && !tm.HasEncoder(t, "libmp3lame") {
			codecs = []string{"-c:v", "mpeg4", "-c:a", "ac3"}
		}
		out := filepath.Join(dir, c.name)
		tm.Make(t, out, tm.Join(tm.Video("1"), tm.Tone("440", "1"), codecs)...)
		got := probeFile(t, p, out)
		if got.Container != c.want || len(byKind(got, "video")) != 1 || len(byKind(got, "audio")) != 1 {
			t.Errorf("%s: container %q, %d streams, want %q", c.name, got.Container, len(got.Streams), c.want)
		}
		if MediaType(got.Container) == "application/octet-stream" {
			t.Errorf("%s: no media type for %q", c.name, got.Container)
		}
	}
}

// TestCoverArtAndSurround: a picture attached to an MP4 is not a stream to
// play, and six channels are read as 5.1.
func TestCoverArtAndSurround(t *testing.T) {
	p := newProber(t)
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.jpg")
	tm.Make(t, cover, "-f", "lavfi", "-i", "color=red:size=64x64:duration=0.04", "-frames:v", "1", "-pix_fmt", "yuvj420p")
	out := filepath.Join(dir, "a.mp4")
	tm.Make(t, out, tm.Join(tm.Video("1"),
		[]string{"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-i", cover},
		[]string{"-filter_complex", "[1:a]pan=5.1|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0|c5=c0[a]"},
		[]string{"-map", "0:v", "-map", "[a]", "-map", "2:v", "-c:v:0", "libx264", "-c:a", "aac", "-c:v:1", "copy", "-disposition:v:1", "attached_pic"})...)
	got := probeFile(t, p, out)
	v, a := byKind(got, "video"), byKind(got, "audio")
	if len(v) != 1 || v[0].Codec != "h264" {
		t.Errorf("video streams %+v: the cover must not be one", v)
	}
	if len(a) != 1 || a[0].Channels != 6 || a[0].ChannelLayout != "5.1" || a[0].Profile != "LC" || a[0].BitRate <= 0 {
		t.Errorf("audio %+v", a)
	}
}

func TestUnreadable(t *testing.T) {
	p := newProber(t)
	dir := t.TempDir()
	junk := filepath.Join(dir, "Invented Film (2001).mkv")
	os.WriteFile(junk, []byte(strings.Repeat("not a video ", 1000)), 0o644)
	empty := filepath.Join(dir, "empty.mp4")
	os.WriteFile(empty, nil, 0o644)
	for _, path := range []string{junk, empty} {
		_, err := p.Probe(context.Background(), path)
		var u *UnreadableError
		if !errors.As(err, &u) {
			t.Fatalf("%s: %v, want an UnreadableError", filepath.Base(path), err)
		}
		if u.Msg == "" || strings.Contains(u.Msg, dir) {
			t.Errorf("%s: message %q: want ffprobe's reason without the path", filepath.Base(path), u.Msg)
		}
	}
}

func TestNoFFprobe(t *testing.T) {
	p := New(filepath.Join(t.TempDir(), "no-ffprobe-here"), 2)
	_, err := p.Probe(context.Background(), "/dev/null")
	if !errors.Is(err, ErrNoFFprobe) {
		t.Fatalf("%v, want ErrNoFFprobe", err)
	}
}

// ffprobeChildren counts the ffprobe processes reading one of paths.
func ffprobeChildren(t *testing.T, paths []string) int {
	t.Helper()
	procs, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	n := 0
	for _, f := range procs {
		b, err := os.ReadFile(f)
		if err != nil || !strings.Contains(string(b), "ffprobe") {
			continue
		}
		for _, p := range paths {
			if strings.Contains(string(b), p) {
				n++
			}
		}
	}
	return n
}

// TestChildrenAreLimitedAndKilled probes named pipes that no one writes
// to, so ffprobe waits on each forever. Two run at once, the third waits
// for a slot, and ending the context kills them all.
func TestChildrenAreLimitedAndKilled(t *testing.T) {
	p := newProber(t)
	p.depthsOnce.Do(func() { p.depths = p.loadDepths(context.Background()) })
	dir := t.TempDir()
	var fifos []string
	for _, name := range []string{"a.mkv", "b.mkv", "c.mkv"} {
		f := filepath.Join(dir, name)
		if err := syscall.Mkfifo(f, 0o600); err != nil {
			t.Skipf("no named pipes here: %v", err)
		}
		fifos = append(fifos, f)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, len(fifos))
	for _, f := range fifos {
		go func() { _, err := p.Probe(ctx, f); done <- err }()
	}
	var running int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if running = ffprobeChildren(t, fifos); running == 2 {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	if running = ffprobeChildren(t, fifos); running != 2 {
		cancel()
		t.Fatalf("%d ffprobe children at once, want 2", running)
	}

	start := time.Now()
	cancel()
	for range fifos {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("a stopped probe returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a probe did not stop when its context ended")
		}
	}
	if n := ffprobeChildren(t, fifos); n != 0 {
		t.Fatalf("%d ffprobe children left after cancelling", n)
	}
	t.Logf("all stopped %v after cancelling", time.Since(start))
}

// TestMapping covers what generated files cannot: a Dolby Vision record,
// values ffprobe prints as "N/A" or -99, data streams, tags in upper case.
// The JSON is in the shape ffprobe prints; the Dolby Vision side data name
// is the one in ffmpeg 6.1's libavcodec.
func TestMapping(t *testing.T) {
	raw := `{
	  "streams": [
	    {"index": 0, "codec_type": "video", "codec_name": "hevc", "profile": "Main 10", "level": 153,
	     "width": 3840, "height": 2160, "pix_fmt": "yuv420p10le", "avg_frame_rate": "24000/1001",
	     "color_transfer": "smpte2084", "side_data_list": [{"side_data_type": "DOVI configuration record", "dv_profile": 8}],
	     "disposition": {"default": 1, "attached_pic": 0}, "tags": {"LANGUAGE": "und"}},
	    {"index": 1, "codec_type": "audio", "codec_name": "eac3", "channels": 6, "channel_layout": "5.1(side)",
	     "sample_rate": "48000", "bit_rate": "N/A", "disposition": {"default": 0}, "tags": {"language": "fre", "TITLE": "Invented Dub"}},
	    {"index": 2, "codec_type": "data", "codec_name": "bin_data"},
	    {"index": 3, "codec_type": "video", "codec_name": "vp9", "level": -99, "pix_fmt": "made_up_fmt", "avg_frame_rate": "0/0"},
	    {"index": 4, "codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle", "disposition": {"forced": 1}}
	  ],
	  "chapters": [{"start_time": "0.000000", "end_time": "N/A", "tags": {"TITLE": "Only"}}],
	  "format": {"format_name": "matroska,webm", "duration": "N/A", "bit_rate": "12000000"}
	}`
	var out output
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	got := mapOutput(out, "/x/Invented Film (2001).webm", map[string]int{"yuv420p10le": 10})
	if got.Container != "webm" || got.DurationMS != 0 || got.BitRate != 12000000 {
		t.Errorf("format: %+v", got)
	}
	if len(got.Streams) != 4 {
		t.Fatalf("%d streams, want 4 (the data stream left out): %+v", len(got.Streams), got.Streams)
	}
	v := got.Streams[0]
	if v.HDR != "dv" || v.BitDepth != 10 || v.Level == nil || *v.Level != 153 || v.FPSNum != 24000 || v.FPSDen != 1001 || v.Lang != "und" || !v.Default {
		t.Errorf("video %+v", v)
	}
	a := got.Streams[1]
	if a.Lang != "fre" || a.Title != "Invented Dub" || a.BitRate != 0 || a.SampleRate != 48000 || a.ChannelLayout != "5.1(side)" {
		t.Errorf("audio %+v", a)
	}
	w := got.Streams[2]
	if w.Index != 3 || w.Level != nil || w.FPSNum != 0 || w.BitDepth != 8 {
		t.Errorf("video with unknowns %+v", w)
	}
	if s := got.Streams[3]; s.Kind != "subtitle" || !s.Forced {
		t.Errorf("subtitle %+v", s)
	}
	if len(got.Chapters) != 1 || got.Chapters[0].EndMS != nil || got.Chapters[0].Title != "Only" {
		t.Errorf("chapters %+v", got.Chapters)
	}
}

func TestBitDepth(t *testing.T) {
	table := map[string]int{"rgb48le": 16, "yuv420p": 8}
	for pix, want := range map[string]int{"rgb48le": 16, "yuv420p": 8, "yuv420p10le": 10, "p010le": 10, "yuv444p12be": 12, "nv12": 8, "": 0} {
		if got := bitDepth(pix, table); got != want {
			t.Errorf("bitDepth(%q) = %d, want %d", pix, got, want)
		}
	}
}

func TestDepthTableFromFFprobe(t *testing.T) {
	p := newProber(t)
	d := p.loadDepths(context.Background())
	for pix, want := range map[string]int{"yuv420p": 8, "yuv420p10le": 10, "p010le": 10, "rgb48le": 16} {
		if d[pix] != want {
			t.Errorf("%s: %d, want %d (from ffprobe -pix_fmts)", pix, d[pix], want)
		}
	}
}
