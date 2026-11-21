// SPDX-License-Identifier: GPL-3.0-or-later

package tasks

import (
	"context"
	"sync"
)

// Gate is the playback gate: a count of the playbacks that are starting.
// While it is above zero, the queue's workers finish what they hold and
// take nothing new (docs/02-architecture.md, section 5).
type Gate struct {
	mu   sync.Mutex
	n    int
	open chan struct{} // closed while the gate is open
}

// NewGate makes an open gate.
func NewGate() *Gate {
	g := &Gate{open: make(chan struct{})}
	close(g.open)
	return g
}

// Hold counts one starting playback until release is called. Calling
// release again does nothing.
func (g *Gate) Hold() (release func()) {
	g.mu.Lock()
	if g.n == 0 {
		g.open = make(chan struct{})
	}
	g.n++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.n--
			if g.n == 0 {
				close(g.open)
			}
			g.mu.Unlock()
		})
	}
}

// Open reports whether no playback is starting.
func (g *Gate) Open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n == 0
}

// Wait returns once the gate is open, or with ctx's error when ctx ends
// first.
func (g *Gate) Wait(ctx context.Context) error {
	g.mu.Lock()
	open := g.open
	g.mu.Unlock()
	select {
	case <-open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
