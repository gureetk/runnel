// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// encodeTimeout bounds each one-second encode of the check. Four of them
// stay inside a JSON route's deadline.
const encodeTimeout = 3 * time.Second

// Row is one target of the check: whether it encodes here, and how
// (docs/04-api.md, section 8.7).
type Row struct {
	V       string
	Depth   int
	How     string // "hardware" or "software"
	Encoder string
	Works   bool
	// Error says why a target does not work: the reason found before any
	// encode, or ffmpeg's first line of error.
	Error string
}

// Result is what the check found, and when.
type Result struct {
	CheckedAt time.Time
	Rows      []Row
}

// HW is "vaapi" when the GPU encodes anything, and "none" otherwise.
func (r Result) HW() string {
	for _, row := range r.Rows {
		if row.How == "hardware" && row.Works {
			return "vaapi"
		}
	}
	return "none"
}

// Can reports whether a target encodes here on the GPU, and in software.
func (r Result) Can(v string, depth int) (hardware, software bool) {
	for _, row := range r.Rows {
		if row.V == v && row.Depth == depth && row.Works {
			if row.How == "hardware" {
				hardware = true
			} else {
				software = true
			}
		}
	}
	return hardware, software
}

// Video is the codecs that encode here, in any way, HEVC first; Depth10
// those that encode at 10 bits. GET /server lists them (docs/04-api.md,
// section 3.1).
func (r Result) Video() (video, depth10 []string) {
	video, depth10 = []string{}, []string{}
	for _, v := range []string{"hevc", "h264"} {
		h8, s8 := r.Can(v, 8)
		h10, s10 := r.Can(v, 10)
		if h8 || s8 || h10 || s10 {
			video = append(video, v)
		}
		if h10 || s10 {
			depth10 = append(depth10, v)
		}
	}
	return video, depth10
}

// HWAccel values, as RUNNEL_HWACCEL takes them (D50).
const (
	HWAuto  = "auto"
	HWVAAPI = "vaapi"
	HWNone  = "none"
)

// Check finds out what the server can encode (docs/05-playback.md,
// section 8): which encoders the configured ffmpeg lists, then a
// one-second encode of a test pattern for each target. Hardware decoding
// is not checked: the ladder finds it per file.
type Check struct {
	FFmpeg  string // empty: there is no ffmpeg
	HWAccel string // HWAuto, HWVAAPI or HWNone
	Device  string // the VAAPI render device
	// Now is the clock: time.Now when nil.
	Now func() time.Time
	// Timeout bounds each encode: encodeTimeout when zero.
	Timeout time.Duration
}

func (c Check) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return encodeTimeout
}

type target struct {
	v       string
	depth   int
	how     string
	encoder string
	args    []string // the encode's arguments after the test pattern
}

// targets are the rows of the check, in the order the API lists them.
// Each encodes with the arguments a transcode uses (section 4.2 there).
func (c Check) targets() []target {
	hw := func(format string, enc ...string) []string {
		return append([]string{"-vf", "format=" + format + ",hwupload"}, enc...)
	}
	return []target{
		{"hevc", 10, "hardware", "hevc_vaapi", hw("p010", "-c:v", "hevc_vaapi", "-profile:v", "main10", "-rc_mode", "CQP", "-qp", "24", "-bf", "0")},
		{"hevc", 8, "hardware", "hevc_vaapi", hw("nv12", "-c:v", "hevc_vaapi", "-profile:v", "main", "-rc_mode", "CQP", "-qp", "24", "-bf", "0")},
		{"h264", 8, "hardware", "h264_vaapi", hw("nv12", "-c:v", "h264_vaapi", "-profile:v", "high", "-rc_mode", "CQP", "-qp", "23", "-bf", "0")},
		{"h264", 8, "software", "libx264", []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "23", "-pix_fmt", "yuv420p"}},
	}
}

// Run checks every target. It never fails: what does not work says why.
func (c Check) Run(ctx context.Context) Result {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	res := Result{}
	listed, listErr := c.encoders(ctx)
	hwReason := ""
	switch {
	case c.HWAccel == HWNone:
		hwReason = "RUNNEL_HWACCEL is none"
	case c.Device == "":
		hwReason = "no render device is set"
	default:
		if fi, err := os.Stat(c.Device); err != nil {
			hwReason = "no render device at " + c.Device
		} else if fi.Mode()&os.ModeCharDevice == 0 {
			hwReason = c.Device + " is not a device"
		}
	}
	for _, t := range c.targets() {
		row := Row{V: t.v, Depth: t.depth, How: t.how, Encoder: t.encoder}
		switch {
		case c.FFmpeg == "":
			row.Error = "ffmpeg is not on PATH"
		case listErr != nil:
			row.Error = listErr.Error()
		case t.how == "hardware" && hwReason != "":
			row.Error = hwReason
		case !listed[t.encoder]:
			row.Error = "this ffmpeg has no " + t.encoder
		default:
			if err := c.encode(ctx, t); err != nil {
				row.Error = err.Error()
			} else {
				row.Works = true
			}
		}
		res.Rows = append(res.Rows, row)
	}
	res.CheckedAt = now()
	return res
}

// encoders is the set of encoder names this ffmpeg lists.
func (c Check) encoders(ctx context.Context) (map[string]bool, error) {
	if c.FFmpeg == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, c.FFmpeg, "-hide_banner", "-encoders")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg -encoders: %v", err)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && len(f[0]) == 6 {
			listed[f[1]] = true
		}
	}
	return listed, nil
}

// encode makes one second of a 720p test pattern with a target's encoder,
// as docs/05-playback.md, section 8, gives it.
func (c Check) encode(ctx context.Context, t target) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if t.how == "hardware" {
		args = append(args, "-vaapi_device", c.Device)
	}
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24", "-t", "1")
	args = append(append(args, t.args...), "-f", "null", "-")
	cmd := exec.CommandContext(ctx, c.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Killed, ffmpeg has nothing left that holds its pipe; this bounds the
	// wait if something else does.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return errors.New("the test encode took longer than " + c.timeout().String())
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); line != "" {
		return errors.New(line)
	}
	return err
}

// ErrBusy is a check asked for while a transcode runs.
var ErrBusy = errors.New("a transcode is running")

// Encoders keeps the last check's result for the process's life.
type Encoders struct {
	check Check
	// Busy reports whether a transcode runs, which a check on request
	// waits for. Nil is never busy.
	Busy func() bool

	running sync.Mutex // one check at a time
	mu      sync.Mutex
	last    Result
}

// NewEncoders keeps the results of c. Nothing is known until the first
// Check.
func NewEncoders(c Check) *Encoders {
	return &Encoders{check: c}
}

// Check runs the check and keeps its result. It is refused with ErrBusy
// while a transcode runs: the encodes would compete with it for the GPU.
func (e *Encoders) Check(ctx context.Context) (Result, error) {
	if e.Busy != nil && e.Busy() {
		return Result{}, ErrBusy
	}
	e.running.Lock()
	defer e.running.Unlock()
	res := e.check.Run(ctx)
	e.mu.Lock()
	e.last = res
	e.mu.Unlock()
	return res, nil
}

// Last is the last check's result.
func (e *Encoders) Last() Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// Hardware is the GPU transcodes use.
func (e *Encoders) Hardware() Hardware {
	return Hardware{Device: e.check.Device}
}

// RowName names a row as the log prints it: "hevc10", "hevc8", "h264",
// and "h264_software".
func RowName(r Row) string {
	name := r.V
	if r.V == "hevc" {
		name += strconv.Itoa(r.Depth)
	}
	if r.How == "software" {
		name += "_software"
	}
	return name
}
