// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/probe"
	"github.com/gureetk/runnel/internal/tasks"
)

// ProbeWorkers is how many files are probed at once: as many as the
// prober has children (docs/02-architecture.md, section 5).
const ProbeWorkers = 2

// noFFprobeWait is how long probing waits after ffprobe could not be run,
// before it tries again.
const noFFprobeWait = time.Minute

// Probes is the background queue's probe work: one task probes one file
// and stores what ffprobe read, with the file's fingerprint
// (docs/06-library-and-metadata.md, sections 3 and 9). It logs each pass
// over the queued probes: its start, its progress every 30 seconds, and
// its end.
type Probes struct {
	db     *db.DB
	prober *probe.Prober
	log    *slog.Logger
	now    func() time.Time

	mu         sync.Mutex
	start      time.Time
	started    bool // the pass's first line is logged
	lastLog    time.Time
	probed     atomic.Int64
	unreadable atomic.Int64
	noFFprobe  atomic.Bool // logged; cleared by the next probe that runs
}

// NewProbes makes the probe work, run by prober.
func NewProbes(d *db.DB, prober *probe.Prober, log *slog.Logger) *Probes {
	return &Probes{db: d, prober: prober, log: log, now: time.Now}
}

// Kind is the queue's kind for probes.
func (p *Probes) Kind() tasks.Kind {
	return tasks.Kind{Name: "probe", Limit: ProbeWorkers, Run: p.run, Begin: p.begin, End: p.end}
}

func (p *Probes) begin() {
	p.mu.Lock()
	p.start, p.started = p.now(), false
	p.mu.Unlock()
	p.probed.Store(0)
	p.unreadable.Store(0)
}

// logStart logs a pass's first line once it has read a file, so that a
// pass that only waited for ffprobe logs nothing.
func (p *Probes) logStart(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.started {
		p.started, p.lastLog = true, now
		// This run's task still counts as running.
		left, _ := p.db.CountTasks(ctx, "probe", "queued", "running")
		p.log.Info("probing", "files", left)
		return
	}
	if now.Sub(p.lastLog) >= 30*time.Second {
		p.lastLog = now
		left, _ := p.db.CountTasks(ctx, "probe", "queued", "running")
		p.log.Info("probing", "done", p.probed.Load()+p.unreadable.Load(), "left", left)
	}
}

func (p *Probes) end() {
	p.mu.Lock()
	started, start := p.started, p.start
	p.mu.Unlock()
	if !started {
		return
	}
	p.log.Info("probing done", "probed", p.probed.Load(), "unreadable", p.unreadable.Load(),
		"ms", p.now().Sub(start).Milliseconds())
	// A pass allocates as it goes; give what it freed back before idling.
	debug.FreeOSMemory()
}

// run probes the file a task names. A file that went, changed or was
// probed since the task was queued leaves nothing to do: the next scan
// sees a change and queues it again.
func (p *Probes) run(ctx context.Context, t db.Task) error {
	target, err := p.db.ProbeTargetOf(ctx, db.FileOfKey(t.Key), probe.Version)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	fi, err := os.Stat(target.Path)
	if err != nil || fi.Size() != target.Size || fi.ModTime().UnixNano() != target.MtimeNS {
		return nil
	}
	res, err := p.prober.Probe(ctx, target.Path)
	var u *probe.UnreadableError
	switch {
	case errors.Is(err, probe.ErrNoFFprobe):
		if !p.noFFprobe.Swap(true) {
			p.log.Error("ffprobe cannot be run; set RUNNEL_FFPROBE to its path. Probing waits and tries again.", "err", err)
		}
		return &tasks.Pause{Err: err, For: noFFprobeWait}
	case errors.As(err, &u):
		p.noFFprobe.Store(false)
		fp, _ := Fingerprint(target.Path)
		if _, err := p.db.SaveProbeError(ctx, target, u.Msg, fp, probe.Version, p.now().UnixMilli()); err != nil {
			return err
		}
		p.logStart(ctx)
		p.unreadable.Add(1)
		p.log.Warn("probe: ffprobe cannot read the file", "file", target.FileID, "path", target.Path, "reason", u.Msg)
		return nil
	case err != nil:
		// Stopped by its timeout, or by the server stopping: the queue
		// tries it again later, or at the next start.
		return fmt.Errorf("probe %s: %w", target.Path, err)
	}
	p.noFFprobe.Store(false)
	fp, err := Fingerprint(target.Path)
	if err != nil {
		fp = ""
	}
	if _, err := p.db.SaveProbe(ctx, target, res, fp, probe.Version, p.now().UnixMilli()); err != nil {
		return err
	}
	p.logStart(ctx)
	p.probed.Add(1)
	return nil
}
