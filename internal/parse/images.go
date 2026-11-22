// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"path"
	"strings"
)

// Local images (docs/06-library-and-metadata.md, section 4.8).

// imageExtensions are the formats of local images.
var imageExtensions = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}

// imageNames are the names Jellyfin's documentation gives each kind of
// image, in the order they are preferred when a folder has several.
var imageNames = []struct{ name, kind string }{
	{"poster", "poster"}, {"cover", "poster"}, {"folder", "poster"}, {"default", "poster"}, {"movie", "poster"}, {"show", "poster"},
	{"backdrop", "backdrop"}, {"fanart", "backdrop"}, {"background", "backdrop"}, {"art", "backdrop"},
	{"logo", "logo"}, {"clearlogo", "logo"},
	{"thumb", "thumb"},
}

// LocalImage reads the name of an image file. The name is one of the
// names of a kind, or ends with a dash and one: "poster.jpg" and
// "Paper Comets (2019)-poster.jpg" are posters. prefix is what precedes
// the dash, "" when there is none. rank orders the images of one kind
// that a folder holds: the lowest is the one kept.
func LocalImage(name string) (kind, prefix string, rank int, ok bool) {
	ext := path.Ext(name)
	if !imageExtensions[strings.ToLower(ext)] {
		return "", "", 0, false
	}
	stem := strings.TrimSuffix(name, ext)
	word := strings.ToLower(stem)
	if i := strings.LastIndexByte(stem, '-'); i >= 0 {
		prefix, word = stem[:i], strings.ToLower(stem[i+1:])
	}
	for r, n := range imageNames {
		if n.name == word {
			return n.kind, prefix, r, true
		}
	}
	return "", "", 0, false
}

// IsImage reports whether a file's extension is one of an image.
func IsImage(name string) bool { return imageExtensions[strings.ToLower(path.Ext(name))] }
