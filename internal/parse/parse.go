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
	EpisodeEnd  int // set for a file that holds several episodes
	Class       string
	Note        string // what the report should show, such as a season that disagrees with its folder
}

var (
	brackets   = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	parens     = regexp.MustCompile(`\([^)]*\)`)
	yearParens = regexp.MustCompile(`\((\d{4})\)`)
	spaces     = regexp.MustCompile(`\s+`)
	nonWord    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	wordRun    = regexp.MustCompile(`[\p{L}\p{N}]+`)

	// An SxxEyy token. It starts the name or follows a character that is
	// not a letter or a digit, so an underscore separates it as a space does.
	token = regexp.MustCompile(`(?i)(?:^|[^\p{L}\p{N}])(S(\d{1,4})[ ._-]{0,3}E(\d{1,4}))`)
	// What can follow a token, or the end of a range, to extend the range:
	// E03 (after a dash, a space, a dot or an underscore, or nothing), -03,
	// and -S01E03 with the same season.
	rangeE     = regexp.MustCompile(`(?i)^[ ._]?-?[ ._]?E(\d{1,4})`)
	rangeNum   = regexp.MustCompile(`^-(\d{1,3})`)
	rangeToken = regexp.MustCompile(`(?i)^ ?- ?S(\d{1,4})[ ._-]{0,3}E(\d{1,4})`)

	seasonDir = regexp.MustCompile(`(?i)^season[ ._-]*(\d{1,4})$`)
	// A number after " - ": the dash between spaces or underscores. E and EP
	// before the number are read as "episode".
	afterDash = regexp.MustCompile(`(?i)(?:^|[\s_])-[\s_]+(?:ep?)?(\d{1,4})(?:v\d+)?(?:[\s_]|$)`)
	// A word that is a number standing alone: 1 to 4 digits, perhaps after E
	// or EP, perhaps with a version suffix such as 05v2.
	number = regexp.MustCompile(`(?i)^(?:ep?)?(\d{1,4})(?:v\d+)?$`)
	// A decimal such as 12.5: a number, a dot and one or two digits, where
	// the number does not follow a dot, so that dots between words are not
	// taken for decimal points.
	decimal = regexp.MustCompile(`(?:^|[^.\d])(\d{1,4})\.(\d{1,2})(?:$|[^.\d\p{L}])`)
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

// Base is a file's name without its folders and its extension.
func Base(rel string) string {
	b := path.Base(rel)
	return strings.TrimSuffix(b, path.Ext(b))
}

// SeasonFolder reads a season folder's name: Season and a number, or
// Specials, which is season 0. Any other name is not a season folder
// (docs/06-library-and-metadata.md, section 4.2).
func SeasonFolder(dir string) (int, bool) {
	if m := seasonDir.FindStringSubmatch(dir); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, true
	}
	if strings.EqualFold(dir, "specials") {
		return 0, true
	}
	return 0, false
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
		f.Title, f.Year = name(Base(rel))
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

// word is a run of letters and digits in a cleaned name, lower case, with
// where it stands.
type word struct {
	text       string
	start, end int
}

func words(s string) []word {
	var out []word
	for _, loc := range wordRun.FindAllStringIndex(s, -1) {
		out = append(out, word{text: strings.ToLower(s[loc[0]:loc[1]]), start: loc[0], end: loc[1]})
	}
	return out
}

// numberOf reads a word that is a number standing alone.
func numberOf(w word) (int, bool) {
	m := number.FindStringSubmatch(w.text)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// decimals lists where the parts of every decimal in s start: 12 and 5 of
// 12.5.
func decimals(s string) map[int]bool {
	out := map[int]bool{}
	for _, m := range decimal.FindAllStringSubmatchIndex(s, -1) {
		out[m[2]], out[m[4]] = true, true
	}
	return out
}

// rangeEnd reads what follows an SxxEyy token for the last episode of a
// file that holds several: -E03, E03, " - E03", -03 and -S01E03, as often
// as they repeat. It returns 0 when there is none. Like Jellyfin, it does
// not read a number that runs on into a digit, a p or an i, as in
// S01E14-1080p, and an end must come after the start.
func rangeEnd(rest string, season, first int) int {
	end, last := 0, first
	ok := func(i int) bool { return i >= len(rest) || !strings.ContainsRune("0123456789iIpP", rune(rest[i])) }
	for {
		var n, used int
		if m := rangeE.FindStringSubmatchIndex(rest); m != nil && ok(m[1]) {
			n, _ = strconv.Atoi(rest[m[2]:m[3]])
			used = m[1]
		} else if m := rangeNum.FindStringSubmatchIndex(rest); m != nil && ok(m[1]) {
			n, _ = strconv.Atoi(rest[m[2]:m[3]])
			used = m[1]
		} else if m := rangeToken.FindStringSubmatchIndex(rest); m != nil && ok(m[1]) {
			if s, _ := strconv.Atoi(rest[m[2]:m[3]]); s != season {
				return end
			}
			n, _ = strconv.Atoi(rest[m[4]:m[5]])
			used = m[1]
		} else {
			return end
		}
		if n <= last {
			return end
		}
		end, last, rest = n, n, rest[used:]
	}
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
	var notes []string
	folderSeason := -1
	dirs := parts[1 : len(parts)-1]
	if len(dirs) > 0 {
		if n, ok := SeasonFolder(dirs[0]); ok {
			folderSeason, e.Season = n, n
			dirs = dirs[1:]
		}
	}
	for _, d := range dirs {
		notes = append(notes, "in the folder "+strconv.Quote(d)+", which is not a season folder")
	}
	done := func(class string, note ...string) Episode {
		e.Class = class
		e.Note = strings.Join(append(notes, note...), "; ")
		return e
	}

	// 1. Bracketed groups go: [Group], [1080p], (…).
	clean := strings.TrimSpace(spaces.ReplaceAllString(parens.ReplaceAllString(brackets.ReplaceAllString(Base(rel), " "), " "), " "))

	// 2. An SxxEyy token, with the end of a range after it for a file that
	// holds several episodes.
	if m := token.FindStringSubmatchIndex(clean); m != nil {
		s, _ := strconv.Atoi(clean[m[4]:m[5]])
		e.Episode, _ = strconv.Atoi(clean[m[6]:m[7]])
		e.EpisodeEnd = rangeEnd(clean[m[3]:], s, e.Episode)
		if folderSeason >= 0 && folderSeason != s {
			notes = append(notes, "the name says season "+clean[m[4]:m[5]]+", its folder season "+strconv.Itoa(folderSeason))
		}
		e.Season = s
		return done(Explicit)
	}

	ws := words(clean)
	dec := decimals(clean)
	// pick takes the number at word w, unless it is part of a decimal, which
	// is not read: it would join the episode of its whole part.
	pick := func(w word, n int, class string, note ...string) Episode {
		if dec[w.start] {
			return done(None, append(note, "a decimal episode number, which is not read")...)
		}
		e.Episode = n
		return done(class, note...)
	}
	dashNumber := func(from int) (word, int, bool) {
		m := afterDash.FindStringSubmatchIndex(clean[from:])
		if m == nil {
			return word{}, 0, false
		}
		n, _ := strconv.Atoi(clean[from+m[2] : from+m[3]])
		return word{start: from + m[2], end: from + m[3]}, n, true
	}

	// 3. The name starts with the series' own title: the first number that
	// stands alone after it, past the folder's year. Where the number after
	// " - " differs from it, it is not trusted, unless it is the season
	// folder's own number: then the number after the dash is the episode.
	title := strings.Fields(normal(e.SeriesTitle))
	if len(title) > 0 && len(ws) > len(title) && sameWords(ws[:len(title)], title) {
		rest := ws[len(title):]
		if e.SeriesYear != 0 && len(rest) > 0 && rest[0].text == strconv.Itoa(e.SeriesYear) {
			rest = rest[1:]
		}
		for _, w := range rest {
			a, ok := numberOf(w)
			if !ok {
				continue
			}
			dw, d, dashed := dashNumber(ws[len(title)-1].end)
			switch {
			case dashed && d != a && folderSeason >= 0 && a == folderSeason:
				return pick(dw, d, Anchored)
			case dashed && d != a:
				return pick(dw, d, Guess, "the number after the title, "+strconv.Itoa(a)+", and the number after the dash, "+strconv.Itoa(d)+", differ")
			}
			return pick(w, a, Anchored)
		}
	}

	// 4. A number that follows " - ".
	if dw, d, ok := dashNumber(0); ok {
		return pick(dw, d, Dash)
	}

	// 5. The last number that stands alone, for a person to look at.
	for i := len(ws) - 1; i >= 0; i-- {
		if n, ok := numberOf(ws[i]); ok {
			return pick(ws[i], n, Guess)
		}
	}
	return done(None, "no episode number in the name")
}

func sameWords(ws []word, title []string) bool {
	for i, w := range ws {
		if w.text != title[i] {
			return false
		}
	}
	return true
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
