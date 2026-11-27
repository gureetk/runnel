// SPDX-License-Identifier: GPL-3.0-or-later

package parse

import (
	"path"
	"strings"
)

// Grouping folders (docs/06-library-and-metadata.md, section 4.9; D49 as
// D52 amends it).

// folder is a folder below a root, as the paths of its videos show it.
type folder struct {
	name  string
	video bool // it holds a video directly
	kids  map[string]*folder
	// grouping is whether it is a grouping folder, once worked out.
	grouping *bool
}

func (f *folder) kid(name string) *folder {
	if f.kids == nil {
		f.kids = map[string]*folder{}
	}
	k := f.kids[name]
	if k == nil {
		k = &folder{name: name}
		f.kids[name] = k
	}
	return k
}

// hasYear reports whether a folder's name gives a year: "Name (year)".
func hasYear(dir string) bool {
	_, y := name(dir)
	return y != 0
}

// holdsSeason reports whether a folder holds a season folder of its own.
func (f *folder) holdsSeason() bool {
	for n := range f.kids {
		if _, ok := SeasonFolder(n); ok {
			return true
		}
	}
	return false
}

// isGrouping reports whether f is a grouping folder: a folder without a
// year, that holds no video and no season folder of its own, and each of
// whose folders has a year, holds a season folder, or is a grouping folder
// itself.
func (f *folder) isGrouping() bool {
	if f.grouping != nil {
		return *f.grouping
	}
	g := !hasYear(f.name) && !f.video && len(f.kids) > 0 && !f.holdsSeason()
	if g {
		for n, k := range f.kids {
			if !hasYear(n) && !k.holdsSeason() && !k.isGrouping() {
				g = false
				break
			}
		}
	}
	f.grouping = &g
	return g
}

// Groups reads the paths of a root's videos, below the root, and gives for
// each one the grouping folders above its title's folder, joined with /:
// "" for a title at the top of the root, "films" for one in a grouping
// folder, "anime/films" for one in two. A grouping folder makes no item:
// the folders in it are read as if they were at the top of the root.
// Folders that hold no video, nor any folder that does, are not seen.
func Groups(rels []string) []string {
	top := &folder{}
	for _, rel := range rels {
		f := top
		dirs := strings.Split(path.Dir(rel), "/")
		if dirs[0] == "." {
			top.video = true
			continue
		}
		for _, d := range dirs {
			f = f.kid(d)
		}
		f.video = true
	}
	out := make([]string, len(rels))
	for i, rel := range rels {
		dirs := strings.Split(path.Dir(rel), "/")
		f, n := top, 0
		for n < len(dirs) && dirs[0] != "." {
			k := f.kids[dirs[n]]
			if k == nil || !k.isGrouping() {
				break
			}
			f, n = k, n+1
		}
		out[i] = strings.Join(dirs[:n], "/")
	}
	return out
}
