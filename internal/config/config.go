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
	// LogLevel is the lowest level that is logged.
	LogLevel slog.Level
}

// Defaults, as docs/02-architecture.md, section 6, gives them.
const (
	DefaultListen  = ":8610"
	DefaultDataDir = "/var/lib/runnel"
)

// FromEnv reads the configuration through getenv, which is os.Getenv
// outside tests.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:   DefaultListen,
		DataDir:  DefaultDataDir,
		LogLevel: slog.LevelInfo,
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
