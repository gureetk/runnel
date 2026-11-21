// SPDX-License-Identifier: GPL-3.0-or-later

// Package probe runs ffprobe on a file and maps what it prints to what the
// database keeps (docs/06-library-and-metadata.md, section 3).
package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/model"
)

// Version is the probe_version written with every result. Raising it
// probes every file again once, after a change to the mapping below.
const Version = 1

// Timeout is the longest one probe may take. A disk that has to spin up
// takes seconds; a probe that takes longer than this is stopped, and the
// queue tries it again later, as it does a failed task.
const Timeout = time.Minute

// ErrNoFFprobe is returned when ffprobe cannot be run at all. Nothing can
// be probed until that is fixed, so the caller waits before it tries again.
var ErrNoFFprobe = errors.New("probe: ffprobe cannot be run")

// UnreadableError says that ffprobe ran and could not read the file. The
// file keeps the message as its probe_error.
type UnreadableError struct{ Msg string }

func (e *UnreadableError) Error() string { return "probe: " + e.Msg }

// Prober runs ffprobe children, at most a few at once.
type Prober struct {
	ffprobe string
	slots   chan struct{}

	depthsOnce sync.Once
	depths     map[string]int // pixel format -> bit depth of its first component
}

// New makes a prober that runs the ffprobe at path, at most children at a
// time (docs/02-architecture.md, section 5: two).
func New(path string, children int) *Prober {
	return &Prober{ffprobe: path, slots: make(chan struct{}, max(1, children))}
}

// Probe reads one file. It returns an *UnreadableError when ffprobe could
// not read it, ErrNoFFprobe when ffprobe could not be run, and the
// context's error when it was stopped. ffprobe is killed when ctx ends.
func (p *Prober) Probe(ctx context.Context, path string) (model.Probe, error) {
	p.depthsOnce.Do(func() { p.depths = p.loadDepths(ctx) })

	out, stderr, err := p.run(ctx, Timeout, "-v", "error", "-show_format", "-show_streams", "-show_chapters", "-of", "json", path)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) {
			return model.Probe{}, &UnreadableError{Msg: firstLine(stderr, path)}
		}
		return model.Probe{}, err
	}
	var raw output
	if err := json.Unmarshal(out, &raw); err != nil {
		return model.Probe{}, &UnreadableError{Msg: "ffprobe printed what is not JSON: " + err.Error()}
	}
	if raw.Format == nil || len(raw.Streams) == 0 {
		return model.Probe{}, &UnreadableError{Msg: "ffprobe found no streams"}
	}
	return mapOutput(raw, path, p.depths), nil
}

// run runs ffprobe with args under one of the slots, and returns what it
// printed on standard output and standard error.
func (p *Prober) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, []byte, error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	defer func() { <-p.slots }()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.ffprobe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), stderr.Bytes(), nil
	case ctx.Err() != nil:
		// Killed: by the caller, or by the timeout.
		return nil, nil, fmt.Errorf("probe: stopped: %w", ctx.Err())
	case errors.As(err, &exit):
		return nil, stderr.Bytes(), err
	default:
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrNoFFprobe, p.ffprobe, err)
	}
}

// firstLine is ffprobe's first line of complaint, without the file's path,
// which the database already has.
func firstLine(stderr []byte, path string) string {
	msg := strings.TrimSpace(string(stderr))
	if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
		// ffprobe prints the decoder's complaint, then "<path>: <reason>".
		msg = strings.TrimSpace(msg[i+1:])
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, path+":"))
	if msg == "" {
		msg = "ffprobe could not read the file"
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

// loadDepths asks ffprobe for the bit depth of every pixel format it
// knows, from the BIT_DEPTHS column of -pix_fmts. Without that table the
// depth is read from the format's name.
func (p *Prober) loadDepths(ctx context.Context) map[string]int {
	out, _, err := p.run(ctx, 10*time.Second, "-hide_banner", "-pix_fmts")
	if err != nil {
		return nil
	}
	depths := map[string]int{}
	col := -1
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if col < 0 {
			for i, name := range f {
				if name == "BIT_DEPTHS" {
					col = i
				}
			}
			continue
		}
		if len(f) <= col || len(f) < 2 {
			continue
		}
		first, _, _ := strings.Cut(f[col], "-")
		if d, err := strconv.Atoi(first); err == nil {
			depths[f[1]] = d
		}
	}
	return depths
}

var depthInName = regexp.MustCompile(`(\d+)(le|be)$`)

// bitDepth is the bit depth of a pixel format: from ffprobe's table when
// there is one, otherwise from a name such as yuv420p10le, otherwise 8.
func bitDepth(pixFmt string, depths map[string]int) int {
	if pixFmt == "" {
		return 0
	}
	if d, ok := depths[pixFmt]; ok {
		return d
	}
	if m := depthInName.FindStringSubmatch(pixFmt); m != nil {
		if d, err := strconv.Atoi(m[1]); err == nil && d <= 16 {
			return d
		}
	}
	return 8
}
