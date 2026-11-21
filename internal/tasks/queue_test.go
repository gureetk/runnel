// SPDX-License-Identifier: GPL-3.0-or-later

package tasks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gureetk/runnel/internal/db"
)

type env struct {
	t     *testing.T
	db    *db.DB
	q     *Queue
	gate  *Gate
	clock atomic.Int64 // Unix ms
	libs  [2]int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "runnel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &env{t: t, db: d, gate: NewGate()}
	for i, name := range []string{"Films", "Shows"} {
		lib, err := d.CreateLibrary(ctx, name, []string{"movies", "shows"}[i], "default", []string{t.TempDir()}, 1)
		if err != nil {
			t.Fatal(err)
		}
		e.libs[i] = lib.ID
	}
	e.clock.Store(1_800_000_000_000)
	e.q = New(d, e.gate, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.q.now = func() time.Time { return time.UnixMilli(e.clock.Load()) }
	return e
}

func (e *env) advance(d time.Duration) { e.clock.Add(d.Milliseconds()) }

func (e *env) add(kind, key string, lib int64) {
	e.t.Helper()
	if err := e.db.AddTask(context.Background(), kind, key, lib, "", e.clock.Load()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) drain() {
	e.t.Helper()
	if err := e.q.Drain(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) rows() map[string]db.TaskRow {
	e.t.Helper()
	ts, err := e.db.Tasks(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]db.TaskRow{}
	for _, t := range ts {
		out[t.Key] = t
	}
	return out
}

func TestRunsAndClearsDone(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var ran []string
	var begins, ends int
	e.q.Register(Kind{Name: "image", Limit: 2,
		Run: func(ctx context.Context, t db.Task) error {
			mu.Lock()
			ran = append(ran, t.Key)
			mu.Unlock()
			if t.Key == "b" {
				return errors.New("no such image")
			}
			return nil
		},
		Begin: func() { begins++ },
		End:   func() { ends++ },
	})
	e.add("image", "a", e.libs[0])
	e.add("image", "b", e.libs[0])
	e.add("image", "c", e.libs[1])
	e.drain()
	if len(ran) != 3 || begins != 1 || ends != 1 {
		t.Fatalf("ran %v, begins %d, ends %d", ran, begins, ends)
	}
	// The first library still has b to retry, so its done task stays and
	// counts in its progress. The second's is gone.
	rows := e.rows()
	if len(rows) != 2 || rows["a"].State != "done" || rows["b"].State != "queued" || rows["b"].Attempts != 1 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestRetriesThenFails(t *testing.T) {
	e := newEnv(t)
	runs := 0
	e.q.Register(Kind{Name: "fetch", Limit: 1, Run: func(ctx context.Context, t db.Task) error {
		runs++
		return errors.New("the provider answered 500")
	}})
	e.add("fetch", "item:1", e.libs[0])
	e.drain()
	for n := 1; n <= Retries; n++ {
		r := e.rows()["item:1"]
		if runs != n || r.State != "queued" || r.Attempts != n || r.RunAfter != e.clock.Load()+RetryDelay(n).Milliseconds() || r.Error != "the provider answered 500" {
			t.Fatalf("after run %d: %d runs, %+v", n, runs, r)
		}
		// Not before its time.
		e.advance(RetryDelay(n) - time.Second)
		e.drain()
		if runs != n {
			t.Fatalf("run %d came early", n+1)
		}
		e.advance(time.Second)
		e.drain()
	}
	if r := e.rows()["item:1"]; runs != Retries+1 || r.State != "failed" || r.Attempts != Retries+1 {
		t.Fatalf("%d runs, %+v", runs, r)
	}
	// A failed task stays failed: nothing runs it again by itself.
	e.advance(time.Hour)
	e.drain()
	if runs != Retries+1 {
		t.Fatalf("a failed task ran again: %d runs", runs)
	}
	if got := []time.Duration{RetryDelay(1), RetryDelay(2), RetryDelay(5)}; got[0] != time.Minute || got[1] != 2*time.Minute || got[2] != 16*time.Minute {
		t.Fatalf("delays %v", got)
	}
}

func TestLimit(t *testing.T) {
	e := newEnv(t)
	var running, most atomic.Int32
	release := make(chan struct{})
	e.q.Register(Kind{Name: "probe", Limit: 2, Run: func(ctx context.Context, t db.Task) error {
		n := running.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return nil
	}})
	for _, k := range []string{"1", "2", "3", "4", "5"} {
		e.add("probe", k, e.libs[0])
	}
	done := make(chan struct{})
	go func() { e.drain(); close(done) }()
	waitFor(t, func() bool { return running.Load() == 2 })
	time.Sleep(50 * time.Millisecond)
	if running.Load() != 2 {
		t.Fatalf("%d running, want the limit of 2", running.Load())
	}
	close(release)
	<-done
	if most.Load() != 2 || len(e.rows()) != 0 {
		t.Fatalf("at most %d at once; rows %v", most.Load(), e.rows())
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestGatePausesTheQueue(t *testing.T) {
	e := newEnv(t)
	var runs atomic.Int32
	e.q.Register(Kind{Name: "image", Limit: 2, Run: func(ctx context.Context, t db.Task) error {
		runs.Add(1)
		return nil
	}})
	release := e.gate.Hold()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { e.q.Run(ctx); close(stopped) }()
	e.add("image", "a", e.libs[0])
	e.q.Wake()
	time.Sleep(100 * time.Millisecond)
	if runs.Load() != 0 || e.gate.Open() {
		t.Fatal("a task ran while a playback was starting")
	}
	release()
	release() // a second call changes nothing
	waitFor(t, func() bool { return runs.Load() == 1 })
	if !e.gate.Open() {
		t.Fatal("the gate is still held")
	}
	cancel()
	<-stopped
}

func TestStopAndRestart(t *testing.T) {
	e := newEnv(t)
	started := make(chan struct{}, 1)
	e.q.Register(Kind{Name: "probe", Limit: 1, Run: func(ctx context.Context, t db.Task) error {
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}})
	e.add("probe", "file:1", e.libs[0])
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error)
	go func() { stopped <- e.q.Run(ctx) }()
	<-started
	cancel()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	// Stopped by the server: queued again, and the run is not counted.
	if r := e.rows()["file:1"]; r.State != "queued" || r.Attempts != 0 || r.RunAfter != 0 {
		t.Fatalf("%+v", r)
	}

	// A server that died left a task running: the next start queues it
	// again, without counting that run.
	if _, ok, _ := e.db.ClaimTask(context.Background(), "probe", e.clock.Load()); !ok {
		t.Fatal("nothing to claim")
	}
	q2 := New(e.db, NewGate(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ran := make(chan struct{}, 1)
	q2.Register(Kind{Name: "probe", Limit: 1, Run: func(ctx context.Context, t db.Task) error {
		if t.Attempts != 1 {
			return errors.New("the run of the server that died was counted")
		}
		ran <- struct{}{}
		return nil
	}})
	ctx, cancel = context.WithCancel(context.Background())
	go func() { stopped <- q2.Run(ctx) }()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the task left running did not run again")
	}
	cancel()
	<-stopped
}

func TestPauseHoldsTheKind(t *testing.T) {
	e := newEnv(t)
	broken := true
	runs := 0
	e.q.Register(Kind{Name: "probe", Limit: 1, Run: func(ctx context.Context, t db.Task) error {
		runs++
		if broken {
			return &Pause{Err: errors.New("ffprobe cannot be run"), For: time.Minute}
		}
		return nil
	}})
	e.add("probe", "file:1", e.libs[0])
	e.add("probe", "file:2", e.libs[0])
	e.drain()
	// One run, then the kind waits: the other task is not taken either.
	if r := e.rows(); runs != 1 || r["file:1"].Attempts != 0 || r["file:1"].Error != "ffprobe cannot be run" || r["file:2"].Attempts != 0 {
		t.Fatalf("%d runs, %+v", runs, r)
	}
	e.advance(30 * time.Second)
	e.drain()
	if runs != 1 {
		t.Fatal("a paused kind ran")
	}
	broken = false
	e.advance(30 * time.Second)
	e.drain()
	if runs != 3 || len(e.rows()) != 0 {
		t.Fatalf("%d runs, rows %+v", runs, e.rows())
	}
}

func TestPanicIsAFailedRun(t *testing.T) {
	e := newEnv(t)
	e.q.Register(Kind{Name: "image", Limit: 1, Run: func(ctx context.Context, t db.Task) error {
		panic("a bug")
	}})
	e.add("image", "a", e.libs[0])
	e.drain()
	if r := e.rows()["a"]; r.State != "queued" || r.Attempts != 1 || r.Error != "panic: a bug" {
		t.Fatalf("%+v", r)
	}
}
