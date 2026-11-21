// SPDX-License-Identifier: GPL-3.0-or-later

// Package parse reads names: what film, or which episode of which series,
// a file is, from its path below its root. It implements the conventions
// of docs/06-library-and-metadata.md, section 4, and no others.
package parse

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The confidence classes, from surest to least sure
// (docs/06-library-and-metadata.md, sections 4.1 and 4.3).
const (
	Explicit = "explicit" // an SxxEyy token; for a film, a name with its year
	Anchored = "anchored" // the number after the series' own title
	Dash     = "dash"     // a number after " - "
	Guess    = "guess"    // the last number that stands alone; a film without a year
	None     = "none"     // nothing usable: the file makes no item
)

// Classes lists the classes in order.
var Classes = []string{Explicit, Anchored, Dash, Guess, None}

// Film is what a film's path gives.
type Film struct {
	Key   string // the item's key: its folder, or the lone file
	Title string
	Year  int
	Class string
}

// Episode is what an episode's path gives.
type Episode struct {
	SeriesKey   string // the series' folder
	SeriesTitle string
	SeriesYear  int
	Season      int // 0 is the specials
	Episode     int
	EpisodeEnd  int // set for SxxEyy-Ezz
	Class       string
	Note        string // something the report should show, such as a season that disagrees with its folder
}

var (
	brackets   = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	parens     = regexp.MustCompile(`\([^)]*\)`)
	yearParens = regexp.MustCompile(`\((\d{4})\)`)
	spaces     = regexp.MustCompile(`\s+`)
	nonWord    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	token      = regexp.MustCompile(`(?i)\bS(\d{1,3})[ ._-]?E(\d{1,4})(?:[ ._-]?-?[ ._-]?E(\d{1,4}))?`)
	seasonDir  = regexp.MustCompile(`(?i)^season[ ._-]*(\d{1,3})$`)
	afterDash  = regexp.MustCompile(`\s-\s+(\d{1,4})(?:v\d+)?(?:\s|$)`)
	number     = regexp.MustCompile(`^(\d{1,4})(?:v\d+)?$`)
)

// name reads a folder's or a file's name as a title and a year:
// "Name (year) [ids]". Brackets are dropped; ids in them are for matching,
// later.
func name(s string) (title string, year int) {
	s = brackets.ReplaceAllString(s, " ")
	if m := yearParens.FindAllStringSubmatchIndex(s, -1); m != nil {
		last := m[len(m)-1]
		y, _ := strconv.Atoi(s[last[2]:last[3]])
		if y >= 1880 && y <= 2100 {
			year = y
			s = s[:last[0]]
		}
	}
	s = parens.ReplaceAllString(s, " ")
	s = strings.Trim(spaces.ReplaceAllString(s, " "), " -._")
	return s, year
}

// normal is a name in the form names are compared in: lower case, every
// run of other characters one space.
func normal(s string) string {
	return strings.TrimSpace(nonWord.ReplaceAllString(strings.ToLower(s), " "))
}

func base(rel string) string {
	b := path.Base(rel)
	return strings.TrimSuffix(b, path.Ext(b))
}

// ParseFilm reads a film from its path below the root: a folder
// "Name (year) [ids]" holding the film, or a lone file "Name (year).ext"
// at the top.
func ParseFilm(rel string) Film {
	var f Film
	if top, _, nested := strings.Cut(rel, "/"); nested {
		f.Key = top
		f.Title, f.Year = name(top)
	} else {
		f.Key = rel
		f.Title, f.Year = name(base(rel))
	}
	switch {
	case f.Title == "":
		f.Class = None
	case f.Year == 0:
		f.Class = Guess
	default:
		f.Class = Explicit
	}
	return f
}

// ParseEpisode reads an episode from its path below the root: the series'
// folder at the top, perhaps a season folder in it, and the file, whose
// name only has to give the episode's number.
func ParseEpisode(rel string) Episode {
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		// A file at the top of a shows library has no series folder.
		return Episode{Class: None, Note: "not in a series folder"}
	}
	e := Episode{SeriesKey: parts[0], Season: 1}
	e.SeriesTitle, e.SeriesYear = name(parts[0])
	if e.SeriesTitle == "" {
		return Episode{SeriesKey: parts[0], Class: None, Note: "the series folder has no title"}
	}
	folderSeason := -1
	if len(parts) >= 3 {
		dir := parts[1]
		if m := seasonDir.FindStringSubmatch(dir); m != nil {
			folderSeason, _ = strconv.Atoi(m[1])
		} else if strings.EqualFold(dir, "specials") {
			folderSeason = 0
		}
		if folderSeason >= 0 {
			e.Season = folderSeason
		}
	}

	file := base(rel)
	// 1. Bracketed groups go: [Group], [1080p], (…).
	clean := strings.TrimSpace(spaces.ReplaceAllString(parens.ReplaceAllString(brackets.ReplaceAllString(file, " "), " "), " "))

	// 2. An SxxEyy token, with SxxEyy-Ezz for a file that holds several.
	if m := token.FindStringSubmatch(clean); m != nil {
		s, _ := strconv.Atoi(m[1])
		e.Episode, _ = strconv.Atoi(m[2])
		if m[3] != "" {
			if end, _ := strconv.Atoi(m[3]); end > e.Episode {
				e.EpisodeEnd = end
			}
		}
		if folderSeason >= 0 && folderSeason != s {
			e.Note = "the name says season " + m[1] + ", its folder season " + strconv.Itoa(folderSeason)
		}
		e.Season = s
		e.Class = Explicit
		return e
	}

	// 3. The name starts with the series' own title: the first number
	// that stands alone after it, past the folder's year.
	words := strings.Fields(normal(clean))
	titleWords := strings.Fields(normal(e.SeriesTitle))
	if len(titleWords) > 0 && len(words) > len(titleWords) && strings.Join(words[:len(titleWords)], " ") == strings.Join(titleWords, " ") {
		rest := words[len(titleWords):]
		if e.SeriesYear != 0 && len(rest) > 0 && rest[0] == strconv.Itoa(e.SeriesYear) {
			rest = rest[1:]
		}
		for _, w := range rest {
			if m := number.FindStringSubmatch(w); m != nil {
				e.Episode, _ = strconv.Atoi(m[1])
				e.Class = Anchored
				return e
			}
		}
	}

	// 4. A number that follows " - ".
	if m := afterDash.FindStringSubmatch(clean + " "); m != nil {
		e.Episode, _ = strconv.Atoi(m[1])
		e.Class = Dash
		return e
	}

	// 5. The last number that stands alone, for a person to look at.
	for i := len(words) - 1; i >= 0; i-- {
		if m := number.FindStringSubmatch(words[i]); m != nil {
			e.Episode, _ = strconv.Atoi(m[1])
			e.Class = Guess
			return e
		}
	}
	e.Class = None
	e.Note = "no episode number in the name"
	return e
}

// SortTitle is a title as lists sort it: lower case, without a leading
// article (docs/06-library-and-metadata.md, section 5).
func SortTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, a := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, a) && len(t) > len(a) {
			return strings.TrimSpace(t[len(a):])
		}
	}
	return t
}
