// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/testmedia"
)

var av1 = Source{Path: "/media/films/Invented Film (2020)/Invented Film (2020).mkv",
	Codec: "av1", Profile: "Main", Depth: 10, Height: 1080}

func all(t Target) Target {
	t.Audio, t.Subs, t.Fonts = AllAudio, true, true
	return t
}

// TestArgs holds the builder to docs/05-playback.md, sections 4.1 to 4.3:
// a request in, an argument array out.
func TestArgs(t *testing.T) {
	vaapi := Hardware{Device: DefaultDevice}
	head := "-hide_banner -loglevel error -nostdin "
	in := "-i " + av1.Path + " -map 0:v:0 "
	tail := " -copyts -max_interleave_delta 500000 -f matroska -flush_packets 1 pipe:1"
	cases := []struct {
		name string
		src  Source
		t    Target
		hw   Hardware
		r    Rung
		want string
	}{
		{"HEVC 10-bit on the GPU, as run on real hardware", av1,
			all(Target{V: "hevc", Depth: 10, StartMS: 65000}), vaapi, HardwareBoth,
			head + "-hwaccel vaapi -hwaccel_output_format vaapi -noautorotate -ss 65.000 -noaccurate_seek " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale_vaapi=format=p010 -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0" + tail},
		{"HEVC 8-bit on the GPU", av1,
			all(Target{V: "hevc", Depth: 8}), vaapi, HardwareBoth,
			head + "-hwaccel vaapi -hwaccel_output_format vaapi -noautorotate " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale_vaapi=format=nv12 -c:v hevc_vaapi -profile:v main -rc_mode CQP -qp 24 -bf 0" + tail},
		{"H.264 on the GPU", av1,
			all(Target{V: "h264", Depth: 8}), vaapi, HardwareBoth,
			head + "-hwaccel vaapi -hwaccel_output_format vaapi -noautorotate " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale_vaapi=format=nv12 -c:v h264_vaapi -profile:v high -rc_mode CQP -qp 23 -bf 0" + tail},
		{"another render device is named", av1,
			all(Target{V: "hevc", Depth: 10}), Hardware{Device: "/dev/dri/renderD129"}, HardwareBoth,
			head + "-hwaccel vaapi -hwaccel_device /dev/dri/renderD129 -hwaccel_output_format vaapi -noautorotate " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale_vaapi=format=p010 -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0" + tail},
		{"software decode, hardware encode", av1,
			all(Target{V: "hevc", Depth: 10, StartMS: 65000}), vaapi, HardwareEncode,
			head + "-vaapi_device /dev/dri/renderD128 -ss 65.000 -noaccurate_seek " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf format=p010,hwupload -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0" + tail},
		{"software H.264, the tested form", av1,
			all(Target{V: "h264", Depth: 8, StartMS: 65000}), vaapi, SoftwareH264,
			head + "-ss 65.000 -noaccurate_seek " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p" + tail},
		{"an exact seek has no -noaccurate_seek", av1,
			all(Target{V: "h264", Depth: 8, StartMS: 1234567, Exact: true}), vaapi, SoftwareH264,
			head + "-ss 1234.567 " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p" + tail},
		{"one audio stream, no subtitles, no fonts", av1,
			Target{V: "h264", Depth: 8, Audio: 3}, vaapi, SoftwareH264,
			head + in + "-map 0:3 -c copy -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p" + tail},
		{"no audio, for the sidecar", av1,
			Target{V: "h264", Depth: 8, Audio: NoAudio, Subs: false, Fonts: false}, vaapi, SoftwareH264,
			head + in + "-c copy -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p" + tail},
		{"max_h on the GPU", av1,
			all(Target{V: "hevc", Depth: 10, MaxH: 720}), vaapi, HardwareBoth,
			head + "-hwaccel vaapi -hwaccel_output_format vaapi -noautorotate " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale_vaapi=w=-2:h=720:format=p010 -c:v hevc_vaapi -profile:v main10 -rc_mode CQP -qp 24 -bf 0" + tail},
		{"max_h, software decode, hardware encode", av1,
			all(Target{V: "h264", Depth: 8, MaxH: 720}), vaapi, HardwareEncode,
			head + "-vaapi_device /dev/dri/renderD128 " + in +
				"-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale=-2:720,format=nv12,hwupload -c:v h264_vaapi -profile:v high -rc_mode CQP -qp 23 -bf 0" + tail},
		{"max_h in software", av1,
			all(Target{V: "h264", Depth: 8, MaxH: 720}), vaapi, SoftwareH264,
			head + in + "-map 0:a? -map 0:s? -map 0:t? -c copy -vf scale=-2:720 -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p" + tail},
		{"max_h at or above the source's height scales nothing", av1,
			all(Target{V: "h264", Depth: 8, MaxH: 1080}), vaapi, SoftwareH264,
			head + in + "-map 0:a? -map 0:s? -map 0:t? -c copy -c:v libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p" + tail},
	}
	for _, c := range cases {
		got := Args(c.src, c.t, c.hw, c.r)
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, strings.Join(got, " "), c.want)
		}
	}
}

// TestAPathIsOneArgument: a path with spaces, quotes and a leading dash
// stays one argument, after -i.
func TestAPathIsOneArgument(t *testing.T) {
	src := av1
	src.Path = `/media/films/-it's "odd" (2020)/a b.mkv`
	a := Args(src, all(Target{V: "h264", Depth: 8}), Hardware{}, SoftwareH264)
	for i, s := range a {
		if s == "-i" {
			if a[i+1] != src.Path {
				t.Fatalf("after -i: %q", a[i+1])
			}
			return
		}
	}
	t.Fatal("no -i")
}

func TestLadder(t *testing.T) {
	cases := []struct {
		t                  Target
		hardware, software bool
		want               []Rung
	}{
		{Target{V: "hevc", Depth: 10}, true, true, []Rung{HardwareBoth, HardwareEncode}},
		{Target{V: "h264", Depth: 8}, true, true, []Rung{HardwareBoth, HardwareEncode, SoftwareH264}},
		{Target{V: "h264", Depth: 8}, false, true, []Rung{SoftwareH264}},
		{Target{V: "hevc", Depth: 8}, false, true, nil},
		{Target{V: "h264", Depth: 8}, true, false, []Rung{HardwareBoth, HardwareEncode}},
		{Target{V: "h264", Depth: 8}, false, false, nil},
	}
	for _, c := range cases {
		if got := Ladder(c.t, c.hardware, c.software); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Ladder(%+v, %v, %v) = %v, want %v", c.t, c.hardware, c.software, got, c.want)
		}
	}
	if Encoder(Target{V: "hevc"}, HardwareEncode) != "hevc_vaapi" || Encoder(Target{V: "h264"}, HardwareBoth) != "h264_vaapi" ||
		Encoder(Target{V: "h264"}, SoftwareH264) != "libx264" {
		t.Error("Encoder names the wrong encoder")
	}
}

// TestFailuresAreRemembered by the source's codec, profile and depth and
// the target, so that the next request starts on the rung that works.
func TestFailuresAreRemembered(t *testing.T) {
	var f Failures
	h264 := Target{V: "h264", Depth: 8}
	rungs := Ladder(h264, true, true)
	f.Failed(av1, h264, HardwareBoth)
	if got := f.Skip(av1, h264, rungs); !reflect.DeepEqual(got, []Rung{HardwareEncode, SoftwareH264}) {
		t.Errorf("after rung 1 failed: %v", got)
	}
	other := av1
	other.Path = "/media/another.mkv" // the same kind of source
	f.Failed(other, h264, HardwareEncode)
	if got := f.Skip(av1, h264, rungs); !reflect.DeepEqual(got, []Rung{SoftwareH264}) {
		t.Errorf("after rung 2 failed too: %v", got)
	}
	eightBit := av1
	eightBit.Depth = 8
	if got := f.Skip(eightBit, h264, rungs); !reflect.DeepEqual(got, rungs) {
		t.Errorf("another bit depth: %v", got)
	}
	if got := f.Skip(av1, Target{V: "hevc", Depth: 10}, Ladder(Target{V: "hevc", Depth: 10}, true, true)); len(got) != 2 {
		t.Errorf("another target: %v", got)
	}
}

// TestTheSoftwareRungStreams runs the software rung on a generated file
// with two audio tracks, an ASS track and a font: the output is Matroska
// from a keyframe at or before the start, with the file's own timestamps,
// the streams asked for, and H.264 video.
func TestTheSoftwareRungStreams(t *testing.T) {
	ffmpeg, ffprobe := testmedia.Tools(t)
	if !testmedia.HasEncoder(t, "libx264") {
		t.Skip("this ffmpeg has no libx264")
	}
	dir := t.TempDir()
	ass := filepath.Join(dir, "subs.ass")
	writeASS(t, ass)
	font := filepath.Join(dir, "font.ttf")
	if err := writeFile(font, bytes.Repeat([]byte{0}, 4096)); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "Invented Film (2021).mkv")
	testmedia.Make(t, src, testmedia.Join(
		[]string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=12"},
		testmedia.Tone("440", "12"), testmedia.Tone("660", "12"),
		[]string{"-i", ass, "-attach", font, "-metadata:s:t", "mimetype=font/ttf",
			"-map", "0", "-map", "1", "-map", "2", "-map", "3",
			"-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-c:a", "aac", "-c:s", "ass"},
	)...)

	run := func(tg Target) (string, []byte) {
		out := filepath.Join(dir, "out.mkv")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ffmpeg, Args(Source{Path: src, Height: 180}, tg, Hardware{}, SoftwareH264)...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, stderr.String())
		}
		if err := writeFile(out, stdout.Bytes()); err != nil {
			t.Fatal(err)
		}
		return out, stdout.Bytes()
	}

	out, b := run(all(Target{V: "h264", Depth: 8, StartMS: 5000}))
	if !bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		t.Fatalf("the output is not Matroska: % x", b[:min(len(b), 8)])
	}
	streams := probeStreams(t, ffprobe, out)
	if got := strings.Join(streams, ","); got != "video:h264,audio:aac,audio:aac,subtitle:ass,attachment:" {
		t.Errorf("streams %s", got)
	}
	// A keyframe at or before 5 s, where the demuxer's seek lands, and the
	// file's own time rather than zero. Keyframes are a second apart.
	first := firstVideoTime(t, ffprobe, out)
	if first < 3.9 || first > 5.001 {
		t.Errorf("the first frame is at %.3f s, not at a keyframe at or before 5 s", first)
	}

	out, _ = run(Target{V: "h264", Depth: 8, Audio: 2, StartMS: 0})
	if got := strings.Join(probeStreams(t, ffprobe, out), ","); got != "video:h264,audio:aac" {
		t.Errorf("one audio stream: %s", got)
	}
}

func writeFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}

func writeASS(t *testing.T, path string) {
	t.Helper()
	s := "[Script Info]\nScriptType: v4.00+\nPlayResX: 320\nPlayResY: 180\n\n" +
		"[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
		"Style: Default,Sans,20,&H00FFFFFF,&H00FFFFFF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n" +
		"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:06.00,0:00:08.00,Default,,0,0,0,,An invented line\n"
	if err := writeFile(path, []byte(s)); err != nil {
		t.Fatal(err)
	}
}

func probeStreams(t *testing.T, ffprobe, path string) []string {
	t.Helper()
	b, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var v struct {
		Streams []struct {
			Type string `json:"codec_type"`
			Name string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range v.Streams {
		out = append(out, s.Type+":"+s.Name)
	}
	return out
}

func firstVideoTime(t *testing.T, ffprobe, path string) float64 {
	t.Helper()
	b, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-read_intervals", "%+#1", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var f float64
	if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &f); err != nil {
		t.Fatalf("pts %q: %v", b, err)
	}
	return f
}
