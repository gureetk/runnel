// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gureetk/runnel/internal/config"
)

const libraryUsage = `Usage: runnel library add --name <name> --kind movies|shows [--profile anime] --root <folder>...

Adds a library to the running server, with one or more folders (--root,
given once for each). Its first scan starts at once; the server's log
says when it is done.
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

func libraryCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprint(stderr, libraryUsage)
		return 2
	}
	fs := flag.NewFlagSet("runnel library add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "the library's name")
	kind := fs.String("kind", "", "movies or shows")
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
