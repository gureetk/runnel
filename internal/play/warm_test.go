// SPDX-License-Identifier: GPL-3.0-or-later

package play

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestAWarmUpHoldsThenHandsOver: the warm-up reads up to its limit and
// stops, so that ffmpeg waits; claimed, it gives what it held first, then
// the rest of the stream, as a transcode without it would have.
func TestAWarmUpHoldsThenHandsOver(t *testing.T) {
	src := film(t)
	tr, _ := newTranscoder(t, softwareOnly, 1)
	var plain bytes.Buffer
	c, err := tr.Start(context.Background(), src, h264)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Relay(&plain, func() {}); err != nil {
		t.Fatal(err)
	}
	c.Stop()

	const limit = 96 << 10
	w, err := tr.Warm(context.Background(), src, h264, limit)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Running() != 1 {
		t.Fatalf("a warm-up takes a slot: running %d", tr.Running())
	}
	deadline := time.Now().Add(10 * time.Second)
	for !w.Held() {
		if time.Now().After(deadline) {
			t.Fatalf("not held after 10 s: %d bytes", w.Buffered())
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if n := w.Buffered(); n < limit || n > limit+readSize {
		t.Fatalf("held %d bytes for a limit of %d", n, limit)
	}
	held := w.Buffered()
	if gone(w.Child().PID()) {
		t.Fatal("ffmpeg ended while held")
	}
	claimed := w.Claim()
	var out bytes.Buffer
	if err := claimed.Relay(&out, func() {}); err != nil {
		t.Fatal(err)
	}
	claimed.Stop()
	// Matroska's segment id is random, so the bytes differ from a plain
	// stream's; the length does not, beyond a little.
	if out.Len() < held || !bytes.HasPrefix(out.Bytes(), []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		t.Fatalf("the claimed stream: %d bytes, %d held", out.Len(), held)
	}
	if d := out.Len() - plain.Len(); d < -4096 || d > 4096 {
		t.Errorf("the claimed stream is %d bytes, a plain one %d", out.Len(), plain.Len())
	}
	if tr.Running() != 0 {
		t.Errorf("running %d after the stream", tr.Running())
	}

	// Stopped before it is claimed: ffmpeg goes, and the slot with it.
	w, err = tr.Warm(context.Background(), src, h264, limit)
	if err != nil {
		t.Fatal(err)
	}
	pid := w.Child().PID()
	w.Stop()
	if !gone(pid) || tr.Running() != 0 {
		t.Errorf("after Stop: ffmpeg gone %v, running %d", gone(pid), tr.Running())
	}
}
