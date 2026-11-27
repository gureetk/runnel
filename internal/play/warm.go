// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"context"
	"sync"
	"sync/atomic"
)

// WarmExtra is what a warm-up holds beyond the file's attachments, which
// come before the first video packet (docs/05-playback.md, section 6).
const WarmExtra = 4 << 20

// Warm is a transcode started ahead of its request. Its output is read
// into a buffer until the buffer is full; then ffmpeg blocks on its pipe
// and uses no CPU. The request that claims it gets the buffer first, then
// the live output.
type Warm struct {
	c     *Child
	limit int

	mu       sync.Mutex
	chunks   [][]byte // what it read, as it read it: no copy grows
	held     int      // bytes in chunks
	stopping atomic.Bool
	done     chan struct{} // closed when the filling stops
}

// Warm starts a transcode as Start does, and holds its output up to limit
// bytes.
func (t *Transcoder) Warm(ctx context.Context, src Source, tg Target, limit int) (*Warm, error) {
	c, err := t.Start(ctx, src, tg)
	if err != nil {
		return nil, err
	}
	w := &Warm{c: c, limit: limit, done: make(chan struct{})}
	for _, chunk := range c.first {
		w.chunks, w.held = append(w.chunks, append([]byte(nil), chunk...)), w.held+len(chunk)
	}
	c.first = nil
	go w.fill()
	return w, nil
}

// fill reads ffmpeg's output into the buffer until it is full, the warm-up
// is claimed or stopped, or the output ends.
func (w *Warm) fill() {
	defer close(w.done)
	for !w.stopping.Load() {
		w.mu.Lock()
		room := w.limit - w.held
		w.mu.Unlock()
		if room <= 0 {
			return
		}
		n, err := w.c.out.Read(w.c.buf[:min(room, len(w.c.buf))])
		if n > 0 {
			chunk := append([]byte(nil), w.c.buf[:n]...)
			w.mu.Lock()
			w.chunks, w.held = append(w.chunks, chunk), w.held+n
			w.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Held reports whether the buffer is full, so that ffmpeg waits.
func (w *Warm) Held() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held >= w.limit
}

// Buffered is how many bytes the warm-up holds.
func (w *Warm) Buffered() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held
}

// Child is the warm-up's ffmpeg child.
func (w *Warm) Child() *Child { return w.c }

// Claim stops the filling and hands over the child, with what was held as
// its first output. A read under way ends first, with what ffmpeg writes
// next, which the client would have waited for anyway.
func (w *Warm) Claim() *Child {
	w.stopping.Store(true)
	<-w.done
	w.mu.Lock()
	w.c.first, w.chunks = w.chunks, nil
	w.mu.Unlock()
	return w.c
}

// Stop kills the warm-up's ffmpeg and frees its slot.
func (w *Warm) Stop() {
	w.stopping.Store(true)
	w.c.Stop()
	<-w.done
}
