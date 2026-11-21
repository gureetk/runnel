// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8610" || c.DataDir != "/var/lib/runnel" || c.Name != "" || c.LogLevel != slog.LevelInfo {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestValues(t *testing.T) {
	c, err := FromEnv(env(map[string]string{
		"RUNNEL_LISTEN": "127.0.0.1:9000",
		"RUNNEL_DATA":   "relative/data",
		"RUNNEL_NAME":   "  Upstairs ",
		"RUNNEL_LOG":    "DEBUG",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9000" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if !filepath.IsAbs(c.DataDir) || !strings.HasSuffix(c.DataDir, "relative/data") {
		t.Errorf("DataDir = %q, want an absolute path ending in relative/data", c.DataDir)
	}
	if c.Name != "Upstairs" {
		t.Errorf("Name = %q", c.Name)
	}
	if c.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}
}

func TestRejects(t *testing.T) {
	for _, m := range []map[string]string{
		{"RUNNEL_LISTEN": "8610"},
		{"RUNNEL_LOG": "loud"},
		{"RUNNEL_DATA": "/srv/what?"},
	} {
		if _, err := FromEnv(env(m)); err == nil {
			t.Errorf("%v: no error", m)
		}
	}
}
