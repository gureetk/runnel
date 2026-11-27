// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gureetk/runnel/internal/db"
)

// tracks lists the tracks of a video, by its path.
func (e *env) tracks(rel string) []db.ExternalTrack {
	e.t.Helper()
	k := e.known()[rel]
	f, err := e.db.FileDetailOf(context.Background(), k.ID)
	if err != nil {
		e.t.Fatalf("%s: %v", rel, err)
	}
	return f.Tracks
}

func (e *env) seqOf(rel string) int64 {
	e.t.Helper()
	items, _ := e.db.LibraryItems(context.Background(), e.lib.ID)
	id := e.known()[rel].ID
	for _, it := range items {
		for _, f := range it.FileIDs {
			if f == id {
				return it.UpdatedSeq
			}
		}
	}
	return 0
}

func TestTracksBesideVideos(t *testing.T) {
	e := newEnv(t, "movies")
	e.s.Built = func(ctx context.Context, id int64, _ db.Scope) error { return buildFilms(ctx, e.db, id) }
	main := "Paper Comets (2019)/Paper Comets (2019).mkv"
	other := "Paper Comets (2019)/Paper Comets (2019) - 720p.mkv"
	e.put(main, 4000)
	e.put(other, 4000)
	e.put("Paper Comets (2019)/Paper Comets (2019).eng.forced.srt", 100)
	e.put("Paper Comets (2019)/Paper Comets (2019).Commentary.en.mka", 900)
	e.put("Paper Comets (2019)/Paper Comets (2019) - 720p.jpn.ass", 200)
	e.put("Paper Comets (2019)/Somebody Else.eng.srt", 100)        // no video of that name
	e.put("Paper Comets (2019)/.Paper Comets (2019).eng.srt", 100) // hidden
	e.scan()
	e.fingerprintAll()

	got := e.tracks(main)
	if len(got) != 2 {
		t.Fatalf("tracks of the main version: %+v", got)
	}
	audio, sub := got[0], got[1]
	if audio.Kind != "audio" || audio.Lang != "eng" || audio.Title != "Commentary" || audio.Codec != "" || audio.Size != 900 ||
		sub.Kind != "subtitle" || sub.Codec != "subrip" || sub.Lang != "eng" || !sub.Forced || sub.RelPath != "Paper Comets (2019)/Paper Comets (2019).eng.forced.srt" {
		t.Fatalf("tracks %+v", got)
	}
	if o := e.tracks(other); len(o) != 1 || o[0].Lang != "jpn" || o[0].Codec != "ass" {
		t.Fatalf("tracks of the other version: %+v", o)
	}

	// Scanning again changes nothing: the ids stay, and the film keeps
	// its sequence value.
	seq := e.seqOf(main)
	e.scan()
	if again := e.tracks(main); again[0].ID != audio.ID || again[1].ID != sub.ID || e.seqOf(main) != seq {
		t.Fatalf("a scan that found nothing new changed the tracks: %+v, seq %d then %d", again, seq, e.seqOf(main))
	}

	// One track goes, one changes, one comes: the film takes a new value.
	os.Remove(filepath.Join(e.root, "Paper Comets (2019)/Paper Comets (2019).Commentary.en.mka"))
	e.put("Paper Comets (2019)/Paper Comets (2019).eng.forced.srt", 150)
	e.put("Paper Comets (2019)/Paper Comets (2019).ger.sdh.vtt", 100)
	e.scanFolders("Paper Comets (2019)")
	got = e.tracks(main)
	if len(got) != 2 || got[0].ID != sub.ID || got[0].Size != 150 || got[1].Lang != "ger" || !got[1].HearingImpaired || e.seqOf(main) <= seq {
		t.Fatalf("after the changes: %+v, seq %d then %d", got, seq, e.seqOf(main))
	}

	// A folder that cannot be read keeps its tracks.
	if os.Geteuid() != 0 {
		dir := filepath.Join(e.root, "Paper Comets (2019)")
		os.Chmod(dir, 0o000)
		e.scan()
		os.Chmod(dir, 0o755)
		if len(e.tracks(main)) != 2 {
			t.Fatal("the tracks of a folder that could not be read went")
		}
	}
}

// buildFilms makes one film item per file, so that the test can follow
// an item's sequence value without the library package.
func buildFilms(ctx context.Context, d *db.DB, libraryID int64) error {
	files, err := d.LibraryFiles(ctx, libraryID)
	if err != nil {
		return err
	}
	var films []db.DesiredFilm
	for _, f := range files {
		films = append(films, db.DesiredFilm{RootID: f.RootID, Key: f.RelPath, Title: f.RelPath, Files: []db.DesiredFile{{ID: f.ID}}})
	}
	_, err = d.BuildItems(ctx, libraryID, films, nil, func(s string) string { return s }, 1)
	return err
}
