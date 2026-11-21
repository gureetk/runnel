// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"testing"
)

func taskState(t *testing.T, d *DB, key string) TaskRow {
	t.Helper()
	ts, err := d.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ts {
		if r.Key == key {
			return r
		}
	}
	return TaskRow{}
}

func TestQueueProbes(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	oneFile(t, d)
	exec(t, d, `INSERT INTO files (id, root_id, rel_path, kind, size, mtime_ns, first_seen_at) VALUES (2, 1, 'Invented Film (2001)/extras/Making of.mkv', 'extra', 10, 1, 1)`)
	exec(t, d, `INSERT INTO files (id, root_id, rel_path, size, mtime_ns, first_seen_at, missing_since) VALUES (3, 1, 'Gone Film (2002)/Gone Film (2002).mkv', 10, 1, 1, 5)`)
	exec(t, d, `INSERT INTO files (id, root_id, rel_path, size, mtime_ns, first_seen_at, probe_version) VALUES (4, 1, 'Done Film (2003)/Done Film (2003).mkv', 10, 1, 1, 1)`)

	// Only a video that is there and not probed: not the extra, the
	// missing file or the probed one.
	if n, err := d.QueueProbes(ctx, 0, 1, 10); err != nil || n != 1 {
		t.Fatalf("queued %d: %v", n, err)
	}
	if r := taskState(t, d, "file:1"); r.Kind != "probe" || r.State != "queued" || r.LibraryID != 1 {
		t.Fatalf("%+v", r)
	}
	// Queued again: nothing changes.
	if n, _ := d.QueueProbes(ctx, 1, 1, 11); n != 0 {
		t.Fatalf("queued %d again", n)
	}
	// A newer probe version needs every file again.
	if n, _ := d.QueueProbes(ctx, 0, 2, 11); n != 1 || taskState(t, d, "file:4").State != "queued" {
		t.Fatalf("queued %d for a newer version", n)
	}
	exec(t, d, `DELETE FROM tasks WHERE key = 'file:4'`)

	// A failed probe stays failed, until its file changes.
	task, ok, err := d.ClaimTask(ctx, "probe", 12)
	if err != nil || !ok || task.Key != "file:1" || task.Attempts != 1 {
		t.Fatalf("claimed %+v %v %v", task, ok, err)
	}
	if err := d.FinishTask(ctx, task, Failed, 0, "ffprobe took longer than a minute"); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.QueueProbes(ctx, 0, 1, 13); n != 0 || taskState(t, d, "file:1").State != "failed" {
		t.Fatalf("a failed probe was queued again: %d", n)
	}
	changed := SeenFile{ID: 1, RelPath: "Invented Film (2001)/Invented Film (2001).mkv", Kind: "video", Size: 2000, MtimeNS: 78}
	if err := d.ApplyScan(ctx, 1, ScanChanges{Changed: []SeenFile{changed}}, 14, 0); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.QueueProbes(ctx, 1, 1, 14); n != 1 {
		t.Fatalf("the changed file was not queued: %d", n)
	}
	if r := taskState(t, d, "file:1"); r.State != "queued" || r.Attempts != 0 || r.Error != "" {
		t.Fatalf("%+v", r)
	}

	// A file that changes while its probe runs is queued again, and the
	// run's end does not undo that.
	task, _, _ = d.ClaimTask(ctx, "probe", 15)
	if n, _ := d.QueueProbes(ctx, 0, 1, 15); n != 1 {
		t.Fatal("a running probe was not queued again")
	}
	if err := d.FinishTask(ctx, task, Done, 0, ""); err != nil {
		t.Fatal(err)
	}
	if r := taskState(t, d, "file:1"); r.State != "queued" {
		t.Fatalf("the run's end undid the new probe: %+v", r)
	}
	// Done, and the last of its kind and library: its row goes.
	task, _, _ = d.ClaimTask(ctx, "probe", 16)
	if err := d.FinishTask(ctx, task, Done, 0, ""); err != nil {
		t.Fatal(err)
	}
	if r := taskState(t, d, "file:1"); r.ID != 0 {
		t.Fatalf("a done task stayed with nothing else to do: %+v", r)
	}
}

func TestFinishTask(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	oneFile(t, d)
	for _, k := range []string{"a", "b"} {
		if err := d.AddTask(ctx, "image", k, 1, `{"size":1}`, 1); err != nil {
			t.Fatal(err)
		}
	}
	a, _, _ := d.ClaimTask(ctx, "image", 1)
	b, _, _ := d.ClaimTask(ctx, "image", 1)
	if a.Key != "a" || b.Key != "b" || a.Payload != `{"size":1}` {
		t.Fatalf("claimed %+v and %+v", a, b)
	}
	if _, ok, _ := d.ClaimTask(ctx, "image", 1); ok {
		t.Fatal("claimed a task that runs")
	}
	if err := d.FinishTask(ctx, a, Done, 0, ""); err != nil {
		t.Fatal(err)
	}
	// b still runs, so a's row stays for the progress count.
	if r := taskState(t, d, "a"); r.State != "done" {
		t.Fatalf("%+v", r)
	}
	if err := d.FinishTask(ctx, b, Retry, 61_000, "no answer"); err != nil {
		t.Fatal(err)
	}
	if r := taskState(t, d, "b"); r.State != "queued" || r.RunAfter != 61_000 || r.Error != "no answer" || taskState(t, d, "a").State != "done" {
		t.Fatalf("%+v", r)
	}
	if _, ok, _ := d.ClaimTask(ctx, "image", 60_999); ok {
		t.Fatal("claimed a task before its time")
	}
	if at, _ := d.NextRunAfter(ctx, []string{"image"}, 1); at != 61_000 {
		t.Fatalf("next run at %d", at)
	}
	b, _, _ = d.ClaimTask(ctx, "image", 61_000)
	if b.Attempts != 2 {
		t.Fatalf("%+v", b)
	}
	if err := d.FinishTask(ctx, b, Delayed, 90_000, "the tool cannot be run"); err != nil {
		t.Fatal(err)
	}
	if r := taskState(t, d, "b"); r.State != "queued" || r.Attempts != 1 || r.RunAfter != 90_000 {
		t.Fatalf("a delayed run was counted: %+v", r)
	}
	b, _, _ = d.ClaimTask(ctx, "image", 90_000)
	if err := d.FinishTask(ctx, b, Failed, 0, "gave up"); err != nil {
		t.Fatal(err)
	}
	// Nothing queued or running: the done row goes, the failed one stays.
	if r := taskState(t, d, "a"); r.ID != 0 || taskState(t, d, "b").State != "failed" {
		t.Fatalf("a %+v, b %+v", r, taskState(t, d, "b"))
	}
	if n, _ := d.CountTasks(ctx, "image", "failed"); n != 1 {
		t.Fatalf("%d failed", n)
	}
	// Queued again by hand, a failed task starts afresh.
	if err := d.AddTask(ctx, "image", "b", 1, "", 100_000); err != nil {
		t.Fatal(err)
	}
	if r := taskState(t, d, "b"); r.State != "queued" || r.Attempts != 0 || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	if FileKey(42) != "file:42" || FileOfKey("file:42") != 42 || FileOfKey("item:42") != 0 {
		t.Fatal("file keys")
	}
}
