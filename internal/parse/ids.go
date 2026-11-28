// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"regexp"
	"strings"
)

// idTag is a provider's id in a name, as Jellyfin reads it: in square
// brackets, the provider's attribute, a dash or an equals sign, and the
// value up to the closing bracket, without case
// (docs/06-library-and-metadata.md, section 4.5).
var idTag = regexp.MustCompile(`(?i)\[(imdbid|tmdbid|tvdbid)[-=]\s*([^\]]*?)\s*\]`)

// The forms of the values: TMDB's and TheTVDB's ids are numbers, IMDb's
// are tt and a number.
var (
	numericID = regexp.MustCompile(`^[0-9]{1,10}$`)
	imdbID    = regexp.MustCompile(`^tt[0-9]{1,10}$`)
)

// IDs reads the providers' ids that a folder's or a file's name carries:
// [imdbid-tt1234567], [tmdbid-12345] and [tvdbid-12345], by the provider
// names of docs/03-data-model.md (imdb, tmdb, tvdb). A value of the wrong
// form is left out, and where a provider is named twice the first counts.
// It is nil when the name carries none.
func IDs(name string) map[string]string {
	var out map[string]string
	for _, m := range idTag.FindAllStringSubmatch(name, -1) {
		provider := strings.TrimSuffix(strings.ToLower(m[1]), "id")
		value := m[2]
		if provider == "imdb" {
			value = strings.ToLower(value)
			if !imdbID.MatchString(value) {
				continue
			}
		} else if !numericID.MatchString(value) {
			continue
		}
		if _, ok := out[provider]; ok {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[provider] = value
	}
	return out
}
