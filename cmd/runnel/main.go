// SPDX-License-Identifier: GPL-3.0-or-later

// Command runnel is the Runnel media server, and the tool that manages it.
//
// What it does so far is the work of the stages of docs/01-roadmap.md, as
// far as they have been built.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

const usage = `Usage: runnel <command>

Commands:
  serve                        Run the server.
  user add <name> [--admin]    Add a user to the running server.
  user passwd <name>           Give a user a new password.
  user list                    Print the users and their devices.
  setup-code                   Print the setup code the first run asks for.
  pair approve <code> --user <name>
                               Sign in the device that shows the code.
  library add --name <name> --kind movies|shows [--profile anime] --root <folder>...
                               Add a library to the running server.
  library list                 Print the libraries and their folders' last scans.
  library remove <id> [--yes]  Remove a library, with its items and watch state.
  scan [--library <id>]        Ask the running server to scan.
  report [--library <id>] [--class <class>|all]
                               Print how every file's name parsed.
  version                      Print the version.

Settings are environment variables that start with RUNNEL_. They are listed
in docs/02-architecture.md, section 6.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run runs one command and returns the process's exit status.
func run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:], stderr)
	case "user":
		return userCmd(args[1:], stdin, stdout, stderr)
	case "library":
		return libraryCmd(args[1:], stdin, stdout, stderr)
	case "pair":
		return pairCmd(args[1:], stdout, stderr)
	case "setup-code":
		return setupCodeCmd(args[1:], stdout, stderr)
	case "scan":
		return scanCmd(args[1:], stdout, stderr)
	case "report":
		return reportCmd(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "runnel: unknown command %q\n\n%s", args[0], usage)
	return 2
}
