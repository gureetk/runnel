// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

const reportUsage = `Usage: runnel report [--library <id>] [--class explicit|anchored|dash|guess|none|all]

Prints how the running server parsed every file's name: the count in each
class for each library, then one line for each file of the classes asked
for (guess and none when left out), then the files ffprobe could not read.
Lines are separated by tabs: class, file id, what the name gave, path.
`

func reportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runnel report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	library := fs.Int64("library", 0, "one library; all when left out")
	class := fs.String("class", "", "the class to list, or all")
	rest, err := parseArgs(fs, args)
	if err != nil || len(rest) > 0 {
		fmt.Fprint(stderr, reportUsage)
		return 2
	}
	return subcommand(stderr, func(ctx context.Context, c *adminClient) error {
		return report(ctx, c, *library, *class, stdout)
	})
}

type reportBody struct {
	Libraries []struct {
		ID         int64          `json:"id"`
		Name       string         `json:"name"`
		Kind       string         `json:"kind"`
		Files      int            `json:"files"`
		Counts     map[string]int `json:"counts"`
		Unreadable int            `json:"unreadable"`
	} `json:"libraries"`
	Files []struct {
		FileID     int64  `json:"file_id"`
		LibraryID  int64  `json:"library_id"`
		Path       string `json:"path"`
		Class      string `json:"class"`
		Title      string `json:"title"`
		Year       *int   `json:"year"`
		Season     *int   `json:"season"`
		Episode    *int   `json:"episode"`
		EpisodeEnd *int   `json:"episode_end"`
		Note       string `json:"note"`
	} `json:"files"`
	Unreadable []struct {
		FileID int64  `json:"file_id"`
		Path   string `json:"path"`
		Error  string `json:"error"`
	} `json:"unreadable"`
}

// report reads GET /admin/parse-report and prints it.
func report(ctx context.Context, c *adminClient, library int64, class string, stdout io.Writer) error {
	q := url.Values{}
	if library > 0 {
		q.Set("library", strconv.FormatInt(library, 10))
	}
	if class != "" {
		q.Set("class", class)
	}
	var b reportBody
	if err := c.do(ctx, "GET", "/admin/parse-report?"+q.Encode(), nil, &b); err != nil {
		return err
	}
	for _, l := range b.Libraries {
		fmt.Fprintf(stdout, "Library %d, %s (%s): %d files. explicit %d, anchored %d, dash %d, guess %d, none %d. ffprobe could not read %d.\n",
			l.ID, l.Name, l.Kind, l.Files, l.Counts["explicit"], l.Counts["anchored"], l.Counts["dash"],
			l.Counts["guess"], l.Counts["none"], l.Unreadable)
	}
	if len(b.Files) > 0 {
		fmt.Fprintln(stdout)
	}
	for _, f := range b.Files {
		var gave strings.Builder
		switch {
		case f.Class == "none":
			gave.WriteString("no item")
		default:
			gave.WriteString(f.Title)
			if f.Year != nil {
				fmt.Fprintf(&gave, " (%d)", *f.Year)
			}
			if f.Season != nil && f.Episode != nil {
				fmt.Fprintf(&gave, " S%02dE%02d", *f.Season, *f.Episode)
				if f.EpisodeEnd != nil {
					fmt.Fprintf(&gave, "-E%02d", *f.EpisodeEnd)
				}
			}
		}
		line := fmt.Sprintf("%s\t%d\t%s\t%s", f.Class, f.FileID, gave.String(), f.Path)
		if f.Note != "" {
			line += "\t" + f.Note
		}
		fmt.Fprintln(stdout, line)
	}
	if len(b.Unreadable) > 0 {
		fmt.Fprintln(stdout, "\nCould not be read by ffprobe:")
		for _, u := range b.Unreadable {
			fmt.Fprintf(stdout, "unreadable\t%d\t%s\t%s\n", u.FileID, u.Error, u.Path)
		}
	}
	return nil
}
