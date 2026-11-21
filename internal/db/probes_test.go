// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gureetk/runnel/internal/model"
)

// exec runs SQL for a test's setup, in a write transaction.
func exec(t *testing.T, d *DB, query string, args ...any) {
	t.Helper()
	err := d.Write(context.Background(), func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, query, args...)
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// oneFile makes a library with one root and one file in it.
func oneFile(t *testing.T, d *DB) ProbeTarget {
	t.Helper()
	exec(t, d, `INSERT INTO libraries (id, name, kind, created_at) VALUES (1, 'Films', 'movies', 1)`)
	exec(t, d, `INSERT INTO roots (id, library_id, path) VALUES (1, 1, '/media/films')`)
	exec(t, d, `INSERT INTO files (id, root_id, rel_path, size, mtime_ns, first_seen_at) VALUES (1, 1, 'Invented Film (2001)/Invented Film (2001).mkv', 1000, 77, 1)`)
	return ProbeTarget{FileID: 1, Path: "/media/films/Invented Film (2001)/Invented Film (2001).mkv", Size: 1000, MtimeNS: 77}
}

func sampleProbe() model.Probe {
	level := 0
	end := int64(1000)
	return model.Probe{
		Container: "matroska", DurationMS: 2000, BitRate: 400000, AttachmentsBytes: 4800,
		Streams: []model.Stream{
			{Index: 0, Kind: "video", Codec: "av1", Profile: "Main", Width: 320, Height: 180, PixFmt: "yuv420p10le", BitDepth: 10, Level: &level, FPSNum: 24000, FPSDen: 1001},
			{Index: 1, Kind: "audio", Codec: "opus", Lang: "jpn", Default: true, Channels: 2, ChannelLayout: "stereo", SampleRate: 48000},
			{Index: 2, Kind: "subtitle", Codec: "ass", Lang: "eng", Title: "Full", Forced: true},
			{Index: 3, Kind: "attachment", Filename: "Invented-Regular.ttf", MIME: "font/ttf", Size: 4800},
		},
		Chapters: []model.Chapter{{Index: 0, StartMS: 0, EndMS: &end, Title: "Opening"}, {Index: 1, StartMS: 1000}},
	}
}

func TestSaveProbe(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	target := oneFile(t, d)

	saved, err := d.SaveProbe(ctx, target, sampleProbe(), "1000-00112233445566778899aabbccddeeff", 1, 5000)
	if err != nil || !saved {
		t.Fatalf("saved %v, %v", saved, err)
	}

	var container, fp string
	var duration, attach, version, probedAt int64
	var probeErr sql.NullString
	d.r.QueryRowContext(ctx, `SELECT container, fingerprint, duration_ms, attachments_bytes, probe_version, probed_at, probe_error FROM files WHERE id = 1`).
		Scan(&container, &fp, &duration, &attach, &version, &probedAt, &probeErr)
	if container != "matroska" || fp != "1000-00112233445566778899aabbccddeeff" || duration != 2000 || attach != 4800 || version != 1 || probedAt != 5000 || probeErr.Valid {
		t.Fatalf("file row: %s %s %d %d %d %d %v", container, fp, duration, attach, version, probedAt, probeErr)
	}

	var n int
	var level sql.NullInt64
	var depth int
	d.r.QueryRowContext(ctx, `SELECT count(*) FROM streams WHERE file_id = 1`).Scan(&n)
	d.r.QueryRowContext(ctx, `SELECT level, bit_depth FROM streams WHERE file_id = 1 AND idx = 0`).Scan(&level, &depth)
	if n != 4 || !level.Valid || level.Int64 != 0 || depth != 10 {
		t.Fatalf("%d streams, video level %v depth %d", n, level, depth)
	}
	var lang sql.NullString
	var channels sql.NullInt64
	d.r.QueryRowContext(ctx, `SELECT lang, channels FROM streams WHERE file_id = 1 AND idx = 2`).Scan(&lang, &channels)
	if lang.String != "eng" || channels.Valid {
		t.Fatalf("subtitle: lang %v, channels %v (want NULL)", lang, channels)
	}
	var endMS sql.NullInt64
	d.r.QueryRowContext(ctx, `SELECT count(*) FROM chapters WHERE file_id = 1`).Scan(&n)
	d.r.QueryRowContext(ctx, `SELECT end_ms FROM chapters WHERE file_id = 1 AND idx = 1`).Scan(&endMS)
	if n != 2 || endMS.Valid {
		t.Fatalf("%d chapters, second end %v", n, endMS)
	}

	// A second probe replaces the streams rather than adding to them.
	p := sampleProbe()
	p.Streams = p.Streams[:2]
	d.SaveProbe(ctx, target, p, "1000-00112233445566778899aabbccddeeff", 1, 6000)
	d.r.QueryRowContext(ctx, `SELECT count(*) FROM streams WHERE file_id = 1`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d streams after a second probe, want 2", n)
	}

	// No item plays the file yet, so no sequence number was used.
	if s, _ := d.Seq(ctx); s != 0 {
		t.Fatalf("seq = %d", s)
	}
}

func TestStaleProbeIsDropped(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	target := oneFile(t, d)
	// A scan saw the file change while it was being probed.
	exec(t, d, `UPDATE files SET size = 2000, probe_version = 0 WHERE id = 1`)

	saved, err := d.SaveProbe(ctx, target, sampleProbe(), "1000-00112233445566778899aabbccddeeff", 1, 5000)
	if err != nil || saved {
		t.Fatalf("saved %v, %v: a probe of the old file must be dropped", saved, err)
	}
	saved, err = d.SaveProbeError(ctx, target, "Invalid data found when processing input", "", 1, 5000)
	if err != nil || saved {
		t.Fatalf("error saved %v, %v", saved, err)
	}
	var version int
	var n int
	d.r.QueryRowContext(ctx, `SELECT probe_version FROM files WHERE id = 1`).Scan(&version)
	d.r.QueryRowContext(ctx, `SELECT count(*) FROM streams`).Scan(&n)
	if version != 0 || n != 0 {
		t.Fatalf("probe_version %d, %d streams", version, n)
	}
}

func TestSaveProbeError(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	target := oneFile(t, d)
	d.SaveProbe(ctx, target, sampleProbe(), "1000-00112233445566778899aabbccddeeff", 1, 5000)

	saved, err := d.SaveProbeError(ctx, target, "Invalid data found when processing input", "1000-ffeeddccbbaa99887766554433221100", 1, 6000)
	if err != nil || !saved {
		t.Fatalf("saved %v, %v", saved, err)
	}
	var msg, fp string
	var version, attach int64
	var container sql.NullString
	var n int
	d.r.QueryRowContext(ctx, `SELECT probe_error, fingerprint, probe_version, attachments_bytes, container FROM files WHERE id = 1`).
		Scan(&msg, &fp, &version, &attach, &container)
	d.r.QueryRowContext(ctx, `SELECT count(*) FROM streams`).Scan(&n)
	if msg != "Invalid data found when processing input" || fp != "1000-ffeeddccbbaa99887766554433221100" || version != 1 || attach != 0 || container.Valid || n != 0 {
		t.Fatalf("after an error: %q %s %d %d %v, %d streams", msg, fp, version, attach, container, n)
	}
}

func TestProbeBumpsItsItems(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	target := oneFile(t, d)
	exec(t, d, `INSERT INTO items (id, library_id, type, root_id, path_key, title, sort_title, added_at, updated_seq) VALUES (10, 1, 'movie', 1, 'Invented Film (2001)', 'Invented Film', 'invented film', 1, 0)`)
	exec(t, d, `INSERT INTO item_files (item_id, file_id) VALUES (10, 1)`)

	d.SaveProbe(ctx, target, sampleProbe(), "1000-00112233445566778899aabbccddeeff", 1, 5000)
	var seq int64
	d.r.QueryRowContext(ctx, `SELECT updated_seq FROM items WHERE id = 10`).Scan(&seq)
	if now, _ := d.Seq(ctx); seq != 1 || now != 1 {
		t.Fatalf("item seq %d, sequence %d: the item of a probed file takes the next value", seq, now)
	}
}
