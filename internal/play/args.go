// SPDX-License-Identifier: GPL-3.0-or-later

// Package play turns a playback request into ffmpeg children and relays
// what they write: the argument builder and its fallback ladder
// (docs/05-playback.md, section 4).
package play

import (
	"fmt"
	"strconv"
	"sync"
)

// DefaultDevice is the render device that ffmpeg is left to find by itself
// when it decodes on the GPU, as it did where the hardware form was run
// (docs/05-playback.md, section 12).
const DefaultDevice = "/dev/dri/renderD128"

// Source is what the builder needs to know about the file.
type Source struct {
	Path string
	// The video stream's codec and profile as ffprobe names them, and its
	// bit depth: they key the failures remembered.
	Codec   string
	Profile string
	Depth   int
	// Height is the video's, for max_h. Zero is unknown.
	Height int
}

// AllAudio and NoAudio are the values of Target.Audio other than a stream
// index.
const (
	AllAudio = -1
	NoAudio  = -2
)

// Target is a transcode request, its parameters already checked and given
// their defaults (docs/04-api.md, section 7.3). Two requests with equal
// targets make the same stream, which is what a warm-up's ticket matches.
type Target struct {
	V     string // "hevc" or "h264"
	Depth int    // 8 or 10
	MaxH  int    // scale down to this height when the source is taller; 0 keeps it
	// StartMS is where to start. Exact decodes forward to it; otherwise
	// the stream starts at the keyframe at or before it.
	StartMS int64
	Exact   bool
	Audio   int // AllAudio, NoAudio or the index of one audio stream
	Subs    bool
	Fonts   bool
}

// Rung is a step of the fallback ladder (docs/05-playback.md, section 4.3).
type Rung int

const (
	// HardwareBoth decodes and encodes on the GPU.
	HardwareBoth Rung = iota + 1
	// HardwareEncode decodes in software and encodes on the GPU.
	HardwareEncode
	// SoftwareH264 decodes in software and encodes H.264 with libx264.
	SoftwareH264
)

func (r Rung) String() string {
	switch r {
	case HardwareBoth:
		return "hardware decode and encode"
	case HardwareEncode:
		return "software decode, hardware encode"
	case SoftwareH264:
		return "software"
	}
	return "rung " + strconv.Itoa(int(r))
}

// Hardware is the GPU the server encodes on.
type Hardware struct {
	// Device is the VAAPI render device. Empty encodes nothing on a GPU.
	Device string
}

// Ladder is the rungs to try for a target, in order: the hardware ones
// when the GPU can encode the target, then software H.264 when that is
// what was asked for and libx264 works. A client that asks for HEVC gets
// no H.264 in its place: it asks again for what it can take.
func Ladder(t Target, hardware, software bool) []Rung {
	var rungs []Rung
	if hardware {
		rungs = append(rungs, HardwareBoth, HardwareEncode)
	}
	if software && t.V == "h264" && t.Depth == 8 {
		rungs = append(rungs, SoftwareH264)
	}
	return rungs
}

// Encoder names the encoder a rung uses for a target.
func Encoder(t Target, r Rung) string {
	switch {
	case r == SoftwareH264:
		return "libx264"
	case t.V == "hevc":
		return "hevc_vaapi"
	default:
		return "h264_vaapi"
	}
}

// Args is ffmpeg's arguments for one rung, without the program's name, in
// the order of docs/05-playback.md, section 4.1. It is an array: nothing
// here is ever handed to a shell.
func Args(src Source, t Target, hw Hardware, r Rung) []string {
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch r {
	case HardwareBoth:
		a = append(a, "-hwaccel", "vaapi")
		if hw.Device != DefaultDevice {
			a = append(a, "-hwaccel_device", hw.Device)
		}
		a = append(a, "-hwaccel_output_format", "vaapi", "-noautorotate")
	case HardwareEncode:
		a = append(a, "-vaapi_device", hw.Device)
	}
	if t.StartMS > 0 {
		a = append(a, "-ss", seconds(t.StartMS))
		if !t.Exact {
			a = append(a, "-noaccurate_seek")
		}
	}
	a = append(a, "-i", src.Path, "-map", "0:v:0")
	switch t.Audio {
	case AllAudio:
		// A file may have no audio: without the question mark ffmpeg
		// stops at once.
		a = append(a, "-map", "0:a?")
	case NoAudio:
	default:
		a = append(a, "-map", "0:"+strconv.Itoa(t.Audio))
	}
	if t.Subs {
		a = append(a, "-map", "0:s?")
	}
	if t.Fonts {
		a = append(a, "-map", "0:t?")
	}
	a = append(a, "-c", "copy")
	a = append(a, video(src, t, r)...)
	return append(a, "-copyts", "-max_interleave_delta", "500000",
		"-f", "matroska", "-flush_packets", "1", "pipe:1")
}

// video is the encoder's part of the arguments (docs/05-playback.md,
// section 4.2).
func video(src Source, t Target, r Rung) []string {
	scale := 0
	if t.MaxH > 0 && src.Height > t.MaxH {
		scale = t.MaxH
	}
	if r == SoftwareH264 {
		var a []string
		if scale > 0 {
			a = append(a, "-vf", fmt.Sprintf("scale=-2:%d", scale))
		}
		return append(a, "-c:v", "libx264", "-preset", "ultrafast", "-crf", "23", "-pix_fmt", "yuv420p")
	}
	format := "nv12"
	if t.V == "hevc" && t.Depth == 10 {
		format = "p010"
	}
	var filter string
	switch {
	case r == HardwareBoth && scale > 0:
		filter = fmt.Sprintf("scale_vaapi=w=-2:h=%d:format=%s", scale, format)
	case r == HardwareBoth:
		filter = "scale_vaapi=format=" + format
	case scale > 0:
		filter = fmt.Sprintf("scale=-2:%d,format=%s,hwupload", scale, format)
	default:
		filter = "format=" + format + ",hwupload"
	}
	a := []string{"-vf", filter}
	switch {
	case t.V == "hevc" && t.Depth == 10:
		a = append(a, "-c:v", "hevc_vaapi", "-profile:v", "main10", "-rc_mode", "CQP", "-qp", "24")
	case t.V == "hevc":
		a = append(a, "-c:v", "hevc_vaapi", "-profile:v", "main", "-rc_mode", "CQP", "-qp", "24")
	default:
		a = append(a, "-c:v", "h264_vaapi", "-profile:v", "high", "-rc_mode", "CQP", "-qp", "23")
	}
	return append(a, "-bf", "0")
}

// seconds writes milliseconds as ffmpeg's -ss reads them.
func seconds(ms int64) string {
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

// Failures remembers, for the process's life, the rungs that failed for a
// kind of source and a target, so that the next request starts on the
// rung that works (docs/05-playback.md, section 4.3).
type Failures struct {
	mu     sync.Mutex
	failed map[failKey]map[Rung]bool
}

type failKey struct {
	codec, profile string
	depth          int
	v              string
	vdepth         int
}

func keyOf(src Source, t Target) failKey {
	return failKey{src.Codec, src.Profile, src.Depth, t.V, t.Depth}
}

// Skip drops from rungs those that failed before for this kind of source
// and this target.
func (f *Failures) Skip(src Source, t Target, rungs []Rung) []Rung {
	f.mu.Lock()
	defer f.mu.Unlock()
	failed := f.failed[keyOf(src, t)]
	var left []Rung
	for _, r := range rungs {
		if !failed[r] {
			left = append(left, r)
		}
	}
	return left
}

// Failed remembers that a rung failed for this kind of source and target.
func (f *Failures) Failed(src Source, t Target, r Rung) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed == nil {
		f.failed = map[failKey]map[Rung]bool{}
	}
	k := keyOf(src, t)
	if f.failed[k] == nil {
		f.failed[k] = map[Rung]bool{}
	}
	f.failed[k][r] = true
}
