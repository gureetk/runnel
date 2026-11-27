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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gureetk/runnel/internal/config"
)

const libraryUsage = `Usage: runnel library add --name <name> --kind movies|shows|mixed [--profile anime] --root <folder>...
       runnel library list
       runnel library change <id> [--name <name>] [--profile default|anime] [--kind mixed] [--yes]
       runnel library remove <id> [--yes]

add adds a library to the running server, with one or more folders
(--root, given once for each). Its first scan starts at once; the
server's log says when it is done.

list prints the libraries, their folders, and each folder's last scan.

change renames a library, sets its profile, or makes a films or a shows
library mixed, which cannot be undone: what reads the same keeps its
watch state, and a film read now as a series, or a series read now as a
film, is a new item without it. Before a change of kind it asks on a
terminal; elsewhere it needs --yes.

remove removes a library, with its items and everyone's watch state for
them. It asks first on a terminal; elsewhere it needs --yes.
`

const scanUsage = `Usage: runnel scan [--library <id>]

Asks the running server to scan one library, or all of them. It does not
wait: the server's log says when each folder is done.
`

// subcommand runs fn with a client for the running server, and turns its
// error into a message and an exit status.
func subcommand(stderr io.Writer, fn func(ctx context.Context, c *adminClient) error) int {
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	client, err := newAdminClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := fn(ctx, client); err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	return 0
}

func libraryCmd(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, libraryUsage)
		return 2
	}
	switch args[0] {
	case "add":
	case "list":
		if len(args) != 1 {
			fmt.Fprint(stderr, libraryUsage)
			return 2
		}
		return subcommand(stderr, func(ctx context.Context, c *adminClient) error { return libraryList(ctx, c, stdout) })
	case "remove":
		fs := flag.NewFlagSet("runnel library remove", flag.ContinueOnError)
		fs.SetOutput(stderr)
		yes := fs.Bool("yes", false, "remove without asking")
		rest, err := parseArgs(fs, args[1:])
		var id int64
		if err == nil && len(rest) == 1 {
			id, err = strconv.ParseInt(rest[0], 10, 64)
		}
		if err != nil || len(rest) != 1 || id <= 0 {
			fmt.Fprint(stderr, libraryUsage)
			return 2
		}
		return subcommand(stderr, func(ctx context.Context, c *adminClient) error {
			return libraryRemove(ctx, c, id, *yes, stdin, stdout, stderr)
		})
	case "change":
		fs := flag.NewFlagSet("runnel library change", flag.ContinueOnError)
		fs.SetOutput(stderr)
		var ch libraryChanges
		fs.Func("name", "a new name", func(v string) error { ch.name = &v; return nil })
		fs.Func("profile", "default, or anime for a library of anime series", func(v string) error { ch.profile = &v; return nil })
		fs.Func("kind", "mixed, for a films or a shows library", func(v string) error { ch.kind = &v; return nil })
		yes := fs.Bool("yes", false, "change the kind without asking")
		rest, err := parseArgs(fs, args[1:])
		var id int64
		if err == nil && len(rest) == 1 {
			id, err = strconv.ParseInt(rest[0], 10, 64)
		}
		if err != nil || len(rest) != 1 || id <= 0 || (ch.name == nil && ch.profile == nil && ch.kind == nil) {
			fmt.Fprint(stderr, libraryUsage)
			return 2
		}
		return subcommand(stderr, func(ctx context.Context, c *adminClient) error {
			return libraryChange(ctx, c, id, ch, *yes, stdin, stdout, stderr)
		})
	default:
		fmt.Fprint(stderr, libraryUsage)
		return 2
	}
	fs := flag.NewFlagSet("runnel library add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "the library's name")
	kind := fs.String("kind", "", "movies, shows or mixed")
	profile := fs.String("profile", "", "anime, for a library of anime series")
	var roots []string
	fs.Func("root", "a folder the library scans; give it once for each", func(v string) error {
		abs, err := filepath.Abs(v)
		if err != nil {
			return err
		}
		roots = append(roots, abs)
		return nil
	})
	rest, err := parseArgs(fs, args[1:])
	if err != nil || len(rest) > 0 || *name == "" || *kind == "" || len(roots) == 0 {
		fmt.Fprint(stderr, libraryUsage)
		return 2
	}
	return subcommand(stderr, func(ctx context.Context, c *adminClient) error {
		return libraryAdd(ctx, c, *name, *kind, *profile, roots, stdout)
	})
}

// libraryAdd asks the server to add a library, with POST /admin/libraries.
func libraryAdd(ctx context.Context, c *adminClient, name, kind, profile string, roots []string, stdout io.Writer) error {
	var lib struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Roots []struct {
			ID   int64  `json:"id"`
			Path string `json:"path"`
		} `json:"roots"`
	}
	body := map[string]any{"name": name, "kind": kind, "profile": profile, "roots": roots}
	if err := c.do(ctx, "POST", "/admin/libraries", body, &lib); err != nil {
		return err
	}
	var folders []string
	for _, r := range lib.Roots {
		folders = append(folders, fmt.Sprintf("%d %s", r.ID, r.Path))
	}
	fmt.Fprintf(stdout, "Added library %d, %s (%s), with folders: %s.\nIts first scan has started.\n",
		lib.ID, lib.Name, lib.Kind, strings.Join(folders, ", "))
	return nil
}

// adminLibrary is a library as GET /admin/libraries gives it.
type adminLibrary struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
	Items   int    `json:"items"`
	Scan    struct {
		State string `json:"state"`
		Done  int    `json:"done"`
		Total int    `json:"total"`
	} `json:"scan"`
	Roots []struct {
		ID        int64  `json:"id"`
		Path      string `json:"path"`
		Available bool   `json:"available"`
		LastScan  *struct {
			At      int64   `json:"at"`
			MS      int64   `json:"ms"`
			Added   int     `json:"added"`
			Changed int     `json:"changed"`
			Removed int     `json:"removed"`
			Error   *string `json:"error"`
		} `json:"last_scan"`
	} `json:"roots"`
}

func adminLibraries(ctx context.Context, c *adminClient) ([]adminLibrary, error) {
	var body struct {
		Libraries []adminLibrary `json:"libraries"`
	}
	err := c.do(ctx, "GET", "/admin/libraries", nil, &body)
	return body.Libraries, err
}

// libraryList prints the libraries, with GET /admin/libraries.
func libraryList(ctx context.Context, c *adminClient, stdout io.Writer) error {
	libs, err := adminLibraries(ctx, c)
	if err != nil {
		return err
	}
	if len(libs) == 0 {
		fmt.Fprintln(stdout, "There is no library yet. Add one with runnel library add.")
		return nil
	}
	for _, l := range libs {
		kind := l.Kind
		if l.Profile != "default" {
			kind += ", " + l.Profile
		}
		scan := l.Scan.State
		if scan == "probing" {
			scan = fmt.Sprintf("probing, %d of %d files", l.Scan.Done, l.Scan.Total)
		}
		fmt.Fprintf(stdout, "%d  %s (%s): %d films and series; scan: %s\n", l.ID, l.Name, kind, l.Items, scan)
		for _, r := range l.Roots {
			line := fmt.Sprintf("   folder %d  %s", r.ID, r.Path)
			if !r.Available {
				line += "  (away)"
			}
			if ls := r.LastScan; ls == nil {
				line += "  not scanned yet"
			} else {
				line += fmt.Sprintf("  scanned %s in %d ms: %d added, %d changed, %d removed",
					time.UnixMilli(ls.At).Format("2006-01-02 15:04"), ls.MS, ls.Added, ls.Changed, ls.Removed)
				if ls.Error != nil {
					line += "; " + *ls.Error
				}
			}
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

// libraryRemove removes a library, with DELETE /admin/libraries/{id},
// after asking on a terminal.
func libraryRemove(ctx context.Context, c *adminClient, id int64, yes bool, stdin *os.File, stdout, prompt io.Writer) error {
	libs, err := adminLibraries(ctx, c)
	if err != nil {
		return err
	}
	var lib *adminLibrary
	for i := range libs {
		if libs[i].ID == id {
			lib = &libs[i]
		}
	}
	if lib == nil {
		return fmt.Errorf("there is no library %d", id)
	}
	if !yes {
		if stdin == nil || !isTerminal(stdin.Fd()) {
			return errors.New("library remove asks before it removes anything; give --yes to remove without asking")
		}
		fmt.Fprintf(prompt, "Remove library %d, %s, with its %d films and series and everyone's watch state for them? Type yes to go on: ",
			lib.ID, lib.Name, lib.Items)
		answer, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.TrimSpace(answer) != "yes" {
			fmt.Fprintln(stdout, "Nothing was removed.")
			return nil
		}
	}
	if err := c.do(ctx, "DELETE", "/admin/libraries/"+strconv.FormatInt(id, 10), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed library %d, %s.\n", lib.ID, lib.Name)
	return nil
}

// libraryChanges are the changes asked of a library; nil is no change.
type libraryChanges struct {
	name, profile, kind *string
}

// libraryChange changes a library, with PATCH /admin/libraries/{id}.
// Before a change of kind, which cannot be undone, it asks on a terminal.
func libraryChange(ctx context.Context, c *adminClient, id int64, ch libraryChanges, yes bool, stdin *os.File, stdout, prompt io.Writer) error {
	libs, err := adminLibraries(ctx, c)
	if err != nil {
		return err
	}
	var lib *adminLibrary
	for i := range libs {
		if libs[i].ID == id {
			lib = &libs[i]
		}
	}
	if lib == nil {
		return fmt.Errorf("there is no library %d", id)
	}
	body := map[string]any{}
	if ch.name != nil {
		body["name"] = *ch.name
	}
	if ch.profile != nil {
		body["profile"] = *ch.profile
	}
	newKind := ch.kind != nil && *ch.kind != lib.Kind
	if ch.kind != nil {
		body["kind"] = *ch.kind
	}
	if newKind && !yes {
		if stdin == nil || !isTerminal(stdin.Fd()) {
			return errors.New("library change asks before a change of kind, which cannot be undone; give --yes to change it without asking")
		}
		fmt.Fprintf(prompt, "Make library %d, %s, %s? This cannot be undone. What reads the same keeps its watch state; "+
			"a film read now as a series, or a series read now as a film, is a new item without it. Type yes to go on: ",
			lib.ID, lib.Name, *ch.kind)
		answer, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if strings.TrimSpace(answer) != "yes" {
			fmt.Fprintln(stdout, "Nothing was changed.")
			return nil
		}
	}
	var changed struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Profile string `json:"profile"`
	}
	if err := c.do(ctx, "PATCH", "/admin/libraries/"+strconv.FormatInt(id, 10), body, &changed); err != nil {
		return err
	}
	kind := changed.Kind
	if changed.Profile != "default" {
		kind += ", " + changed.Profile
	}
	fmt.Fprintf(stdout, "Changed library %d, %s (%s).\n", changed.ID, changed.Name, kind)
	if newKind {
		fmt.Fprintln(stdout, "Its folders are being read again; the server's log says when its items are built.")
	}
	return nil
}

func scanCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("runnel scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	library := fs.Int64("library", 0, "the library to scan; all when left out")
	rest, err := parseArgs(fs, args)
	if err != nil || len(rest) > 0 || *library < 0 {
		fmt.Fprint(stderr, scanUsage)
		return 2
	}
	return subcommand(stderr, func(ctx context.Context, c *adminClient) error {
		return scanRequest(ctx, c, *library, stdout)
	})
}

// scanRequest asks the server to scan, with POST /admin/scan.
func scanRequest(ctx context.Context, c *adminClient, library int64, stdout io.Writer) error {
	body := map[string]any{}
	if library > 0 {
		body["library_id"] = library
	}
	var e *apiError
	if err := c.do(ctx, "POST", "/admin/scan", body, nil); errors.As(err, &e) && e.Code == "not_found" {
		return fmt.Errorf("there is no library %d", library)
	} else if err != nil {
		return err
	}
	if library > 0 {
		fmt.Fprintf(stdout, "Asked the server to scan library %d.\n", library)
	} else {
		fmt.Fprintln(stdout, "Asked the server to scan every library.")
	}
	return nil
}
