// SPDX-License-Identifier: GPL-3.0-or-later

// Package tasks is the background queue: one queue in the tasks table, run
// by a few workers of each kind, with retries, paused while a playback
// starts (docs/06-library-and-metadata.md, section 9;
// docs/02-architecture.md, section 5).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

// Retries is how often a failed task is tried again before it is left as
// failed.
const Retries = 5

// RetryDelay is how long a task waits after its nth failed run: 1, 2, 4, 8
// and 16 minutes.
func RetryDelay(n int) time.Duration { return time.Minute << max(0, n-1) }

// Kind is one kind of work, and how many of it run at once.
type Kind struct {
	Name  string
	Limit int
	// Run does one task. An error is a failed run, retried as Retries
	// says; a *Pause is not.
	Run func(ctx context.Context, t db.Task) error
	// Begin, when set, is called when work of this kind starts after an
	// idle spell, before its first run. End is called when none of it is
	// left that may run now. Both are called from the queue's own
	// goroutine.
	Begin, End func()
}

// Pause is the error of a run that could not do its work for a reason
// that is not the task's: a tool that cannot be run, or a provider that
// asked to wait. The task is queued again, the run is not counted, and
// the whole kind waits For before the queue takes any of it.
type Pause struct {
	Err error
	For time.Duration
}

func (p *Pause) Error() string { return p.Err.Error() }
func (p *Pause) Unwrap() error { return p.Err }

type kind struct {
	Kind
	running int
	busy    bool  // Begin was called, End not yet
	until   int64 // paused until then
}

// Queue runs the tasks of the kinds registered with it.
type Queue struct {
	db   *db.DB
	gate *Gate
	log  *slog.Logger
	now  func() time.Time

	mu    sync.Mutex
	kinds []*kind
	wake  chan struct{}
	ended chan struct{} // a run ended
}

// New makes a queue that pauses while gate is held.
func New(d *db.DB, gate *Gate, log *slog.Logger) *Queue {
	return &Queue{db: d, gate: gate, log: log, now: time.Now,
		wake: make(chan struct{}, 1), ended: make(chan struct{}, 1)}
}

// Register adds a kind of work. It is called before Run.
func (q *Queue) Register(k Kind) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds = append(q.kinds, &kind{Kind: k})
}

// Paused reports whether a kind waits for a cause that is not its tasks'
// own: a Pause one of its runs returned (docs/04-api.md, section 8.3,
// paused_by "provider").
func (q *Queue) Paused(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now().UnixMilli()
	for _, k := range q.kinds {
		if k.Name == name {
			return k.until > now
		}
	}
	return false
}

// Resume ends a kind's pause before its time, as a new metadata key does
// for work that the provider's refusal of the old one paused, and wakes
// the queue.
func (q *Queue) Resume(name string) {
	q.mu.Lock()
	for _, k := range q.kinds {
		if k.Name == name {
			k.until = 0
		}
	}
	q.mu.Unlock()
	q.Wake()
}

// Wake tells the queue that tasks were queued.
func (q *Queue) Wake() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Run runs tasks until ctx ends, and then waits for the runs under way,
// which ctx's end stops. Tasks a stopped server left running are queued
// again first.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.db.ResetRunning(ctx); err != nil {
		return err
	}
	return q.loop(ctx, false)
}

// Drain runs tasks until none is left that may run now, and none runs.
func (q *Queue) Drain(ctx context.Context) error { return q.loop(ctx, true) }

func (q *Queue) loop(ctx context.Context, untilIdle bool) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		if q.gate.Wait(ctx) != nil {
			return nil
		}
		claimed, idle, err := q.dispatch(ctx, &wg)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			q.log.Error("background work: cannot take tasks", "err", err)
		}
		if claimed {
			continue
		}
		if untilIdle && idle && err == nil {
			return nil
		}
		q.sleep(ctx, err != nil)
	}
}

// sleep waits until something may have changed: a task was queued, a run
// ended, or a waiting task's time came.
func (q *Queue) sleep(ctx context.Context, failed bool) {
	wait := time.Hour
	if failed {
		wait = 10 * time.Second
	}
	now := q.now().UnixMilli()
	if at, err := q.db.NextRunAfter(ctx, q.names(), now); err == nil && at > 0 {
		wait = min(wait, time.Duration(at-now)*time.Millisecond)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-q.wake:
	case <-q.ended:
	case <-t.C:
	}
}

func (q *Queue) names() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.kinds))
	for i, k := range q.kinds {
		out[i] = k.Name
	}
	return out
}

// dispatch takes as many tasks of each kind as may run, while the gate is
// open. idle is true when nothing runs and nothing was taken.
func (q *Queue) dispatch(ctx context.Context, wg *sync.WaitGroup) (claimed, idle bool, err error) {
	q.mu.Lock()
	kinds := append([]*kind(nil), q.kinds...)
	q.mu.Unlock()
	idle = true
	now := q.now().UnixMilli()
	for _, k := range kinds {
		took := false
		for err == nil && q.gate.Open() {
			q.mu.Lock()
			room := k.running < max(1, k.Limit) && k.until <= now
			q.mu.Unlock()
			if !room {
				break
			}
			var t db.Task
			var ok bool
			if t, ok, err = q.db.ClaimTask(ctx, k.Name, now); err != nil || !ok {
				break
			}
			took, claimed = true, true
			q.mu.Lock()
			begin := !k.busy && k.Begin != nil
			k.busy = true
			k.running++
			q.mu.Unlock()
			if begin {
				k.Begin()
			}
			wg.Add(1)
			go q.run(ctx, k, t, wg)
		}
		q.mu.Lock()
		end := k.busy && k.running == 0 && !took && q.gate.Open()
		if end {
			k.busy = false
		}
		if k.running > 0 {
			idle = false
		}
		q.mu.Unlock()
		if end && k.End != nil {
			k.End()
		}
	}
	return claimed, idle, err
}

// run does one task and records how it ended.
func (q *Queue) run(ctx context.Context, k *kind, t db.Task, wg *sync.WaitGroup) {
	defer wg.Done()
	err := q.call(ctx, k, t)
	now := q.now()
	o, at, msg := db.Done, int64(0), ""
	var pause *Pause
	switch {
	case err == nil:
	case ctx.Err() != nil:
		o = db.Interrupted
	case errors.As(err, &pause):
		o, at, msg = db.Delayed, now.Add(pause.For).UnixMilli(), err.Error()
		q.mu.Lock()
		k.until = max(k.until, at)
		q.mu.Unlock()
		q.log.Debug("background work paused", "kind", t.Kind, "for", pause.For, "err", err)
	case t.Attempts <= Retries:
		delay := RetryDelay(t.Attempts)
		o, at, msg = db.Retry, now.Add(delay).UnixMilli(), err.Error()
		q.log.Warn("a task failed; it is tried again later", "kind", t.Kind, "key", t.Key, "attempt", t.Attempts, "in", delay, "err", err)
	default:
		o, msg = db.Failed, err.Error()
		q.log.Error("a task failed for the last time", "kind", t.Kind, "key", t.Key, "attempts", t.Attempts, "err", err)
	}
	// The database closes only after the queue has stopped, so how a run
	// ended is written even while the server stops.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	if err := q.db.FinishTask(fctx, t, o, at, msg); err != nil {
		q.log.Error("background work: cannot record how a task ended", "kind", t.Kind, "key", t.Key, "err", err)
	}
	cancel()
	q.mu.Lock()
	k.running--
	q.mu.Unlock()
	select {
	case q.ended <- struct{}{}:
	default:
	}
}

// call runs a task, and turns a panic into a failed run.
func (q *Queue) call(ctx context.Context, k *kind, t db.Task) (err error) {
	defer func() {
		if v := recover(); v != nil {
			q.log.Error("panic in background work", "kind", t.Kind, "key", t.Key, "panic", v, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", v)
		}
	}()
	return k.Run(ctx, t)
}
