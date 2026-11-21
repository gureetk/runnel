// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"errors"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/probe"
)

// probeWorkers is how many files are probed at once: as many as the
// prober has children (docs/02-architecture.md, section 5).
const probeWorkers = 2

// probeLoop probes what is waiting at start, and again each time a scan
// has written something, until ctx ends.
func (s *Scanner) probeLoop(ctx context.Context) {
	for {
		if err := s.ProbePending(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("probing stopped", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.probes:
		}
	}
}

// ProbePending probes every file whose probe is missing or older than
// probe.Version, a few at a time, and stores each result. Until stage 2's
// queue, the files table is the queue: a file is waiting while its
// probe_version is behind (docs/06-library-and-metadata.md, section 9).
func (s *Scanner) ProbePending(ctx context.Context) error {
	if s.prober == nil {
		return nil
	}
	total, err := s.db.CountToProbe(ctx, probe.Version)
	if err != nil || total == 0 {
		return err
	}
	s.log.Info("probing", "files", total)
	start := time.Now()

	var probed, failed atomic.Int64
	var noFFprobe atomic.Bool
	jobs := make(chan db.ProbeTarget)
	var wg sync.WaitGroup
	for i := 0; i < probeWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				switch err := s.probeOne(ctx, t); {
				case err == nil:
					probed.Add(1)
				case errors.Is(err, probe.ErrNoFFprobe):
					if !noFFprobe.Swap(true) {
						s.log.Error("ffprobe cannot be run; set RUNNEL_FFPROBE to its path", "err", err)
					}
				case errors.Is(err, errUnreadable):
					failed.Add(1)
				}
			}
		}()
	}

	lastLog := time.Now()
	var after int64
feed:
	for {
		batch, err := s.db.FilesToProbe(ctx, probe.Version, after, 100)
		if err != nil || len(batch) == 0 {
			break
		}
		for _, t := range batch {
			if noFFprobe.Load() {
				break feed
			}
			if time.Since(lastLog) >= 30*time.Second {
				left, _ := s.db.CountToProbe(ctx, probe.Version)
				s.log.Info("probing", "done", probed.Load()+failed.Load(), "left", left)
				lastLog = time.Now()
			}
			select {
			case jobs <- t:
				after = t.FileID
			case <-ctx.Done():
				break feed
			}
		}
	}
	close(jobs)
	wg.Wait()
	s.log.Info("probing done", "probed", probed.Load(), "unreadable", failed.Load(),
		"ms", time.Since(start).Milliseconds())
	// A pass allocates as it goes; give what it freed back before idling.
	debug.FreeOSMemory()
	return ctx.Err()
}

var errUnreadable = errors.New("unreadable")

// probeOne probes one file and stores the result, its fingerprint with
// it. A file that changed or went since it was listed is left for the next
// scan, which sees the change.
func (s *Scanner) probeOne(ctx context.Context, t db.ProbeTarget) error {
	fi, err := os.Stat(t.Path)
	if err != nil || fi.Size() != t.Size || fi.ModTime().UnixNano() != t.MtimeNS {
		return nil
	}
	res, err := s.prober.Probe(ctx, t.Path)
	var u *probe.UnreadableError
	switch {
	case errors.As(err, &u):
		fp, _ := Fingerprint(t.Path)
		if _, err := s.db.SaveProbeError(ctx, t, u.Msg, fp, probe.Version, s.now().UnixMilli()); err != nil {
			return err
		}
		s.log.Warn("probe: ffprobe cannot read the file", "file", t.FileID, "path", t.Path, "reason", u.Msg)
		return errUnreadable
	case err != nil:
		if ctx.Err() == nil && !errors.Is(err, probe.ErrNoFFprobe) {
			s.log.Warn("probe stopped; it is tried again later", "file", t.FileID, "path", t.Path, "err", err)
		}
		return err
	}
	fp, err := Fingerprint(t.Path)
	if err != nil {
		fp = ""
	}
	_, err = s.db.SaveProbe(ctx, t, res, fp, probe.Version, s.now().UnixMilli())
	return err
}
