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
	"strings"
	"syscall"

	"github.com/gureetk/runnel/internal/config"
)

const userUsage = `Usage: runnel user add <name> [--admin]

Adds a user to the running server. The password is asked for twice on a
terminal, or read as one line from standard input. The first user is
always an admin.
`

func userCmd(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprint(stderr, userUsage)
		return 2
	}
	fs := flag.NewFlagSet("runnel user add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	admin := fs.Bool("admin", false, "make the user an admin")
	names, err := parseArgs(fs, args[1:])
	if err != nil || len(names) != 1 {
		fmt.Fprint(stderr, userUsage)
		return 2
	}

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
	password, err := readPassword(stdin, stderr, names[0])
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := userAdd(ctx, client, names[0], password, *admin, stdout); err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	return 0
}

// userAdd asks the server to add a user, with POST /admin/users.
func userAdd(ctx context.Context, c *adminClient, name, password string, admin bool, stdout io.Writer) error {
	var u struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		IsAdmin bool   `json:"is_admin"`
	}
	body := map[string]any{"name": name, "password": password, "is_admin": admin}
	if err := c.do(ctx, "POST", "/admin/users", body, &u); err != nil {
		return err
	}
	role := ""
	if u.IsAdmin {
		role = ", an admin"
	}
	fmt.Fprintf(stdout, "Added user %d, %s%s.\n", u.ID, u.Name, role)
	return nil
}

// readPassword asks twice, without echo, when stdin is a terminal.
// Otherwise it reads the first line of stdin.
func readPassword(stdin *os.File, prompt io.Writer, name string) (string, error) {
	if !isTerminal(stdin.Fd()) {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return "", errors.New("no password on standard input")
		}
		return line, nil
	}
	fmt.Fprintf(prompt, "Password for %s: ", name)
	first, err := readSecret(stdin.Fd())
	fmt.Fprintln(prompt)
	if err != nil {
		return "", err
	}
	fmt.Fprint(prompt, "Again: ")
	second, err := readSecret(stdin.Fd())
	fmt.Fprintln(prompt)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the two passwords differ")
	}
	return first, nil
}
