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
	if c.Listen != ":8610" || c.DataDir != "/var/lib/runnel" || c.Name != "" || c.LogLevel != slog.LevelInfo ||
		c.HWAccel != "auto" || c.VAAPIDevice != "/dev/dri/renderD128" || c.TranscodeSlots != 1 {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestValues(t *testing.T) {
	c, err := FromEnv(env(map[string]string{
		"RUNNEL_LISTEN":          "127.0.0.1:9000",
		"RUNNEL_DATA":            "relative/data",
		"RUNNEL_NAME":            "  Upstairs ",
		"RUNNEL_LOG":             "DEBUG",
		"RUNNEL_HWACCEL":         "None",
		"RUNNEL_VAAPI_DEVICE":    "/dev/dri/renderD129/",
		"RUNNEL_TRANSCODE_SLOTS": "2",
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
	if c.HWAccel != "none" || c.VAAPIDevice != "/dev/dri/renderD129" || c.TranscodeSlots != 2 {
		t.Errorf("HWAccel = %q, VAAPIDevice = %q", c.HWAccel, c.VAAPIDevice)
	}
}

func TestRejects(t *testing.T) {
	for _, m := range []map[string]string{
		{"RUNNEL_LISTEN": "8610"},
		{"RUNNEL_LOG": "loud"},
		{"RUNNEL_DATA": "/srv/what?"},
		{"RUNNEL_HWACCEL": "qsv"},
		{"RUNNEL_VAAPI_DEVICE": "renderD128"},
		{"RUNNEL_TRANSCODE_SLOTS": "0"},
		{"RUNNEL_TRANSCODE_SLOTS": "two"},
	} {
		if _, err := FromEnv(env(m)); err == nil {
			t.Errorf("%v: no error", m)
		}
	}
}
