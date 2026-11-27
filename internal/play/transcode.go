// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of docs/05-playback.md, section 4.4.
const (
	DefaultMaxRSS = 1024 << 20
	// rssEvery is how often a child's memory is read.
	rssEvery = 2 * time.Second
	// readSize is how much of ffmpeg's output is read at once.
	readSize = 64 << 10
)

var (
	// ErrNoSlot is a transcode asked for while every slot is in use.
	ErrNoSlot = errors.New("every transcode slot is in use")
	// ErrUnsupported is a target that nothing here encodes.
	ErrUnsupported = errors.New("this server cannot encode that")
	// ErrClosed is a transcode asked for while the server stops.
	ErrClosed = errors.New("the server is stopping")

	errOverRSS = errors.New("ffmpeg went past its memory limit")
)

// FailedError is a transcode that stopped before its first output on
// every rung of the ladder left to try.
type FailedError struct {
	// Message is what ffmpeg said last, or why nothing was tried.
	Message string
}

func (e *FailedError) Error() string { return "ffmpeg stopped before its first output: " + e.Message }

// Capabilities is what the last check found, and the GPU to use.
type Capabilities interface {
	Last() Result
	Hardware() Hardware
}

// Transcoder runs ffmpeg children for transcodes: one slot each, down the
// fallback ladder until one produces output, killed above the memory
// limit (docs/05-playback.md, section 4).
type Transcoder struct {
	ffmpeg string
	caps   Capabilities
	log    *slog.Logger
	maxRSS atomic.Int64
	slots  chan struct{}
	// rssEvery is how often a child's memory is read: rssEvery outside
	// tests.
	rssEvery time.Duration

	failures Failures

	mu       sync.Mutex
	children map[*Child]bool
	closed   bool
}

// TranscoderConfig is what a Transcoder is made with.
type TranscoderConfig struct {
	FFmpeg string
	Slots  int   // at least 1
	MaxRSS int64 // bytes; DefaultMaxRSS when zero
	Log    *slog.Logger
}

// NewTranscoder makes a transcoder that encodes what caps says works.
func NewTranscoder(c TranscoderConfig, caps Capabilities) *Transcoder {
	if c.MaxRSS <= 0 {
		c.MaxRSS = DefaultMaxRSS
	}
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	t := &Transcoder{
		ffmpeg:   c.FFmpeg,
		caps:     caps,
		log:      c.Log,
		slots:    make(chan struct{}, max(c.Slots, 1)),
		rssEvery: rssEvery,
		children: map[*Child]bool{},
	}
	t.maxRSS.Store(c.MaxRSS)
	return t
}

// Slots is how many transcodes may run at once.
func (t *Transcoder) Slots() int { return cap(t.slots) }

// Running is how many transcodes hold a slot now.
func (t *Transcoder) Running() int { return len(t.slots) }

// MaxRSS is the memory limit of one child, in bytes.
func (t *Transcoder) MaxRSS() int64 { return t.maxRSS.Load() }

// Start takes a slot and starts ffmpeg on the first rung of the ladder
// that produces output. A rung whose ffmpeg stops before its first output
// is remembered as failed for this kind of source and target, and the
// next is tried. It answers ErrNoSlot, ErrUnsupported, ErrClosed, a
// *FailedError, or ctx's error when ctx ends first.
func (t *Transcoder) Start(ctx context.Context, src Source, tg Target) (*Child, error) {
	hw, sw := t.caps.Last().Can(tg.V, tg.Depth)
	ladder := Ladder(tg, hw, sw)
	if len(ladder) == 0 || t.ffmpeg == "" {
		return nil, ErrUnsupported
	}
	select {
	case t.slots <- struct{}{}:
	default:
		return nil, ErrNoSlot
	}
	release := func() { <-t.slots }
	rungs := t.failures.Skip(src, tg, ladder)
	if len(rungs) == 0 {
		release()
		return nil, &FailedError{Message: "every way to encode this failed earlier"}
	}
	var last error
	for _, r := range rungs {
		c, err := t.start(ctx, src, tg, r, release)
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil || errors.Is(err, ErrClosed) {
			release()
			return nil, err
		}
		// Memory is not the rung's fault, and the next would hold as much.
		if errors.Is(err, errOverRSS) {
			release()
			return nil, &FailedError{Message: err.Error()}
		}
		t.failures.Failed(src, tg, r)
		t.log.Warn("a transcode failed before its first output; the next is tried", "rung", r.String(),
			"encoder", Encoder(tg, r), "codec", src.Codec, "profile", src.Profile, "depth", src.Depth, "err", err)
		last = err
	}
	release()
	return nil, &FailedError{Message: last.Error()}
}

// start runs ffmpeg for one rung and reads its first output.
func (t *Transcoder) start(ctx context.Context, src Source, tg Target, r Rung, release func()) (*Child, error) {
	// A pipe of our own, not StdoutPipe: Wait closes that one, and a
	// child is waited for while its output is still being read.
	out, in, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(t.ffmpeg, Args(src, tg, t.caps.Hardware(), r)...)
	cmd.Stdout = in
	stderr := &tail{}
	cmd.Stderr = stderr
	c := &Child{
		t: t, Rung: r, Encoder: Encoder(tg, r), Target: tg,
		cmd: cmd, out: out, stderr: stderr, release: release, exited: make(chan struct{}),
		started: time.Now(),
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		out.Close()
		in.Close()
		return nil, ErrClosed
	}
	err = cmd.Start()
	if err == nil {
		t.children[c] = true
	}
	t.mu.Unlock()
	in.Close()
	if err != nil {
		out.Close()
		return nil, err
	}
	go func() {
		c.waitErr = cmd.Wait()
		close(c.exited)
	}()
	go c.watch()

	type read struct {
		n   int
		err error
	}
	buf := make([]byte, readSize)
	first := make(chan read, 1)
	go func() {
		n, err := out.Read(buf)
		first <- read{n, err}
	}()
	select {
	case got := <-first:
		if got.n > 0 {
			c.buf, c.first = buf, buf[:got.n]
			return c, nil
		}
		c.kill()
		<-c.exited
		c.forget()
		if c.overRSS.Load() {
			return nil, errOverRSS
		}
		return nil, errors.New(c.said())
	case <-ctx.Done():
		c.kill()
		<-c.exited
		c.forget()
		return nil, ctx.Err()
	}
}

// Close kills every child, and refuses transcodes from now on. Each
// child's slot is freed when its stream stops it.
func (t *Transcoder) Close() {
	t.mu.Lock()
	t.closed = true
	children := make([]*Child, 0, len(t.children))
	for c := range t.children {
		children = append(children, c)
	}
	t.mu.Unlock()
	for _, c := range children {
		c.kill()
	}
	for _, c := range children {
		<-c.exited
	}
}

// Child is one ffmpeg child of a transcode, with its first output read.
type Child struct {
	t       *Transcoder
	Rung    Rung
	Encoder string
	Target  Target

	cmd     *exec.Cmd
	out     *os.File
	buf     []byte
	first   []byte // ffmpeg's first output, not relayed yet
	stderr  *tail
	release func()
	started time.Time

	exited  chan struct{}
	waitErr error

	killOnce sync.Once
	stopOnce sync.Once
	rss      atomic.Int64
	overRSS  atomic.Bool
}

// PID is ffmpeg's process id.
func (c *Child) PID() int { return c.cmd.Process.Pid }

// RSS is ffmpeg's resident memory when it was last read, in bytes.
func (c *Child) RSS() int64 { return c.rss.Load() }

// Relay writes ffmpeg's output to w as it comes, and calls flush after
// every write. It returns nil when ffmpeg's output ends, or the error of
// the first write that fails: the client went.
func (c *Child) Relay(w io.Writer, flush func()) error {
	if len(c.first) > 0 {
		if _, err := w.Write(c.first); err != nil {
			return err
		}
		c.first = nil
		flush()
	}
	for {
		n, err := c.out.Read(c.buf)
		if n > 0 {
			if _, werr := w.Write(c.buf[:n]); werr != nil {
				return werr
			}
			flush()
		}
		if err != nil {
			return nil
		}
	}
}

// Stop kills ffmpeg with SIGKILL, never SIGTERM, which can hang flushing
// into a pipe nobody reads (rule 12), waits for it and frees its slot.
// What ffmpeg said on its way is logged. Calling Stop again does nothing.
func (c *Child) Stop() {
	c.stopOnce.Do(func() {
		c.kill()
		<-c.exited
		c.forget()
		c.release()
		if c.overRSS.Load() {
			return
		}
		if s := c.stderr.String(); s != "" {
			c.t.log.Warn("ffmpeg said", "encoder", c.Encoder, "err", s)
		}
	})
}

// Ended is closed once ffmpeg has exited.
func (c *Child) Ended() <-chan struct{} { return c.exited }

func (c *Child) kill() {
	c.killOnce.Do(func() {
		_ = c.cmd.Process.Kill()
		_ = c.out.Close()
	})
}

func (c *Child) forget() {
	c.t.mu.Lock()
	delete(c.t.children, c)
	c.t.mu.Unlock()
}

// said is ffmpeg's last line of error, or how it ended.
func (c *Child) said() string {
	s := strings.TrimSpace(c.stderr.String())
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s != "" {
		return s
	}
	if c.waitErr != nil {
		return c.waitErr.Error()
	}
	return "ffmpeg wrote nothing"
}

// watch reads the child's resident memory every rssEvery, and kills it
// above the limit. GPU memory is not seen (docs/05-playback.md, section
// 4.4).
func (c *Child) watch() {
	tick := time.NewTicker(c.t.rssEvery)
	defer tick.Stop()
	for {
		rss, ok := residentOf(c.cmd.Process.Pid)
		if ok {
			c.rss.Store(rss)
			if limit := c.t.maxRSS.Load(); rss > limit {
				c.overRSS.Store(true)
				c.t.log.Warn("ffmpeg went past its memory limit and was killed", "encoder", c.Encoder,
					"rss_mb", rss>>20, "limit_mb", limit>>20)
				c.kill()
				return
			}
		}
		select {
		case <-c.exited:
			return
		case <-tick.C:
		}
	}
}

var pageSize = int64(os.Getpagesize())

// residentOf reads a process's resident memory from /proc/<pid>/statm.
func residentOf(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0, false
	}
	f := bytes.Fields(b)
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(string(f[1]), 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * pageSize, true
}

// tail keeps the last 4 KiB written to it: ffmpeg's errors.
type tail struct {
	mu sync.Mutex
	b  []byte
}

const tailSize = 4 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > tailSize {
		t.b = t.b[len(t.b)-tailSize:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

// String names a child for the log.
func (c *Child) String() string {
	return fmt.Sprintf("ffmpeg %d (%s)", c.PID(), c.Encoder)
}
