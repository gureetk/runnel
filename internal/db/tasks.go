// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Task is one unit of background work: a row of tasks
// (docs/06-library-and-metadata.md, section 9).
type Task struct {
	ID        int64
	Kind      string
	Key       string // what it is about, such as "file:123"
	LibraryID int64  // 0 for work of no library
	Payload   string
	Attempts  int // runs so far, this one included
}

// FileKey is a task's key for a file.
func FileKey(fileID int64) string { return "file:" + strconv.FormatInt(fileID, 10) }

// FileOfKey reads the file id back from a task's key, or 0.
func FileOfKey(key string) int64 {
	if len(key) < 6 || key[:5] != "file:" {
		return 0
	}
	id, _ := strconv.ParseInt(key[5:], 10, 64)
	return id
}

// QueueProbes queues a probe for every video file of a root, or of every
// root with 0, whose probe is missing or older than version, and that is
// there to probe: not missing, on a root that is not away. A probe that is
// queued already, or failed, is left as it is. One that is done or running
// is queued again: its file changed after the probe read it, and the
// running one's end leaves it queued. It returns how many it queued.
func (d *DB) QueueProbes(ctx context.Context, rootID int64, version int, now int64) (int, error) {
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		// The WHERE before ON CONFLICT is SQLite's way to tell the upsert
		// from the SELECT; it is needed even when it holds nothing.
		r, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, created_at)
			 SELECT 'probe', 'file:' || f.id, r.library_id, ? FROM files f JOIN roots r ON r.id = f.root_id
			 WHERE (? = 0 OR f.root_id = ?) AND f.probe_version < ? AND f.missing_since IS NULL AND f.kind = 'video' AND r.available = 1
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = 0, error = NULL
			 WHERE tasks.state IN ('done', 'running')`, now, rootID, rootID, version)
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("db: queue probes: %w", err)
	}
	return int(n), nil
}

// ResetRunning queues again every task left running by a server that
// stopped. That run is not counted as an attempt.
func (d *DB) ResetRunning(ctx context.Context) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET state = 'queued', attempts = max(attempts - 1, 0) WHERE state = 'running'`)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: reset running tasks: %w", err)
	}
	return nil
}

// ClaimTask marks the next task of a kind that may run at now as running,
// and counts the attempt. ok is false when there is none.
func (d *DB) ClaimTask(ctx context.Context, kind string, now int64) (t Task, ok bool, err error) {
	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var lib sql.NullInt64
		var payload sql.NullString
		err := tx.tx.QueryRowContext(ctx,
			`UPDATE tasks SET state = 'running', attempts = attempts + 1
			 WHERE id = (SELECT id FROM tasks WHERE state = 'queued' AND kind = ? AND run_after <= ? ORDER BY run_after, id LIMIT 1)
			 RETURNING id, kind, key, library_id, payload, attempts`, kind, now).
			Scan(&t.ID, &t.Kind, &t.Key, &lib, &payload, &t.Attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		t.LibraryID, t.Payload, ok = lib.Int64, payload.String, true
		return nil
	})
	if err != nil {
		return Task{}, false, fmt.Errorf("db: claim a %s task: %w", kind, err)
	}
	return t, ok, nil
}

// Outcome is how a run of a task ended.
type Outcome int

const (
	// Done: the work is done, or there was nothing to do.
	Done Outcome = iota
	// Retry: it failed, and is tried again at RunAfter.
	Retry
	// Failed: it failed for the last time, and stays failed until an admin
	// retries or cancels it.
	Failed
	// Interrupted: the server stopped it. It is queued again, and the run
	// is not counted as an attempt.
	Interrupted
	// Delayed: it could not run for a reason that is not its own, such as
	// a tool that cannot be run. It is queued again for RunAfter, and the
	// run is not counted.
	Delayed
)

// FinishTask records how a run of a task ended. A task queued again while
// it ran, for a file that changed meanwhile, stays queued. Once its kind
// and library have nothing queued or running, their done tasks are deleted
// together (docs/03-data-model.md, section 4).
func (d *DB) FinishTask(ctx context.Context, t Task, o Outcome, runAfter int64, msg string) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		var q string
		var args []any
		switch o {
		case Done:
			q, args = `UPDATE tasks SET state = 'done', error = NULL WHERE id = ? AND state = 'running'`, []any{t.ID}
		case Retry:
			q, args = `UPDATE tasks SET state = 'queued', run_after = ?, error = ? WHERE id = ? AND state = 'running'`, []any{runAfter, msg, t.ID}
		case Failed:
			q, args = `UPDATE tasks SET state = 'failed', error = ? WHERE id = ? AND state = 'running'`, []any{msg, t.ID}
		case Interrupted:
			q, args = `UPDATE tasks SET state = 'queued', attempts = max(attempts - 1, 0) WHERE id = ? AND state = 'running'`, []any{t.ID}
		case Delayed:
			q, args = `UPDATE tasks SET state = 'queued', attempts = max(attempts - 1, 0), run_after = ?, error = ?
			           WHERE id = ? AND state = 'running'`, []any{runAfter, msg, t.ID}
		}
		if _, err := tx.tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
		_, err := tx.tx.ExecContext(ctx,
			`DELETE FROM tasks WHERE kind = ? AND library_id IS ? AND state = 'done'
			   AND NOT EXISTS (SELECT 1 FROM tasks WHERE kind = ? AND library_id IS ? AND state IN ('queued', 'running'))`,
			t.Kind, nullInt(t.LibraryID), t.Kind, nullInt(t.LibraryID))
		return err
	})
	if err != nil {
		return fmt.Errorf("db: finish task %d: %w", t.ID, err)
	}
	return nil
}

// NextRunAfter is the earliest time after now at which a queued task of
// one of the kinds may run, or 0 when none waits.
func (d *DB) NextRunAfter(ctx context.Context, kinds []string, now int64) (int64, error) {
	if len(kinds) == 0 {
		return 0, nil
	}
	q := `SELECT COALESCE(min(run_after), 0) FROM tasks WHERE state = 'queued' AND run_after > ? AND kind IN (?` + strings.Repeat(",?", len(kinds)-1) + `)`
	args := []any{now}
	for _, k := range kinds {
		args = append(args, k)
	}
	var at int64
	if err := d.r.QueryRowContext(ctx, q, args...).Scan(&at); err != nil {
		return 0, fmt.Errorf("db: next task: %w", err)
	}
	return at, nil
}

// CountTasks counts the tasks of a kind in one of the given states.
func (d *DB) CountTasks(ctx context.Context, kind string, states ...string) (int, error) {
	q := `SELECT count(*) FROM tasks WHERE kind = ? AND state IN (?` + strings.Repeat(",?", len(states)-1) + `)`
	args := []any{kind}
	for _, s := range states {
		args = append(args, s)
	}
	var n int
	if err := d.r.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count tasks: %w", err)
	}
	return n, nil
}

// TaskRow is a task as tests read it back.
type TaskRow struct {
	Task
	State    string
	RunAfter int64
	Error    string
}

// Tasks lists every task, in id order.
func (d *DB) Tasks(ctx context.Context) ([]TaskRow, error) {
	rows, err := d.r.QueryContext(ctx, `SELECT id, kind, key, library_id, payload, attempts, state, run_after, error FROM tasks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("db: read tasks: %w", err)
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var t TaskRow
		var lib sql.NullInt64
		var payload, msg sql.NullString
		if err := rows.Scan(&t.ID, &t.Kind, &t.Key, &lib, &payload, &t.Attempts, &t.State, &t.RunAfter, &msg); err != nil {
			return nil, err
		}
		t.LibraryID, t.Payload, t.Error = lib.Int64, payload.String, msg.String
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTask queues a task, or queues it again when it exists and is done
// or failed.
// QueueTask queues a task to run no earlier than runAfter, a time in
// Unix milliseconds: the queue takes the earliest first, so a task queued
// to run after 1 waits behind every one queued to run after 0. A task of
// that kind and key that is queued, done or failed takes the new payload
// and time; one that runs is left to finish.
func (d *DB) QueueTask(ctx context.Context, kind, key string, libraryID int64, payload string, runAfter, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, payload, run_after, created_at) VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = excluded.run_after, error = NULL,
			   payload = excluded.payload
			 WHERE tasks.state <> 'running'`,
			kind, key, nullInt(libraryID), nullString(payload), runAfter, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: queue a %s task: %w", kind, err)
	}
	return nil
}

// HurryThumbs moves the queued thumbs of a season's episodes, which wait
// behind every other image, to the front, as when the season is opened
// (docs/06-library-and-metadata.md, section 8). It writes only when there
// are such thumbs.
func (d *DB) HurryThumbs(ctx context.Context, seasonID int64) error {
	const which = `kind = 'image' AND state = 'queued' AND run_after = 1 AND key IN
		(SELECT 'tmdb:' || id || ':thumb' FROM items WHERE parent_id = ? AND type = 'episode')`
	var any bool
	if err := d.r.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE `+which+`)`, seasonID).Scan(&any); err != nil || !any {
		return err
	}
	return d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE tasks SET run_after = 0 WHERE `+which, seasonID)
		return err
	})
}

func (d *DB) AddTask(ctx context.Context, kind, key string, libraryID int64, payload string, now int64) error {
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx,
			`INSERT INTO tasks (kind, key, library_id, payload, created_at) VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT (kind, key) DO UPDATE SET state = 'queued', attempts = 0, run_after = 0, error = NULL, payload = excluded.payload
			 WHERE tasks.state IN ('done', 'failed')`,
			kind, key, nullInt(libraryID), nullString(payload), now)
		return err
	})
	if err != nil {
		return fmt.Errorf("db: add a %s task: %w", kind, err)
	}
	return nil
}
