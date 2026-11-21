// SPDX-License-Identifier: GPL-3.0-or-later

// Package library builds items from files (docs/06-library-and-metadata.md,
// section 5), and reports how every file's name parsed.
//
// What stage 1 builds: films, series and episodes, titled from their
// names. Every file of an item is attached as part 0 without a label.
// Seasons, versions, parts, files that hold several episodes and absolute
// numbering come with stage 2's full item model (docs/01-roadmap.md).
package library

import (
	"context"
	"fmt"
	"sort"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/parse"
)

// Build makes the items of a library what its files say. Running it twice
// changes nothing the second time.
func Build(ctx context.Context, d *db.DB, libraryID int64, now int64) (db.BuildResult, error) {
	lib, err := d.Library(ctx, libraryID)
	if err != nil {
		return db.BuildResult{}, err
	}
	files, err := d.LibraryFiles(ctx, libraryID)
	if err != nil {
		return db.BuildResult{}, err
	}
	films, series := plan(lib, files)
	return d.BuildItems(ctx, libraryID, films, series, parse.SortTitle, now)
}

// plan parses every file and groups the files into the items they make.
// A file in the none class makes no item.
func plan(lib model.Library, files []db.LibraryFile) ([]db.DesiredFilm, []db.DesiredSeries) {
	type key struct {
		root int64
		key  string
	}
	switch lib.Kind {
	case "movies":
		byKey := map[key]*db.DesiredFilm{}
		var order []key
		for _, f := range files {
			p := parse.ParseFilm(f.RelPath)
			if p.Class == parse.None {
				continue
			}
			k := key{f.RootID, p.Key}
			if byKey[k] == nil {
				byKey[k] = &db.DesiredFilm{RootID: f.RootID, Key: p.Key, Title: p.Title, Year: p.Year}
				order = append(order, k)
			}
			byKey[k].FileIDs = append(byKey[k].FileIDs, f.ID)
		}
		out := make([]db.DesiredFilm, 0, len(order))
		for _, k := range order {
			out = append(out, *byKey[k])
		}
		return out, nil

	case "shows":
		type ep struct{ season, episode int }
		bySeries := map[key]*db.DesiredSeries{}
		episodes := map[key]map[ep][]int64{}
		var order []key
		for _, f := range files {
			p := parse.ParseEpisode(f.RelPath)
			if p.Class == parse.None {
				continue
			}
			k := key{f.RootID, p.SeriesKey}
			if bySeries[k] == nil {
				bySeries[k] = &db.DesiredSeries{RootID: f.RootID, Key: p.SeriesKey, Title: p.SeriesTitle, Year: p.SeriesYear}
				episodes[k] = map[ep][]int64{}
				order = append(order, k)
			}
			e := ep{p.Season, p.Episode}
			episodes[k][e] = append(episodes[k][e], f.ID)
		}
		out := make([]db.DesiredSeries, 0, len(order))
		for _, k := range order {
			s := *bySeries[k]
			for e, ids := range episodes[k] {
				s.Episodes = append(s.Episodes, db.DesiredEpisode{Season: e.season, Episode: e.episode, FileIDs: ids})
			}
			sort.Slice(s.Episodes, func(i, j int) bool {
				a, b := s.Episodes[i], s.Episodes[j]
				return a.Season < b.Season || (a.Season == b.Season && a.Episode < b.Episode)
			})
			out = append(out, s)
		}
		return nil, out
	}
	return nil, nil
}

// ReportRow is how one file's name parsed.
type ReportRow struct {
	FileID     int64
	LibraryID  int64
	Path       string // below its root
	Class      string
	Title      string // the film's, or the series'
	Year       int
	Season     int
	Episode    int
	EpisodeEnd int
	Note       string
}

// Report parses the names of every video file of the given libraries
// again and returns how each parsed. Nothing about parsing is stored, so
// the report is always that of the parser that is running.
//
// Files are grouped into the items they make, as building does. Where
// several files make one item and their names do not tell them apart as
// versions or parts, every one of them gets a note: that is how a wrong
// number in a trusted class shows (docs/06-library-and-metadata.md,
// section 4.4).
func Report(ctx context.Context, d *db.DB, libs []model.Library) ([]ReportRow, error) {
	var rows []ReportRow
	for _, lib := range libs {
		files, err := d.LibraryFiles(ctx, lib.ID)
		if err != nil {
			return nil, fmt.Errorf("report: %w", err)
		}
		type item struct {
			root            int64
			key             string
			season, episode int
		}
		groups := map[item][]int{} // rows of the files that make each item
		for _, f := range files {
			r := ReportRow{FileID: f.ID, LibraryID: lib.ID, Path: f.RelPath}
			it := item{root: f.RootID}
			if lib.Kind == "movies" {
				p := parse.ParseFilm(f.RelPath)
				r.Class, r.Title, r.Year = p.Class, p.Title, p.Year
				it.key = p.Key
			} else {
				p := parse.ParseEpisode(f.RelPath)
				r.Class, r.Title, r.Year, r.Note = p.Class, p.SeriesTitle, p.SeriesYear, p.Note
				r.Season, r.Episode, r.EpisodeEnd = p.Season, p.Episode, p.EpisodeEnd
				it.key, it.season, it.episode = p.SeriesKey, p.Season, p.Episode
			}
			rows = append(rows, r)
			if r.Class != parse.None {
				groups[it] = append(groups[it], len(rows)-1)
			}
		}
		for it, at := range groups {
			if len(at) < 2 {
				continue
			}
			bases := make([]string, len(at))
			for i, n := range at {
				bases[i] = parse.Base(rows[n].Path)
			}
			if parse.Distinct(bases) {
				continue
			}
			what := "the film"
			if lib.Kind == "shows" {
				what = fmt.Sprintf("S%02dE%02d", it.season, it.episode)
			}
			note := fmt.Sprintf("one of %d files that make %s, and their names do not tell them apart as versions or parts", len(at), what)
			for _, n := range at {
				rows[n].Note = joinNotes(rows[n].Note, note)
			}
		}
	}
	return rows, nil
}

func joinNotes(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
