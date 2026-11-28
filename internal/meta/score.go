// SPDX-License-Identifier: GPL-3.0-or-later

package meta

import (
	"strings"
	"unicode"
)

// The thresholds of an automatic match (P4; docs/06-library-and-metadata.md,
// section 6.2): the best candidate is taken when it scores at least
// Accept and at least Lead more than the second.
const (
	Accept = 0.90
	Lead   = 0.10
)

// The parts of a score: the title's similarity, the year, and the kind.
const (
	titleWeight = 0.7
	yearWeight  = 0.2
	kindWeight  = 0.1
)

// Score is how well a candidate fits a film's or a series' title and
// year, from 0 to 1. The title's part is the best similarity of the
// candidate's title or original title to the item's, once both are
// normalised. The year's is whole when they are the same, half when they
// are a year apart or either is unknown, and none otherwise. The kind's
// is whole: a film is searched among films and a series among series.
func Score(title string, year int, candTitle, candOriginal string, candYear int) float64 {
	t := similarity(normalise(title), normalise(candTitle))
	if candOriginal != "" {
		t = max(t, similarity(normalise(title), normalise(candOriginal)))
	}
	var y float64
	switch d := year - candYear; {
	case year == 0 || candYear == 0:
		y = 0.5
	case d == 0:
		y = 1
	case d == 1 || d == -1:
		y = 0.5
	}
	return titleWeight*t + yearWeight*y + kindWeight
}

// folds are the letters with marks that are compared as the letters
// without them: Go's standard library decomposes nothing, so the common
// Latin ones are listed, macrons among them, as romanised names use them.
var folds = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'ç': "c", 'ć': "c", 'č': "c", 'ď': "d", 'đ': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i", 'ı': "i",
	'ł': "l", 'ñ': "n", 'ń': "n", 'ň': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ő': "o",
	'ř': "r", 'ś': "s", 'š': "s", 'ş': "s", 'ß': "ss", 'ť': "t", 'ţ': "t",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ů': "u", 'ű': "u",
	'ý': "y", 'ÿ': "y", 'ź': "z", 'ż': "z", 'ž': "z",
	'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d",
}

// normalise is a title as it is compared: in lower case, its common Latin
// marks folded, "&" read as "and", apostrophes dropped, every other
// character that is not a letter or a digit a space, and a leading "the",
// "a" or "an" left out. Letters of every script are kept.
func normalise(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case folds[r] != "":
			b.WriteString(folds[r])
		case r == '\'' || r == '’' || r == 'ʼ':
		case r == '&':
			b.WriteString(" and ")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) > 1 && (words[0] == "the" || words[0] == "a" || words[0] == "an") {
		words = words[1:]
	}
	return strings.Join(words, " ")
}

// similarity is 1 less the edit distance between two normalised titles,
// in characters, over the longer one's length: 1 for the same, 0 when
// either is empty.
func similarity(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}
