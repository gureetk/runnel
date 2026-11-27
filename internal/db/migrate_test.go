// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// oldDatabase makes a database as a server with only the first `upto`
// migrations left it, with rows in every table that a person's use writes,
// and returns its path. At upto 2 it is the schema of v0.2.0-rc.1.
func oldDatabase(t *testing.T, upto int) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runnel.db")
	db, err := sql.Open("sqlite3", path+"?"+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ms, err := migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:upto] {
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	rows := []string{
		`INSERT INTO meta (key, value) VALUES ('schema_version', '` + strconv.Itoa(upto) + `'), ('instance_id', 'b6c1f0a2'),
		   ('server_name', 'Upstairs'), ('last_item_id', '40'), ('last_library_id', '3'), ('last_root_id', '3'),
		   ('last_user_id', '2'), ('last_token_id', '5')
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		`UPDATE meta SET value = '57' WHERE key = 'seq'`,
		`INSERT INTO libraries (id, name, kind, profile, created_at, updated_seq) VALUES
		   (1, 'Films', 'movies', 'default', 100, 3), (3, 'Anime', 'shows', 'anime', 101, 9)`,
		`INSERT INTO roots (id, library_id, path, available, last_scan_at, last_scan_ms) VALUES
		   (1, 1, '/media/films', 1, 200, 12), (3, 3, '/media/anime', 0, 201, 30)`,
		`INSERT INTO files (id, root_id, rel_path, size, mtime_ns, fingerprint, container, duration_ms, probe_version, first_seen_at) VALUES
		   (1, 1, 'Paper Comets (2019)/Paper Comets (2019).mkv', 1000, 5, 'fp1', 'matroska', 6240000, 1, 300),
		   (2, 3, 'Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E01.mkv', 2000, 6, 'fp2', 'matroska', 1440000, 1, 301),
		   (3, 3, 'Harbor Lantern (2021)/Season 01/Harbor Lantern - S01E02.mkv', 2001, 7, 'fp3', 'matroska', 1440000, 1, 302)`,
		`INSERT INTO streams (file_id, idx, kind, codec, lang) VALUES (1, 0, 'video', 'av1', NULL), (1, 1, 'audio', 'opus', 'jpn')`,
		`INSERT INTO items (id, library_id, type, root_id, path_key, title, sort_title, year, match_state, added_at, updated_seq) VALUES
		   (7, 1, 'movie', 1, 'Paper Comets (2019)', 'Paper Comets', 'paper comets', 2019, 'matched', 300, 3),
		   (20, 3, 'series', 3, 'Harbor Lantern (2021)', 'Harbor Lantern', 'harbor lantern', 2021, 'unmatched', 302, 9)`,
		`INSERT INTO items (id, library_id, type, parent_id, series_id, season_number, title, sort_title, added_at, updated_seq) VALUES
		   (21, 3, 'season', 20, 20, 1, 'Season 1', 'season 1', 302, 9)`,
		`INSERT INTO items (id, library_id, type, parent_id, series_id, season_number, episode_number, title, sort_title, added_at, updated_seq) VALUES
		   (22, 3, 'episode', 21, 20, 1, 1, 'Episode 1', 'episode 1', 301, 9),
		   (40, 3, 'episode', 21, 20, 1, 2, 'Episode 2', 'episode 2', 302, 9)`,
		`INSERT INTO item_files (item_id, file_id, label, part) VALUES (7, 1, NULL, 0), (22, 2, NULL, 0), (40, 3, NULL, 0)`,
		`INSERT INTO external_ids (item_id, provider, value) VALUES (7, 'tmdb', '123456'), (7, 'imdb', 'tt0000001')`,
		`INSERT INTO images (id, item_id, kind, key, widths, aspect, color, source, source_ref, rank) VALUES
		   (1, 7, 'poster', '0e9a3f6d4c7e8a2b9d0f1e3c5a7b5b1c', '[342,500]', 0.667, '#22313f', 'local', '{}', 0)`,
		`INSERT INTO overrides (root_id, path_key, provider, value, created_at) VALUES (3, 'Harbor Lantern (2021)', 'none', '', 400)`,
		`INSERT INTO users (id, name, pw_hash, pw_salt, pw_iter, is_admin, audio_lang, created_at) VALUES
		   (1, 'ada', x'01', x'02', 600000, 1, 'jpn', 10), (2, 'noor', NULL, NULL, NULL, 0, NULL, 11)`,
		`INSERT INTO tokens (id, hash, user_id, device_id, device_name, client, created_at, last_used_at) VALUES
		   (4, x'aa', 1, 'tv-1', 'Living room TV', 'runnel-androidtv/1.0.0', 20, 30),
		   (5, x'bb', 2, 'web-1', 'Firefox', 'runnel-web/0.1.0', 21, NULL)`,
		`INSERT INTO user_state (user_id, item_id, position_ms, played, play_count, last_played_at, file_id, updated_seq) VALUES
		   (1, 7, 1980000, 0, 0, 500, 1, 50), (1, 22, 0, 1, 2, 510, 2, 51), (2, 40, 60000, 0, 1, 520, 3, 52)`,
		`INSERT INTO user_list (user_id, item_id, listed_at, updated_seq) VALUES (1, 7, 600, 53), (2, 20, NULL, 54)`,
		`INSERT INTO series_prefs (user_id, series_id, audio_lang, sub_lang, sub_mode, updated_seq) VALUES (1, 20, 'jpn', 'eng', 'on', 55)`,
		`INSERT INTO tombstones (seq, item_id, at) VALUES (56, 39, 700)`,
		`INSERT INTO tasks (id, kind, key, library_id, state, created_at) VALUES (1, 'probe', 'file:3', 3, 'queued', 800)`,
		`INSERT INTO play_log (id, at, user_id, item_id, file_id, mode, detail) VALUES (1, 900, 1, 22, 2, 'direct', '{}')`,
	}
	for _, q := range rows {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return path
}

// keptTables are the tables a migration must leave as they were: every
// one but the search index, which has no content of its own.
var keptTables = []string{"chapters", "credits", "external_ids", "external_tracks", "files", "images", "item_files",
	"item_genres", "items", "libraries", "meta", "overrides", "pairings", "people", "play_log", "provider_cache",
	"roots", "series_prefs", "streams", "tasks", "tokens", "tombstones", "user_list", "user_state", "users"}

// dump reads every row of the kept tables, as text, in order.
func dump(t *testing.T, db *sql.DB, skipMeta ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range keptTables {
		rows, err := db.Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var b strings.Builder
			for i, v := range vals {
				if bs, ok := v.([]byte); ok {
					v = fmt.Sprintf("%x", bs)
				}
				fmt.Fprintf(&b, "%s=%v ", cols[i], v)
			}
			line := b.String()
			if table == "meta" && slices.ContainsFunc(skipMeta, func(k string) bool { return strings.HasPrefix(line, "key="+k+" ") }) {
				continue
			}
			out[table] = append(out[table], line)
		}
		rows.Close()
		slices.Sort(out[table])
	}
	return out
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?"+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestRebuildKeepsEverything upgrades a database as v0.2.0-rc.1 made it
// (exit criterion 13 of stage 4): the copy in backup/ is made first and
// holds the database as it was, and the upgrade keeps every row and every
// id, with the new kinds allowed and foreign keys whole.
func TestRebuildKeepsEverything(t *testing.T) {
	ctx := context.Background()
	path := oldDatabase(t, 2)
	before := dump(t, openRaw(t, path), "schema_version")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if v, _ := d.schemaVersion(ctx); v != 3 {
		t.Fatalf("schema version %d", v)
	}
	after := dump(t, d.r, "schema_version")
	for _, table := range keptTables {
		if !slices.Equal(after[table], before[table]) {
			t.Errorf("%s changed:\n got %q\nwant %q", table, after[table], before[table])
		}
	}
	if len(after["user_state"]) != 3 || len(after["tokens"]) != 2 || len(after["user_list"]) != 2 || len(after["items"]) != 5 {
		t.Fatalf("rows lost: %d states, %d tokens, %d listings, %d items",
			len(after["user_state"]), len(after["tokens"]), len(after["user_list"]), len(after["items"]))
	}

	// The copy is the database as it was, at its own schema version.
	if d.Backup == "" || !strings.HasPrefix(filepath.Base(d.Backup), "runnel-schema2-") || filepath.Base(filepath.Dir(d.Backup)) != "backup" {
		t.Fatalf("backup %q", d.Backup)
	}
	if fi, err := os.Stat(d.Backup); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the copy's mode: %v (%v)", fi.Mode(), err)
	}
	old := openRaw(t, d.Backup)
	var v string
	old.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
	if v != "2" {
		t.Fatalf("the copy is at schema version %q", v)
	}
	copied := dump(t, old, "schema_version")
	for _, table := range keptTables {
		if !slices.Equal(copied[table], before[table]) {
			t.Errorf("the copy's %s differs from the database before", table)
		}
	}

	// Foreign keys are on again, still point where they did, and hold.
	var on int
	d.w.QueryRow(`PRAGMA foreign_keys`).Scan(&on)
	if on != 1 {
		t.Fatal("foreign keys are off after the migration")
	}
	if n := rowCount(t, d.w, `SELECT count(*) FROM pragma_foreign_key_check`); n != 0 {
		t.Fatalf("%d foreign keys broken", n)
	}
	var schema string
	d.w.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'items'`).Scan(&schema)
	if !strings.Contains(schema, "REFERENCES items(id)") || !strings.Contains(schema, "REFERENCES libraries(id)") || strings.Contains(schema, "new_") {
		t.Fatalf("items: %s", schema)
	}
	for _, name := range []string{"items_path", "items_list", "items_added", "items_parent", "items_series", "items_seq", "items_collection", "images_key"} {
		if rowCount(t, d.w, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = '`+name+`'`) != 1 {
			t.Errorf("index %s is missing", name)
		}
	}
	// Removing a library still takes its items, and their state, with it.
	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `DELETE FROM libraries WHERE id = 3`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, d.w, `SELECT count(*) FROM user_state WHERE item_id IN (22, 40)`); n != 0 {
		t.Fatalf("%d states of a removed library's items are left", n)
	}

	// The new kinds and the new type are allowed; others are still not.
	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO libraries (id, name, kind, created_at) VALUES (8, 'Both', 'mixed', 1), (9, 'Home', 'videos', 1)`); err != nil {
			return err
		}
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO items (id, library_id, type, title, sort_title, added_at, updated_seq) VALUES (90, 9, 'video', 'Beach', 'beach', 1, 1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO libraries (id, name, kind, created_at) VALUES (10, 'Music', 'music', 1)`)
		return err
	})
	if err == nil {
		t.Fatal("a kind of library that does not exist was allowed")
	}
}

func rowCount(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestANewDatabaseIsNotCopied(t *testing.T) {
	d, path := openTemp(t)
	if d.Backup != "" {
		t.Fatalf("a new database was copied to %s", d.Backup)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "backup")); !os.IsNotExist(err) {
		t.Fatalf("backup/ was made for a new database (%v)", err)
	}
}

// TestNoRoomForTheCopy refuses to migrate when the data directory has no
// room for the copy, and changes nothing.
func TestNoRoomForTheCopy(t *testing.T) {
	path := oldDatabase(t, 2)
	before := dump(t, openRaw(t, path))
	saved := freeSpace
	freeSpace = func(string) (uint64, error) { return 1 << 10, nil }
	defer func() { freeSpace = saved }()
	_, err := Open(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "Nothing has been changed") || !strings.Contains(err.Error(), "MB free") {
		t.Fatalf("err = %v", err)
	}
	after := dump(t, openRaw(t, path))
	for _, table := range keptTables {
		if !slices.Equal(after[table], before[table]) {
			t.Errorf("%s changed", table)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(path), "backup"))
	if len(entries) != 0 {
		t.Fatalf("backup/ holds %d files", len(entries))
	}
}

// TestBackupsKept keeps the newest three copies and deletes a copy that was
// cut short.
func TestBackupsKept(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"runnel-schema2-20261101-000000.db", "runnel-schema2-20261102-000000.db",
		"runnel-schema3-20261103-000000.db", "runnel-schema4-20261104-000000.db",
		"runnel-schema5-20261105-000000.db.partial", "notes.txt",
	}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	pruneBackups(dir)
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := []string{"notes.txt", "runnel-schema2-20261102-000000.db", "runnel-schema3-20261103-000000.db", "runnel-schema4-20261104-000000.db"}
	if !slices.Equal(left, want) {
		t.Fatalf("left %q, want %q", left, want)
	}
}

// TestAFailedRebuildChangesNothing rolls a rebuild back when a foreign key
// would break, and leaves foreign keys on.
func TestAFailedRebuildChangesNothing(t *testing.T) {
	ctx := context.Background()
	path := oldDatabase(t, 2)
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	bad := migration{version: 4, name: "0004_test.sql", rebuilds: true, sql: `
CREATE TABLE new_users (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO new_users (id, name) SELECT id, name FROM users WHERE id = 1;
DROP TABLE users;
ALTER TABLE new_users RENAME TO users;`}
	err = d.rebuild(ctx, bad)
	if err == nil || !strings.Contains(err.Error(), "foreign keys") {
		t.Fatalf("err = %v", err)
	}
	if n := rowCount(t, d.w, `SELECT count(*) FROM users`); n != 2 {
		t.Fatalf("%d users after a rebuild that was rolled back", n)
	}
	if v, _ := d.schemaVersion(ctx); v != 3 {
		t.Fatalf("schema version %d", v)
	}
	var on int
	d.w.QueryRow(`PRAGMA foreign_keys`).Scan(&on)
	if on != 1 {
		t.Fatal("foreign keys are off")
	}
}
