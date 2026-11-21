// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"regexp"
	"strconv"
	"strings"
)

// Versions and parts (docs/06-library-and-metadata.md, section 4.4).

// part is Jellyfin's rule for files that split a video: at the end of the
// name, after a space, a dot, a dash or an underscore, or right after a
// closing bracket, one of cd, dvd, part, pt, disc and disk, perhaps in
// brackets, then a number or a letter a to d.
var part = regexp.MustCompile(`(?i)^(?:(.*?)[ ._-]+|(.*?[\])}]))[(\[]?(?:cd|dvd|part|pt|dis[ck])[ ._-]*(\d+|[a-d])[)\]]?$`)

// Part reads a part token at the end of a base name. It returns the part's
// number, a letter counting as 1 to 4, and the name before the token; or 0
// and the whole name when there is none. A part counts only where another
// file of the same item has the same name before its token: alone, "Part 2"
// is as likely a title's.
func Part(base string) (int, string) {
	m := part.FindStringSubmatch(base)
	if m == nil {
		return 0, base
	}
	before := m[1] + m[2]
	if n, err := strconv.Atoi(m[3]); err == nil {
		return n, before
	}
	return int(strings.ToLower(m[3])[0]-'a') + 1, before
}

// pieces splits a name into runs of letters and digits and runs of
// everything else, in order.
func pieces(s string) []string {
	var out []string
	last := 0
	for _, loc := range wordRun.FindAllStringIndex(s, -1) {
		if loc[0] > last {
			out = append(out, s[last:loc[0]])
		}
		out = append(out, s[loc[0]:loc[1]])
		last = loc[1]
	}
	if last < len(s) {
		out = append(out, s[last:])
	}
	return out
}

// differences returns what is left of each name once the beginning and the
// end that all of them share are taken off, cut where letters and digits
// meet other characters and trimmed of those: for "Film - 1080p" and
// "Film - 720p", 1080p and 720p.
func differences(names []string) []string {
	split := make([][]string, len(names))
	shortest := -1
	for i, n := range names {
		split[i] = pieces(n)
		if shortest < 0 || len(split[i]) < shortest {
			shortest = len(split[i])
		}
	}
	same := func(at func(p []string) string) bool {
		for _, p := range split[1:] {
			if !strings.EqualFold(at(p), at(split[0])) {
				return false
			}
		}
		return true
	}
	lead := 0
	for lead < shortest && same(func(p []string) string { return p[lead] }) {
		lead++
	}
	tail := 0
	for lead+tail < shortest && same(func(p []string) string { return p[len(p)-1-tail] }) {
		tail++
	}
	out := make([]string, len(names))
	for i, p := range split {
		out[i] = strings.TrimFunc(strings.Join(p[lead:len(p)-tail], ""), func(r rune) bool {
			return !wordRun.MatchString(string(r))
		})
	}
	return out
}

// numberOnly is a difference that is a number and nothing else, perhaps
// marked as an episode's: such names are more likely two episodes than two
// versions of one.
var numberOnly = regexp.MustCompile(`(?i)^(?:e|ep|episode)?[ ._-]?\d{1,4}(?:v\d+)?$|^\d{1,4}\.\d{1,2}$`)

// Distinct reports whether the base names of the files that make one item
// tell them apart, as versions by a label or as parts by a part number. Two
// names that differ in nothing, or only by a number, do not: those files
// are more likely episodes that parsed to the same number, or copies.
func Distinct(bases []string) bool {
	if len(bases) < 2 {
		return true
	}
	parts := make([]int, len(bases))
	rests := make([]string, len(bases))
	for i, b := range bases {
		parts[i], rests[i] = Part(b)
	}
	for i := range bases {
		if parts[i] == 0 {
			continue
		}
		shared := false
		for j := range bases {
			if j != i && parts[j] != 0 && parts[j] != parts[i] && strings.EqualFold(rests[i], rests[j]) {
				shared = true
			}
		}
		if !shared {
			parts[i], rests[i] = 0, bases[i]
		}
	}
	type key struct {
		part int
		diff string
	}
	seen := map[key]bool{}
	for i, d := range differences(rests) {
		if numberOnly.MatchString(d) {
			return false
		}
		k := key{parts[i], strings.ToLower(d)}
		if seen[k] {
			return false
		}
		seen[k] = true
	}
	return true
}
