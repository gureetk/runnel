// SPDX-License-Identifier: GPL-3.0-or-later

// Package testmedia makes small media files for tests, from ffmpeg's test
// sources. Nothing outside tests uses it.
//
// Tests that need ffmpeg skip when it is not on PATH, unless
// RUNNEL_TEST_REQUIRE_FFMPEG is set, as it is in CI: then they fail, so
// that they cannot be skipped there by accident.
package testmedia

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Tools returns the paths of ffmpeg and ffprobe.
func Tools(t testing.TB) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		if os.Getenv("RUNNEL_TEST_REQUIRE_FFMPEG") != "" {
			t.Fatal("ffmpeg and ffprobe are needed on PATH")
		}
		t.Skip("ffmpeg and ffprobe are not on PATH")
	}
	return ffmpeg, ffprobe
}

// Make runs ffmpeg with args and then writes out. Inputs come from args:
// lavfi test sources, or files the test wrote.
func Make(t testing.TB, out string, args ...string) {
	t.Helper()
	ffmpeg, _ := Tools(t)
	full := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)
	full = append(full, out)
	if b, err := exec.Command(ffmpeg, full...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", strings.Join(full, " "), err, b)
	}
}

// HasEncoder reports whether this ffmpeg has the encoder name.
func HasEncoder(t testing.TB, name string) bool {
	t.Helper()
	ffmpeg, _ := Tools(t)
	b, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil {
		t.Fatalf("ffmpeg -encoders: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// Video is the lavfi input for a small test picture of seconds length.
func Video(seconds string) []string {
	return []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=" + seconds}
}

// Tone is the lavfi input for a sine tone of seconds length.
func Tone(freq, seconds string) []string {
	return []string{"-f", "lavfi", "-i", "sine=frequency=" + freq + ":duration=" + seconds}
}

// Join puts argument lists together.
func Join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
