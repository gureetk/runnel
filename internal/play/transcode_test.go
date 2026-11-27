// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/testmedia"
)

// caps is a fixed check result.
type caps struct {
	rows   []Row
	device string
}

func (c caps) Last() Result       { return Result{CheckedAt: time.Now(), Rows: c.rows} }
func (c caps) Hardware() Hardware { return Hardware{Device: c.device} }

var softwareOnly = caps{rows: []Row{{V: "h264", Depth: 8, How: "software", Encoder: "libx264", Works: true}}}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// film makes a 20-second film with a picture and a tone, once per test.
func film(t *testing.T) Source {
	t.Helper()
	ffmpeg, _ := testmedia.Tools(t)
	_ = ffmpeg
	if !testmedia.HasEncoder(t, "libx264") {
		t.Skip("this ffmpeg has no libx264")
	}
	path := filepath.Join(t.TempDir(), "Invented Film (2022).mkv")
	testmedia.Make(t, path, testmedia.Join(
		[]string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=20"}, testmedia.Tone("440", "20"),
		[]string{"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-c:a", "aac"})...)
	return Source{Path: path, Codec: "h264", Profile: "High", Depth: 8, Height: 180}
}

func newTranscoder(t *testing.T, c Capabilities, slots int) (*Transcoder, *logBuf) {
	t.Helper()
	ffmpeg, _ := testmedia.Tools(t)
	logs := &logBuf{}
	tr := NewTranscoder(TranscoderConfig{FFmpeg: ffmpeg, Slots: slots, Log: slog.New(slog.NewTextHandler(logs, nil))}, c)
	t.Cleanup(tr.Close)
	return tr, logs
}

var h264 = Target{V: "h264", Depth: 8, Audio: AllAudio, Subs: true, Fonts: true}

func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestATranscodeStreamsToTheEnd(t *testing.T) {
	src := film(t)
	tr, _ := newTranscoder(t, softwareOnly, 1)
	c, err := tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatal(err)
	}
	if c.Encoder != "libx264" || c.Rung != SoftwareH264 || tr.Running() != 1 {
		t.Fatalf("encoder %s, rung %v, running %d", c.Encoder, c.Rung, tr.Running())
	}
	var out bytes.Buffer
	flushes := 0
	if err := c.Relay(&out, func() { flushes++ }); err != nil {
		t.Fatal(err)
	}
	c.Stop()
	if !bytes.HasPrefix(out.Bytes(), []byte{0x1a, 0x45, 0xdf, 0xa3}) || out.Len() < 100<<10 || flushes < 2 {
		t.Fatalf("%d bytes, %d flushes", out.Len(), flushes)
	}
	if tr.Running() != 0 {
		t.Errorf("running %d after the end", tr.Running())
	}
	c.Stop() // again: nothing
}

// TestSlots: a second transcode while the one slot is held is refused,
// and the slot comes back when the first stops.
func TestSlots(t *testing.T) {
	src := film(t)
	tr, _ := newTranscoder(t, softwareOnly, 1)
	first, err := tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Start(context.Background(), src, h264); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a second: %v", err)
	}
	first.Stop()
	second, err := tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatalf("after the first stopped: %v", err)
	}
	second.Stop()
}

// TestStopKillsAtOnce: ffmpeg blocked on a full pipe, as when a client
// stops reading, is gone as soon as Stop returns (rule 12).
func TestStopKillsAtOnce(t *testing.T) {
	src := film(t)
	tr, _ := newTranscoder(t, softwareOnly, 1)
	for i := 0; i < 5; i++ {
		c, err := tr.Start(context.Background(), src, h264)
		if err != nil {
			t.Fatal(err)
		}
		pid := c.PID()
		time.Sleep(100 * time.Millisecond) // ffmpeg fills the pipe and blocks
		start := time.Now()
		c.Stop()
		if took := time.Since(start); took > time.Second || !gone(pid) {
			t.Fatalf("try %d: ffmpeg %d still there %v after Stop began", i, pid, took)
		}
	}
}

// TestTheLadder: a GPU that fails moves the transcode down to software,
// and the next transcode of the same kind starts there.
func TestTheLadder(t *testing.T) {
	src := film(t)
	gpu := caps{device: "/dev/null", rows: append([]Row{{V: "h264", Depth: 8, How: "hardware", Encoder: "h264_vaapi", Works: true}}, softwareOnly.rows...)}
	tr, logs := newTranscoder(t, gpu, 1)
	c, err := tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatal(err)
	}
	c.Stop()
	if c.Rung != SoftwareH264 {
		t.Fatalf("rung %v", c.Rung)
	}
	if n := strings.Count(logs.String(), "a transcode failed before its first output"); n != 2 {
		t.Fatalf("%d rungs failed, want 2:\n%s", n, logs)
	}
	c, err = tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatal(err)
	}
	c.Stop()
	if n := strings.Count(logs.String(), "a transcode failed before its first output"); n != 2 || c.Rung != SoftwareH264 {
		t.Errorf("the second transcode tried again: %d failures, rung %v", n, c.Rung)
	}
	// Another target keeps its own ladder: HEVC has no software rung, and
	// both hardware rungs fail.
	gpu.rows = append(gpu.rows, Row{V: "hevc", Depth: 8, How: "hardware", Encoder: "hevc_vaapi", Works: true})
	tr2, _ := newTranscoder(t, gpu, 1)
	var failed *FailedError
	if _, err := tr2.Start(context.Background(), src, Target{V: "hevc", Depth: 8, Audio: AllAudio}); !errors.As(err, &failed) || failed.Message == "" {
		t.Fatalf("HEVC on a GPU that fails: %v", err)
	}
	if tr2.Running() != 0 {
		t.Error("a failed transcode kept its slot")
	}
	if _, err := tr2.Start(context.Background(), src, Target{V: "hevc", Depth: 8, Audio: AllAudio}); !errors.As(err, &failed) ||
		failed.Message != "every way to encode this failed earlier" {
		t.Errorf("again: %v", err)
	}
	if _, err := tr2.Start(context.Background(), src, Target{V: "hevc", Depth: 10}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a target the check found nothing for: %v", err)
	}
}

func TestTheMemoryLimit(t *testing.T) {
	src := film(t)
	ffmpeg, _ := testmedia.Tools(t)
	logs := &logBuf{}
	tr := NewTranscoder(TranscoderConfig{FFmpeg: ffmpeg, Slots: 1, Log: slog.New(slog.NewTextHandler(logs, nil))}, softwareOnly)
	tr.rssEvery = 20 * time.Millisecond
	c, err := tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatal(err)
	}
	// Under way, the limit drops below what ffmpeg holds: the stream ends.
	tr.maxRSS.Store(4 << 20)
	done := make(chan error, 1)
	go func() { done <- c.Relay(io.Discard, func() {}) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end")
	}
	c.Stop()
	if !strings.Contains(logs.String(), "ffmpeg went past its memory limit and was killed") || c.RSS() <= 4<<20 {
		t.Errorf("rss %d, log:\n%s", c.RSS(), logs)
	}

	// Past the limit before its first output: the rung is not blamed, so
	// the next transcode tries it again.
	logs2 := &logBuf{}
	tr2 := NewTranscoder(TranscoderConfig{FFmpeg: ffmpeg, Slots: 1, MaxRSS: 1 << 20, Log: slog.New(slog.NewTextHandler(logs2, nil))}, softwareOnly)
	defer tr2.Close()
	tr2.rssEvery = 5 * time.Millisecond
	var failed *FailedError
	if _, err := tr2.Start(context.Background(), src, h264); !errors.As(err, &failed) || failed.Message != "ffmpeg went past its memory limit" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(logs2.String(), "a transcode failed before its first output") || tr2.Running() != 0 {
		t.Errorf("running %d, log:\n%s", tr2.Running(), logs2)
	}
	tr2.maxRSS.Store(DefaultMaxRSS)
	c, err = tr2.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatalf("with the limit back: %v", err)
	}
	c.Stop()
}

// TestAClientThatGoesFirst: a context that ends before ffmpeg's first
// output stops it, and frees the slot.
func TestAClientThatGoesFirst(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := NewTranscoder(TranscoderConfig{FFmpeg: slow, Slots: 1}, softwareOnly)
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := tr.Start(ctx, Source{Path: "/nowhere.mkv"}, h264); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	if took := time.Since(start); took > 2*time.Second || tr.Running() != 0 {
		t.Errorf("took %v, running %d", took, tr.Running())
	}
}

func TestCloseKillsEveryChild(t *testing.T) {
	src := film(t)
	tr, _ := newTranscoder(t, softwareOnly, 2)
	var pids []int
	var children []*Child
	for i := 0; i < 2; i++ {
		c, err := tr.Start(context.Background(), src, h264)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, c.PID())
		children = append(children, c)
	}
	tr.Close()
	for _, pid := range pids {
		if !gone(pid) {
			t.Errorf("ffmpeg %d outlived Close", pid)
		}
	}
	for _, c := range children {
		c.Stop()
	}
	if _, err := tr.Start(context.Background(), src, h264); !errors.Is(err, ErrClosed) {
		t.Errorf("after Close: %v", err)
	}
}
