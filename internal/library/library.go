// SPDX-License-Identifier: GPL-3.0-or-later

// Package library builds items from files (docs/06-library-and-metadata.md,
// section 5), and reports how every file's name parsed.
package library

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

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
	// read is how each file's name was read, in the order of the files.
	read []reading
}

type group struct {
	files []int
	what  string // how the report names the item: "the film", "S01E05", "episode 112"
}

// reading is how one file's name was read: as a film's file, or as an
// episode's.
type reading struct {
	class   string
	title   string // the film's, or the series'
	year    int
	episode *parse.Episode // nil for a film's file
	note    string
}

// title is a film's or a series' folder below a root, with grouping
// folders read through (docs/06-library-and-metadata.md, section 4.9), or
// a lone file at the top of a root.
type title struct {
	root  int64
	key   string // the folder below the root, grouping folders included, or the lone file
	lone  bool
	at    []int    // its files, as indexes into the files given to plan
	inner []string // their paths below the grouping folders
}

// titlesOf finds the titles of a library's files: for each root, the
// grouping folders are read through, and each folder below them, or at
// the top of the root, is a title's.
func titlesOf(files []db.LibraryFile) []*title {
	byRoot := map[int64][]int{}
	var roots []int64
	for i, f := range files {
		if _, ok := byRoot[f.RootID]; !ok {
			roots = append(roots, f.RootID)
		}
		byRoot[f.RootID] = append(byRoot[f.RootID], i)
	}
	var out []*title
	for _, r := range roots {
		at := byRoot[r]
		rels := make([]string, len(at))
		for i, n := range at {
			rels[i] = files[n].RelPath
		}
		byKey := map[string]*title{}
		for i, g := range parse.Groups(rels) {
			inner := rels[i]
			if g != "" {
				inner = inner[len(g)+1:]
			}
			top, _, nested := strings.Cut(inner, "/")
			key := rels[i]
			if nested {
				key = path.Join(g, top)
			}
			t := byKey[key]
			if t == nil {
				t = &title{root: r, key: key, lone: !nested}
				byKey[key] = t
				out = append(out, t)
			}
			t.at = append(t.at, at[i])
			t.inner = append(t.inner, inner)
		}
	}
	return out
}

// plan parses every file and groups the files into the items they make.
// A file in the none class makes no item. A films library reads every
// title as a film, a shows library as a series, and a mixed library each
// by its folder (docs/06-library-and-metadata.md, section 4.10).
func plan(lib model.Library, files []db.LibraryFile) planned {
	p := planned{read: make([]reading, len(files))}
	for _, t := range titlesOf(files) {
		var eps []parse.Episode
		if lib.Kind != "movies" {
			eps = parse.ParseEpisodes(t.inner)
		}
		switch {
		case lib.Kind == "movies":
			p.addFilm(files, t, nil)
		case lib.Kind == "shows":
			p.addSeries(files, t, eps)
		case t.lone && parse.HasToken(t.inner[0]):
			// A lone file with an SxxEyy token at the top of a mixed
			// library is an episode with no series folder: no item, as in
			// a shows library.
			p.read[t.at[0]] = reading{class: parse.None, note: "not in a series folder, though its name gives an episode"}
		case t.lone || !seriesFolder(t, eps):
			p.addFilm(files, t, eps)
		default:
			p.addSeries(files, t, eps)
		}
	}
	return p
}

// seriesFolder reports whether a mixed library reads a title's folder as a
// series' (D49): when it holds a season folder, a file with an SxxEyy
// token, or two files that the anchored class gives different numbers and
// that the film rules do not read as parts.
func seriesFolder(t *title, eps []parse.Episode) bool {
	for i, rel := range t.inner {
		if dirs := strings.Split(rel, "/"); len(dirs) > 2 {
			if _, ok := parse.SeasonFolder(dirs[1]); ok {
				return true
			}
		}
		if eps[i].Class == parse.Explicit {
			return true
		}
	}
	return len(numbers(t, eps, parse.Anchored)) > 1
}

// numbers are the different episode numbers that the given classes give
// a title's files, read as episodes, leaving out the parts of one video.
func numbers(t *title, eps []parse.Episode, classes ...string) map[int]bool {
	vs := parse.Versions(t.inner, "")
	out := map[int]bool{}
	for i, e := range eps {
		if vs[i].Part == 0 && slices.Contains(classes, e.Class) {
			out[e.Episode] = true
		}
	}
	return out
}

// filmLooksLikeEpisodes is the note on the files of a film in a mixed
// library whose files, read as episodes, give different numbers.
const filmLooksLikeEpisodes = "read as a film, though its folder's files give different episode numbers: " +
	"a series' folder holds a season folder, a file with an SxxEyy token, or two files with anchored numbers"

// addFilm reads a title as a film. eps, in a mixed library, are its files read
// as episodes: where they give different numbers, every file of the film
// gets a note, since they look like a series' episodes.
func (p *planned) addFilm(files []db.LibraryFile, t *title, eps []parse.Episode) {
	var pf parse.Film
	for i, n := range t.at {
		pf = parse.ParseFilm(t.inner[i])
		p.read[n] = reading{class: pf.Class, title: pf.Title, year: pf.Year}
	}
	// Every file of a folder gives the folder's title, and a lone file is
	// a title by itself.
	if pf.Class == parse.None {
		return
	}
	// A film in a folder is named after it; a lone file is a film by
	// itself, with no versions or parts.
	folder := ""
	if !t.lone {
		folder, _, _ = strings.Cut(t.inner[0], "/")
	}
	p.films = append(p.films, db.DesiredFilm{RootID: t.root, Key: t.key, Title: pf.Title, Year: pf.Year,
		Files: versions(files, t.at, folder)})
	p.groups = append(p.groups, group{files: t.at, what: "the film"})
	if eps != nil && len(numbers(t, eps, parse.Anchored, parse.Dash, parse.Guess)) > 1 {
		for _, n := range t.at {
			p.read[n].note = joinNotes(p.read[n].note, filmLooksLikeEpisodes)
		}
	}
}

// addSeries reads a title as a series, from its files read as episodes.
func (p *planned) addSeries(files []db.LibraryFile, t *title, eps []parse.Episode) {
	type ep struct {
		at int
		p  parse.Episode
	}
	var all []ep
	for i, n := range t.at {
		e := eps[i]
		r := reading{class: e.Class, title: e.SeriesTitle, year: e.SeriesYear, note: e.Note}
		if e.Class != parse.None {
			r.episode = &eps[i]
			all = append(all, ep{n, e})
		}
		p.read[n] = r
	}
	if len(all) == 0 {
		return
	}
	first := all[0].p
	s := db.DesiredSeries{RootID: t.root, Key: t.key, Title: first.SeriesTitle, Year: first.SeriesYear}
	// A number past what a season holds, without a token, makes the
	// series one running count: then every such number is the episode's
	// running number, which is its key. A token, or the specials' folder,
	// says where an episode is, and its number is not in the count.
	counted := func(p parse.Episode) bool {
		return p.Class != parse.Explicit && p.Class != parse.Unnumbered && p.Season != 0
	}
	for _, e := range all {
		if counted(e.p) && e.p.Episode > runningPast {
			s.Absolute = true
		}
	}
	// An episode without a number is its file alone (D51).
	type ekey struct {
		season, episode, absolute int
		file                      int64
	}
	byEp := map[ekey][]ep{}
	var eorder []ekey
	for _, e := range all {
		k := ekey{season: e.p.Season, episode: e.p.Episode}
		switch {
		case e.p.Class == parse.Unnumbered:
			k = ekey{season: e.p.Season, file: files[e.at].ID}
		case s.Absolute && counted(e.p):
			k = ekey{absolute: e.p.Episode}
		}
		if _, ok := byEp[k]; !ok {
			eorder = append(eorder, k)
		}
		byEp[k] = append(byEp[k], e)
	}
	for _, k := range eorder {
		members := byEp[k]
		first := members[0].p
		de := db.DesiredEpisode{Season: first.Season, Episode: first.Episode, Absolute: k.absolute}
		if first.Class == parse.Unnumbered {
			de.Unnumbered, de.Order, de.Title = true, first.Decimal, first.Title
		}
		at := make([]int, len(members))
		for i, m := range members {
			at[i] = m.at
			de.EpisodeEnd = max(de.EpisodeEnd, m.p.EpisodeEnd)
		}
		de.Files = versions(files, at, "")
		s.Episodes = append(s.Episodes, de)
		what := fmt.Sprintf("S%02dE%02d", de.Season, de.Episode)
		switch {
		case de.Unnumbered:
			what = "an episode without a number"
		case de.Absolute != 0:
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
	IsEpisode  bool // read as an episode's file, with the season and numbers below
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
		p := plan(lib, files)
		for i, f := range files {
			rd := p.read[i]
			r := ReportRow{FileID: f.ID, LibraryID: lib.ID, Path: f.RelPath, Class: rd.class, Title: rd.title, Year: rd.year,
				Note: rd.note}
			if e := rd.episode; e != nil {
				r.IsEpisode, r.Season, r.Episode, r.EpisodeEnd = true, e.Season, e.Episode, e.EpisodeEnd
			}
			rows = append(rows, r)
		}
		for _, g := range p.groups {
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
