// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gureetk/runnel/internal/config"
	"github.com/gureetk/runnel/internal/play"
)

// doctorCmd is runnel doctor: ffmpeg, ffprobe, the render device and what
// can be encoded, checked here as the server checks them at start
// (docs/05-playback.md, section 8), with the same settings. It needs no
// running server, and asks none.
func doctorCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "Usage: runnel doctor")
		return 2
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ok := true
	tool := func(name, set string) string {
		path := set
		if path == "" {
			path, _ = exec.LookPath(name)
		}
		if path == "" {
			fmt.Fprintf(stdout, "%s: not found: put it on PATH, or set RUNNEL_%s to its path\n", name, strings.ToUpper(name))
			ok = false
			return ""
		}
		out, err := exec.CommandContext(ctx, path, "-hide_banner", "-version").Output()
		if err != nil {
			fmt.Fprintf(stdout, "%s: %s does not run: %v\n", name, path, err)
			ok = false
			return ""
		}
		first, _, _ := strings.Cut(string(out), "\n")
		first, _, _ = strings.Cut(first, " Copyright")
		fmt.Fprintf(stdout, "%s: %s, %s\n", name, path, strings.TrimSpace(first))
		return path
	}
	ffmpeg := tool("ffmpeg", cfg.FFmpeg)
	tool("ffprobe", cfg.FFprobe)

	fmt.Fprintf(stdout, "RUNNEL_HWACCEL: %s\n", cfg.HWAccel)
	if cfg.HWAccel != play.HWNone {
		dev := cfg.VAAPIDevice
		switch f, err := os.OpenFile(dev, os.O_RDWR, 0); {
		case err == nil:
			f.Close()
			fmt.Fprintf(stdout, "Render device: %s, which this user can open\n", dev)
		case os.IsNotExist(err):
			fmt.Fprintf(stdout, "Render device: %s is not there", dev)
			if _, err := os.Stat("/.dockerenv"); err == nil {
				fmt.Fprint(stdout, ": pass it into the container")
			}
			fmt.Fprintln(stdout)
		case os.IsPermission(err):
			fmt.Fprintf(stdout, "Render device: %s, which this user cannot open: add the user to the group that owns it\n", dev)
		default:
			fmt.Fprintf(stdout, "Render device: %s: %v\n", dev, err)
		}
	}

	res := encoderCheck(cfg, ffmpeg).Run(ctx)
	fmt.Fprintln(stdout, "What it can encode:")
	for _, r := range res.Rows {
		state := "yes"
		if !r.Works {
			state = "no: " + r.Error
		}
		fmt.Fprintf(stdout, "  %-5s %2d-bit  %-8s  %-10s  %s\n", r.V, r.Depth, r.How, r.Encoder, state)
	}
	if video, _ := res.Video(); len(video) == 0 {
		fmt.Fprintln(stdout, "This server can transcode nothing.")
		ok = false
	}
	if !ok {
		return 1
	}
	return 0
}
