// SPDX-License-Identifier: GPL-3.0-or-later

// Package config reads the server's settings from its environment.
//
// There is no configuration file (docs/08-decisions.md, D17). Everything
// that changes while the server runs lives in the database. The variables
// are listed in docs/02-architecture.md, section 6; this package reads the
// ones the server uses so far.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is what the server needs in order to start.
type Config struct {
	// Listen is the address to listen on, as net.Listen takes it.
	Listen string
	// DataDir holds the database and everything the server rebuilds.
	// Always an absolute path.
	DataDir string
	// Name is the server's name as clients show it. Empty means the
	// machine's host name.
	Name string
	// FFprobe and FFmpeg are the paths of ffprobe and ffmpeg. Empty means
	// the ones found on PATH.
	FFprobe string
	FFmpeg  string
	// HWAccel is how the server encodes on a GPU: "auto", "vaapi" or
	// "none" (D50).
	HWAccel string
	// VAAPIDevice is the render device. Always an absolute path.
	VAAPIDevice string
	// TranscodeSlots is how many transcodes run at once: 1 to 16.
	TranscodeSlots int
	// LogLevel is the lowest level that is logged.
	LogLevel slog.Level
}

// Defaults, as docs/02-architecture.md, section 6, gives them.
const (
	DefaultListen      = ":8610"
	DefaultDataDir     = "/var/lib/runnel"
	DefaultHWAccel     = "auto"
	DefaultVAAPIDevice = "/dev/dri/renderD128"
)

// FromEnv reads the configuration through getenv, which is os.Getenv
// outside tests.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:         DefaultListen,
		DataDir:        DefaultDataDir,
		HWAccel:        DefaultHWAccel,
		VAAPIDevice:    DefaultVAAPIDevice,
		TranscodeSlots: 1,
		LogLevel:       slog.LevelInfo,
	}

	if v := getenv("RUNNEL_LISTEN"); v != "" {
		if _, _, err := net.SplitHostPort(v); err != nil {
			return Config{}, fmt.Errorf("RUNNEL_LISTEN=%q: want host:port or :port: %w", v, err)
		}
		c.Listen = v
	}

	if v := getenv("RUNNEL_DATA"); v != "" {
		c.DataDir = v
	}
	// The SQLite driver reads everything after a '?' in its file name as
	// options, so a data directory cannot contain one.
	if strings.ContainsRune(c.DataDir, '?') {
		return Config{}, fmt.Errorf("RUNNEL_DATA=%q: the path must not contain '?'", c.DataDir)
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("RUNNEL_DATA=%q: %w", c.DataDir, err)
	}
	c.DataDir = abs

	c.Name = strings.TrimSpace(getenv("RUNNEL_NAME"))
	c.FFprobe = getenv("RUNNEL_FFPROBE")
	c.FFmpeg = getenv("RUNNEL_FFMPEG")

	if v := getenv("RUNNEL_HWACCEL"); v != "" {
		switch v = strings.ToLower(v); v {
		case "auto", "vaapi", "none":
			c.HWAccel = v
		default:
			return Config{}, fmt.Errorf("RUNNEL_HWACCEL=%q: want auto, vaapi or none", v)
		}
	}
	if v := getenv("RUNNEL_VAAPI_DEVICE"); v != "" {
		if !filepath.IsAbs(v) {
			return Config{}, fmt.Errorf("RUNNEL_VAAPI_DEVICE=%q: want an absolute path, such as %s", v, DefaultVAAPIDevice)
		}
		c.VAAPIDevice = filepath.Clean(v)
	}
	if v := getenv("RUNNEL_TRANSCODE_SLOTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 16 {
			return Config{}, fmt.Errorf("RUNNEL_TRANSCODE_SLOTS=%q: want a number from 1 to 16", v)
		}
		c.TranscodeSlots = n
	}

	if v := getenv("RUNNEL_LOG"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			c.LogLevel = slog.LevelDebug
		case "info":
			c.LogLevel = slog.LevelInfo
		case "warn":
			c.LogLevel = slog.LevelWarn
		case "error":
			c.LogLevel = slog.LevelError
		default:
			return Config{}, fmt.Errorf("RUNNEL_LOG=%q: want debug, info, warn or error", v)
		}
	}

	return c, nil
}
