// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// openTemp opens a fresh database in a temporary directory.
func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runnel.db")
	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func TestPragmasOnEveryConnection(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()

	check := func(t *testing.T, conn *sql.Conn, queryOnly int) {
		t.Helper()
		want := map[string]any{
			"journal_mode": "wal",
			"synchronous":  int64(1), // NORMAL
			"foreign_keys": int64(1),
			"busy_timeout": int64(5000),
			"cache_size":   int64(-1000),
			"query_only":   int64(queryOnly),
		}
		for name, w := range want {
			var got any
			if err := conn.QueryRowContext(ctx, "PRAGMA "+name).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", name, err)
			}
			if s, ok := got.([]byte); ok {
				got = string(s)
			}
			if got != w {
				t.Errorf("PRAGMA %s = %v (%T), want %v", name, got, got, w)
			}
		}
	}

	wc, err := d.w.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(t, wc, 0)
	wc.Close()

	// Hold every reader at once, so that each of the four is checked.
	var conns []*sql.Conn
	for i := 0; i < readers; i++ {
		c, err := d.r.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		check(t, c, 1)
	}
	for _, c := range conns {
		c.Close()
	}
}

func TestReadersCannotWrite(t *testing.T) {
	d, _ := openTemp(t)
	_, err := d.r.ExecContext(context.Background(), `INSERT INTO meta (key, value) VALUES ('x', 'y')`)
	if err == nil {
		t.Fatal("a reader connection wrote to the database")
	}
}

func TestMigrationMatchesTheDocument(t *testing.T) {
	doc, err := os.ReadFile("../../docs/03-data-model.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("(?s)Migration `0001_init.sql`:\n\n```sql\n(.*?)```").FindSubmatch(doc)
	if m == nil {
		t.Fatal("no migration 0001 block in docs/03-data-model.md")
	}
	file, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The file starts with a comment header and a blank line; the rest is
	// the document's block.
	_, body, ok := strings.Cut(string(file), "\n\n")
	if !ok {
		t.Fatal("0001_init.sql has no header")
	}
	if body != string(m[1]) {
		t.Fatal("migrations/0001_init.sql and docs/03-data-model.md, section 3, differ: change both together")
	}
}

func TestSchema(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()

	v, err := d.schemaVersion(ctx)
	if err != nil || v != 1 || SchemaVersion() != 1 {
		t.Fatalf("schema version %d (%v), embedded %d", v, err, SchemaVersion())
	}

	var tables []string
	rows, err := d.r.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'items_fts_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	want := "chapters credits external_ids external_tracks files images item_files item_genres items items_fts libraries meta overrides pairings people play_log provider_cache roots series_prefs streams tasks tokens tombstones user_list user_state users"
	if got := strings.Join(tables, " "); got != want {
		t.Fatalf("tables:\n got %s\nwant %s", got, want)
	}

	for key, want := range map[string]string{"schema_version": "1", "seq": "0", "tombstone_floor": "0"} {
		var got string
		if err := d.r.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&got); err != nil || got != want {
			t.Errorf("meta %s = %q (%v), want %q", key, got, err, want)
		}
	}
}

func TestSearchTable(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		for id, title := range map[int]string{1: "Paper Comets", 2: "Harbor Lantern"} {
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO items_fts (rowid, title) VALUES (?, ?)`, id, title); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	match := func(q string) []int {
		var ids []int
		rows, err := d.r.QueryContext(ctx, `SELECT rowid FROM items_fts WHERE items_fts MATCH ? ORDER BY rowid`, q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	if got := match(`"omet"`); len(got) != 1 || got[0] != 1 {
		t.Fatalf("substring match: %v", got)
	}
	// contentless_delete=1 lets a row be deleted by rowid.
	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `DELETE FROM items_fts WHERE rowid = 1`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := match(`"omet"`); len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestReopenAndNewerSchema(t *testing.T) {
	d, path := openTemp(t)
	ctx := context.Background()
	id1, err := d.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id1) {
		t.Fatalf("instance id %q is not a version 4 UUID", id1)
	}
	d.Close()

	d2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := d2.InstanceID(ctx)
	if err != nil || id2 != id1 {
		t.Fatalf("instance id after reopen: %q (%v), want %q", id2, err, id1)
	}
	err = d2.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE meta SET value = '99' WHERE key = 'schema_version'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	d2.Close()

	if d3, err := Open(ctx, path); err == nil {
		d3.Close()
		t.Fatal("opened a database newer than the server")
	} else if !strings.Contains(err.Error(), "newer than this server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSequence(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()

	// A transaction that never asks for the sequence uses up no value.
	if err := d.Write(ctx, func(ctx context.Context, tx *Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if s, _ := d.Seq(ctx); s != 0 {
		t.Fatalf("seq = %d after a transaction that changed nothing", s)
	}

	// One value per transaction, however often it is asked for.
	for want := int64(1); want <= 3; want++ {
		err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
			a, err := tx.Seq(ctx)
			if err != nil {
				return err
			}
			b, _ := tx.Seq(ctx)
			if a != want || b != want {
				t.Errorf("Seq = %d, %d; want %d", a, b, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// A transaction that is rolled back gives its value back.
	boom := errors.New("boom")
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		tx.Seq(ctx)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := d.Seq(ctx); s != 3 {
		t.Fatalf("seq = %d after a rollback, want 3", s)
	}
}

func TestWriteIsImmediate(t *testing.T) {
	d, path := openTemp(t)
	ctx := context.Background()

	// A second, independent connection, which does not wait.
	other, err := sql.Open("sqlite3", path+"?_busy_timeout=0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1) // so that ROLLBACK reaches the connection that began

	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		// Nothing has been written yet. With BEGIN IMMEDIATE the write
		// lock is held anyway, so another writer is refused.
		_, err := other.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if err == nil {
			other.ExecContext(ctx, `ROLLBACK`)
			t.Error("a second writer got the lock: the transaction is not IMMEDIATE")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWritersQueue(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()

	var mu sync.Mutex
	inside, most := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
				mu.Lock()
				inside++
				if inside > most {
					most = inside
				}
				mu.Unlock()
				_, err := tx.Seq(ctx)
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d writers ran at once", most)
	}
	if s, _ := d.Seq(ctx); s != 8 {
		t.Fatalf("seq = %d, want 8", s)
	}

	// A writer that is waiting gives up when its context ends.
	hold := make(chan struct{})
	held := make(chan struct{})
	go d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		close(held)
		<-hold
		return nil
	})
	<-held
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	err := d.Write(short, func(ctx context.Context, tx *Tx) error { return nil })
	close(hold)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting writer: err = %v", err)
	}
}

func TestPanicReleasesTheWriter(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	func() {
		defer func() { recover() }()
		d.Write(ctx, func(ctx context.Context, tx *Tx) error {
			tx.Seq(ctx)
			panic("boom")
		})
	}()
	done := make(chan error, 1)
	go func() {
		done <- d.Write(ctx, func(ctx context.Context, tx *Tx) error { _, err := tx.Seq(ctx); return err })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the writer was left locked after a panic")
	}
	if s, _ := d.Seq(ctx); s != 1 {
		t.Fatalf("seq = %d, want 1: the panicking transaction must not commit", s)
	}
}

func TestClose(t *testing.T) {
	d, _ := openTemp(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	err := d.Write(context.Background(), func(ctx context.Context, tx *Tx) error { return nil })
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after Close: %v", err)
	}
}

func TestSetupDone(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	if done, err := d.SetupDone(ctx); err != nil || done {
		t.Fatalf("SetupDone = %v, %v on an empty database", done, err)
	}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO users (name, created_at) VALUES ('ada', 1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if done, err := d.SetupDone(ctx); err != nil || !done {
		t.Fatalf("SetupDone = %v, %v with a user", done, err)
	}
}
