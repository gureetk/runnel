// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const importUsage = `Usage: runnel import jellyfin users --url <address>
       runnel import jellyfin --url <address> [--user <name in Jellyfin>=<name here>]... [--path <from>=<to>]... [--write]

The one-time import from Jellyfin. users lists Jellyfin's users. Without
--write the import is a dry run: it writes nothing and reports what it
would do. --user maps a Jellyfin user to a person here; give it once for
each. --path rewrites the start of Jellyfin's paths, for a Jellyfin that
sees the files elsewhere, as in a container.

The API key, made in Jellyfin's dashboard, is asked for on a terminal, or
read as one line from standard input. The command waits for the import to
end and prints its report.
`

// pollEvery is how often the command asks how the import is going.
var pollEvery = time.Second

func importCmd(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "jellyfin" {
		fmt.Fprint(stderr, importUsage)
		return 2
	}
	args = args[1:]
	listUsers := len(args) > 0 && args[0] == "users"
	if listUsers {
		args = args[1:]
	}
	fs := flag.NewFlagSet("runnel import jellyfin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "Jellyfin's address, such as http://jellyfin.lan:8096")
	write := fs.Bool("write", false, "write what the import finds; without it, a dry run")
	users := map[string]string{}
	var paths []map[string]string
	pair := func(v string) (string, string, error) {
		a, b, ok := strings.Cut(v, "=")
		if !ok || a == "" || b == "" {
			return "", "", errors.New("give it as <a>=<b>")
		}
		return a, b, nil
	}
	fs.Func("user", "<name in Jellyfin>=<name here>; give it once for each", func(v string) error {
		a, b, err := pair(v)
		users[a] = b
		return err
	})
	fs.Func("path", "<from>=<to>, a path prefix to rewrite; give it once for each", func(v string) error {
		a, b, err := pair(v)
		paths = append(paths, map[string]string{"from": a, "to": b})
		return err
	})
	rest, err := parseArgs(fs, args)
	if err != nil || len(rest) > 0 || *url == "" || (listUsers && (len(users) > 0 || len(paths) > 0 || *write)) {
		fmt.Fprint(stderr, importUsage)
		return 2
	}
	key, err := readKey(stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	return subcommand(stderr, func(ctx context.Context, c *adminClient) error {
		if listUsers {
			return jellyfinUsers(ctx, c, *url, key, stdout)
		}
		return importJellyfin(ctx, c, map[string]any{"url": *url, "api_key": key, "users": users, "paths": paths,
			"dry_run": !*write}, stdout)
	})
}

// readKey asks for the key without echo on a terminal, or reads the first
// line of stdin.
func readKey(stdin *os.File, prompt io.Writer) (string, error) {
	if stdin == nil || !isTerminal(stdin.Fd()) {
		var r io.Reader = strings.NewReader("")
		if stdin != nil {
			r = stdin
		}
		line, err := bufio.NewReader(r).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if line = strings.TrimSpace(line); line == "" {
			return "", errors.New("no Jellyfin API key on standard input")
		}
		return line, nil
	}
	fmt.Fprint(prompt, "Jellyfin API key: ")
	key, err := readSecret(stdin.Fd())
	fmt.Fprintln(prompt)
	return strings.TrimSpace(key), err
}

// jellyfinUsers prints Jellyfin's users, from POST /admin/import/jellyfin/users.
func jellyfinUsers(ctx context.Context, c *adminClient, url, key string, stdout io.Writer) error {
	var body struct {
		Version string `json:"version"`
		Users   []struct {
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := c.do(ctx, "POST", "/admin/import/jellyfin/users", map[string]string{"url": url, "api_key": key}, &body); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Jellyfin %s has %d users:\n", body.Version, len(body.Users))
	for _, u := range body.Users {
		fmt.Fprintln(stdout, "  "+u.Name)
	}
	return nil
}

// importReport is the report of GET /admin/import/jellyfin.
type importReport struct {
	State       string         `json:"state"`
	DryRun      bool           `json:"dry_run"`
	Version     string         `json:"version"`
	Joined      int            `json:"joined"`
	NotJoined   int            `json:"not_joined"`
	Series      int            `json:"series_joined"`
	SeriesNot   int            `json:"series_not_joined"`
	Matches     int            `json:"matches"`
	States      map[string]int `json:"states"`
	Missing     []struct{ Type, Path, Reason string }
	MissingMore int     `json:"missing_more"`
	Error       *string `json:"error"`
}

// importJellyfin starts the import with POST /admin/import/jellyfin, waits
// for it to end, and prints its report.
func importJellyfin(ctx context.Context, c *adminClient, body map[string]any, stdout io.Writer) error {
	if err := c.do(ctx, "POST", "/admin/import/jellyfin", body, nil); err != nil {
		return err
	}
	var rep importReport
	for {
		if err := c.do(ctx, "GET", "/admin/import/jellyfin", nil, &rep); err != nil {
			return err
		}
		if rep.State != "running" {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("stopped waiting; the import goes on in the server, and runnel import jellyfin does not show it")
		case <-time.After(pollEvery):
		}
	}
	if rep.State == "failed" {
		msg := "unknown"
		if rep.Error != nil {
			msg = *rep.Error
		}
		return errors.New("the import failed: " + msg)
	}
	if rep.DryRun {
		fmt.Fprintf(stdout, "A dry run against Jellyfin %s: nothing was written. Run it again with --write to import.\n", rep.Version)
	} else {
		fmt.Fprintf(stdout, "Imported from Jellyfin %s.\n", rep.Version)
	}
	share := "no films or episodes"
	if all := rep.Joined + rep.NotJoined; all > 0 {
		share = fmt.Sprintf("%.1f%%", 100*float64(rep.Joined)/float64(all))
	}
	fmt.Fprintf(stdout, "Films and episodes: %d joined, %d not (%s joined).\n", rep.Joined, rep.NotJoined, share)
	fmt.Fprintf(stdout, "Series: %d joined, %d not.\n", rep.Series, rep.SeriesNot)
	fmt.Fprintf(stdout, "Provider ids for %d films and series.\n", rep.Matches)
	names := make([]string, 0, len(rep.States))
	for n := range rep.States {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(stdout, "Play states for %s: %d.\n", n, rep.States[n])
	}
	if len(rep.Missing) > 0 {
		fmt.Fprintf(stdout, "Not joined (%d):\n", len(rep.Missing)+rep.MissingMore)
		for _, m := range rep.Missing {
			fmt.Fprintf(stdout, "  %s\t%s\t%s\n", m.Type, m.Path, m.Reason)
		}
		if rep.MissingMore > 0 {
			fmt.Fprintf(stdout, "  and %d more: GET /api/v1/admin/import/jellyfin?full=1 lists every one.\n", rep.MissingMore)
		}
	}
	return nil
}
