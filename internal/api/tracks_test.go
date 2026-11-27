// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/library"
	"github.com/gureetk/runnel/internal/scan"
)

func TestExternalTracks(t *testing.T) {
	b := newBrowse(t)
	dir := filepath.Join(b.films, "Paper Comets (2019)")
	srt := []byte("1\n00:00:00,500 --> 00:00:02,000\nAn invented line\n")
	os.WriteFile(filepath.Join(dir, "Paper Comets (2019).eng.forced.srt"), srt, 0o644)
	os.WriteFile(filepath.Join(dir, "Paper Comets (2019).Commentary.fr.mka"), []byte("not really audio"), 0o644)
	ctx := context.Background()
	s := scan.New(b.db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Built = func(ctx context.Context, id int64, scope db.Scope) error {
		_, err := library.BuildIn(ctx, b.db, id, scope, 2)
		return err
	}
	lib, _ := b.db.Library(ctx, 1)
	if _, err := s.ScanLibrary(ctx, lib); err != nil {
		t.Fatal(err)
	}

	f := b.fileOf(t, "Paper Comets")
	if len(f.Subtitles) != 1 || len(f.Audio) != 2 {
		t.Fatalf("subtitles %+v, audio %+v", f.Subtitles, f.Audio)
	}
	own, ext := f.Audio[0], f.Audio[1]
	if own.Idx == nil || own.ExternalID != nil || own.Channels == nil {
		t.Fatalf("the file's own audio: %+v", own)
	}
	if ext.Idx != nil || ext.ExternalID == nil || ext.Codec != nil || ext.Channels != nil || *ext.Lang != "fre" || *ext.Title != "Commentary" {
		t.Fatalf("the audio file beside it: %+v", ext)
	}
	sub := f.Subtitles[0]
	if sub.Idx != nil || sub.ExternalID == nil || *sub.Codec != "subrip" || *sub.Lang != "eng" || !sub.Forced {
		t.Fatalf("the subtitle file beside it: %+v", sub)
	}

	get := func(target string, headers ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, req)
		return rec
	}
	url := "/api/v1/files/" + idStr(f.ID) + "/external/" + idStr(*sub.ExternalID)
	if rec := get(url + "?t=" + b.token); rec.Code != 200 || rec.Body.String() != string(srt) || rec.Header().Get("Content-Type") != "application/x-subrip" {
		t.Fatalf("the subtitle: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := get(url, "Authorization", "Bearer "+b.token, "Range", "bytes=2-11"); rec.Code != 206 || rec.Body.String() != string(srt[2:12]) {
		t.Fatalf("a range of it: %d %q", rec.Code, rec.Body.String())
	}
	other := b.fileOf(t, "The Salt Road")
	for target, want := range map[string]int{
		url: 401,
		"/api/v1/files/" + idStr(other.ID) + "/external/" + idStr(*sub.ExternalID) + "?t=" + b.token: 404, // not that file's
		"/api/v1/files/" + idStr(f.ID) + "/external/99999?t=" + b.token:                              404,
		"/api/v1/files/" + idStr(f.ID) + "/external/abc?t=" + b.token:                                404,
	} {
		if rec := get(target); rec.Code != want {
			t.Errorf("%s: %d, want %d", target, rec.Code, want)
		}
	}
	// The track's file gone from the disk: 503.
	os.Remove(filepath.Join(dir, "Paper Comets (2019).eng.forced.srt"))
	if rec := get(url + "?t=" + b.token); rec.Code != 503 {
		t.Fatalf("a track gone from the disk: %d", rec.Code)
	}
}
