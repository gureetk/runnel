// SPDX-License-Identifier: GPL-3.0-or-later

// Package library builds items from files (docs/06-library-and-metadata.md,
// section 5), and reports how every file's name parsed.
package library

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
	"github.com/gureetk/runnel/internal/parse"
)

// runningPast is the highest number one season holds: a series with an
// episode numbered higher without an SxxEyy token is numbered in one
// running count (docs/06-library-and-metadata.md, section 5).
const runningPast = 99

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
	p := plan(lib, files)
	return d.BuildItems(ctx, libraryID, p.films, p.series, parse.SortTitle, now)
}

// planned is what a library's files say its items should be.
type planned struct {
	films  []db.DesiredFilm
	series []db.DesiredSeries
	// groups are the files that make each film and episode, as indexes
	// into the files given to plan, for the report's notes.
	groups []group
}

type group struct {
	files []int
	what  string // how the report names the item: "the film", "S01E05", "episode 112"
}

// plan parses every file and groups the files into the items they make.
// A file in the none class makes no item.
func plan(lib model.Library, files []db.LibraryFile) planned {
	var p planned
	switch lib.Kind {
	case "movies":
		type key struct {
			root int64
			key  string
		}
		byKey := map[key][]int{}
		parsed := map[key]parse.Film{}
		var order []key
		for i, f := range files {
			pf := parse.ParseFilm(f.RelPath)
			if pf.Class == parse.None {
				continue
			}
			k := key{f.RootID, pf.Key}
			if _, ok := byKey[k]; !ok {
				order = append(order, k)
				parsed[k] = pf
			}
			byKey[k] = append(byKey[k], i)
		}
		for _, k := range order {
			pf, at := parsed[k], byKey[k]
			// A film in a folder is named after it; a lone file is a film by
			// itself, with no versions or parts.
			folder := ""
			if pf.Key != files[at[0]].RelPath {
				folder = pf.Key
			}
			p.films = append(p.films, db.DesiredFilm{RootID: k.root, Key: pf.Key, Title: pf.Title, Year: pf.Year,
				Files: versions(files, at, folder)})
			p.groups = append(p.groups, group{files: at, what: "the film"})
		}

	case "shows":
		type key struct {
			root int64
			key  string
		}
		type ep struct {
			at int
			p  parse.Episode
		}
		bySeries := map[key][]ep{}
		var order []key
		for i, f := range files {
			pe := parse.ParseEpisode(f.RelPath)
			if pe.Class == parse.None {
				continue
			}
			k := key{f.RootID, pe.SeriesKey}
			if _, ok := bySeries[k]; !ok {
				order = append(order, k)
			}
			bySeries[k] = append(bySeries[k], ep{i, pe})
		}
		for _, k := range order {
			eps := bySeries[k]
			first := eps[0].p
			s := db.DesiredSeries{RootID: k.root, Key: first.SeriesKey, Title: first.SeriesTitle, Year: first.SeriesYear}
			// A number past what a season holds, without a token, makes the
			// series one running count: then every such number is the
			// episode's running number, which is its key. A token, or the
			// specials' folder, says where an episode is, and its number
			// is not in the count.
			counted := func(p parse.Episode) bool { return p.Class != parse.Explicit && p.Season != 0 }
			for _, e := range eps {
				if counted(e.p) && e.p.Episode > runningPast {
					s.Absolute = true
				}
			}
			type ekey struct{ season, episode, absolute int }
			byEp := map[ekey][]ep{}
			var eorder []ekey
			for _, e := range eps {
				k := ekey{season: e.p.Season, episode: e.p.Episode}
				if s.Absolute && counted(e.p) {
					k = ekey{absolute: e.p.Episode}
				}
				if _, ok := byEp[k]; !ok {
					eorder = append(eorder, k)
				}
				byEp[k] = append(byEp[k], e)
			}
			for _, k := range eorder {
				members := byEp[k]
				de := db.DesiredEpisode{Season: members[0].p.Season, Episode: members[0].p.Episode, Absolute: k.absolute}
				at := make([]int, len(members))
				for i, m := range members {
					at[i] = m.at
					de.EpisodeEnd = max(de.EpisodeEnd, m.p.EpisodeEnd)
				}
				de.Files = versions(files, at, "")
				s.Episodes = append(s.Episodes, de)
				what := fmt.Sprintf("S%02dE%02d", de.Season, de.Episode)
				if de.Absolute != 0 {
					what = fmt.Sprintf("episode %d", de.Absolute)
				}
				p.groups = append(p.groups, group{files: at, what: what})
			}
			sort.Slice(s.Episodes, func(i, j int) bool {
				a, b := s.Episodes[i], s.Episodes[j]
				return a.Season < b.Season || (a.Season == b.Season && (a.Episode < b.Episode || (a.Episode == b.Episode && a.Absolute < b.Absolute)))
			})
			p.series = append(p.series, s)
		}
	}
	return p
}

// versions tells the files of one item apart as versions and parts
// (docs/06-library-and-metadata.md, section 4.4).
func versions(files []db.LibraryFile, at []int, folder string) []db.DesiredFile {
	rels := make([]string, len(at))
	for i, n := range at {
		rels[i] = files[n].RelPath
	}
	vs := parse.Versions(rels, folder)
	out := make([]db.DesiredFile, len(at))
	for i, n := range at {
		out[i] = db.DesiredFile{ID: files[n].ID, Label: vs[i].Label, Part: vs[i].Part}
	}
	slices.SortFunc(out, func(a, b db.DesiredFile) int { return cmp.Compare(a.ID, b.ID) })
	return out
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
		start := len(rows)
		for _, f := range files {
			r := ReportRow{FileID: f.ID, LibraryID: lib.ID, Path: f.RelPath}
			if lib.Kind == "movies" {
				p := parse.ParseFilm(f.RelPath)
				r.Class, r.Title, r.Year = p.Class, p.Title, p.Year
			} else {
				p := parse.ParseEpisode(f.RelPath)
				r.Class, r.Title, r.Year, r.Note = p.Class, p.SeriesTitle, p.SeriesYear, p.Note
				r.Season, r.Episode, r.EpisodeEnd = p.Season, p.Episode, p.EpisodeEnd
			}
			rows = append(rows, r)
		}
		for _, g := range plan(lib, files).groups {
			if len(g.files) < 2 {
				continue
			}
			bases := make([]string, len(g.files))
			for i, n := range g.files {
				bases[i] = parse.Base(files[n].RelPath)
			}
			if parse.Distinct(bases) {
				continue
			}
			note := fmt.Sprintf("one of %d files that make %s, and their names do not tell them apart as versions or parts", len(g.files), g.what)
			for _, n := range g.files {
				rows[start+n].Note = joinNotes(rows[start+n].Note, note)
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
