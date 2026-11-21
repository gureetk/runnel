// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"sync"
	"time"
)

// maxAddresses bounds the memory a flood of addresses can take.
const maxAddresses = 10_000

// Limiter counts wrong tries by address and refuses an address that has
// made too many within a window (docs/04-api.md, section 1, Wrong tries).
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	tries map[string][]time.Time // the wrong tries inside the window, oldest first
}

// NewLimiter allows max wrong tries per address in window. now is
// time.Now outside tests.
func NewLimiter(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, tries: map[string][]time.Time{}}
}

// Blocked reports whether addr has used up its tries, and if it has, how
// long until it may try again.
func (l *Limiter) Blocked(addr string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.recent(addr, l.now())
	if len(t) < l.max {
		return false, 0
	}
	return true, t[0].Add(l.window).Sub(l.now())
}

// Fail records a wrong try from addr.
func (l *Limiter) Fail(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if _, ok := l.tries[addr]; !ok && len(l.tries) >= maxAddresses {
		for a := range l.tries {
			if len(l.recent(a, now)) == 0 {
				delete(l.tries, a)
			}
		}
		for a := range l.tries {
			if len(l.tries) < maxAddresses {
				break
			}
			delete(l.tries, a)
		}
	}
	l.tries[addr] = append(l.recent(addr, now), now)
}

// recent drops the tries of addr that are outside the window and returns
// the rest. The caller holds mu.
func (l *Limiter) recent(addr string, now time.Time) []time.Time {
	t := l.tries[addr]
	i := 0
	for i < len(t) && now.Sub(t[i]) >= l.window {
		i++
	}
	t = t[i:]
	if len(t) == 0 {
		delete(l.tries, addr)
		return nil
	}
	l.tries[addr] = t
	return t
}
