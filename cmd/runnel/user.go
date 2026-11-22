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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gureetk/runnel/internal/config"
)

const userUsage = `Usage: runnel user add <name> [--admin]
       runnel user passwd <name>
       runnel user list

add adds a user to the running server, and passwd gives a user a new
password. Both ask for the password twice on a terminal, or read it as
one line from standard input. The first user is always an admin. A new
password leaves the user's devices signed in.

list prints the users, with how many devices each has signed in and
when they were last active.
`

func userCmd(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, userUsage)
		return 2
	}
	switch args[0] {
	case "add", "passwd":
	case "list":
		if len(args) != 1 {
			fmt.Fprint(stderr, userUsage)
			return 2
		}
		return subcommand(stderr, func(ctx context.Context, c *adminClient) error { return userList(ctx, c, stdout) })
	default:
		fmt.Fprint(stderr, userUsage)
		return 2
	}
	verb := args[0]
	fs := flag.NewFlagSet("runnel user "+verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	admin := fs.Bool("admin", false, "make the user an admin")
	names, err := parseArgs(fs, args[1:])
	if err != nil || len(names) != 1 || (verb == "passwd" && *admin) {
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
	// A user who does not exist is said so before the password is asked
	// for.
	if verb == "passwd" {
		if _, err := userNamed(context.Background(), client, names[0]); err != nil {
			fmt.Fprintf(stderr, "runnel: %v\n", err)
			return 1
		}
	}
	password, err := readPassword(stdin, stderr, names[0])
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if verb == "passwd" {
		err = userPasswd(ctx, client, names[0], password, stdout)
	} else {
		err = userAdd(ctx, client, names[0], password, *admin, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	return 0
}

// adminUser is a user as GET /admin/users lists them.
type adminUser struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	IsAdmin      bool   `json:"is_admin"`
	HasPassword  bool   `json:"has_password"`
	Devices      int    `json:"devices"`
	LastActiveAt *int64 `json:"last_active_at"`
}

func adminUsers(ctx context.Context, c *adminClient) ([]adminUser, error) {
	var body struct {
		Users []adminUser `json:"users"`
	}
	err := c.do(ctx, "GET", "/admin/users", nil, &body)
	return body.Users, err
}

// userNamed finds a user by name, compared without case as the server
// compares names.
func userNamed(ctx context.Context, c *adminClient, name string) (adminUser, error) {
	users, err := adminUsers(ctx, c)
	if err != nil {
		return adminUser{}, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Name, name) {
			return u, nil
		}
	}
	return adminUser{}, fmt.Errorf("there is no user named %s", name)
}

// userPasswd gives a user a new password, with PATCH /admin/users/{id}.
func userPasswd(ctx context.Context, c *adminClient, name, password string, stdout io.Writer) error {
	u, err := userNamed(ctx, c, name)
	if err != nil {
		return err
	}
	if err := c.do(ctx, "PATCH", "/admin/users/"+strconv.FormatInt(u.ID, 10), map[string]any{"password": password}, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Set a new password for %s. Their devices stay signed in.\n", u.Name)
	return nil
}

// userList prints the users, with GET /admin/users.
func userList(ctx context.Context, c *adminClient, stdout io.Writer) error {
	users, err := adminUsers(ctx, c)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Fprintln(stdout, "There is no user yet. Add one with runnel user add, or with the setup code.")
		return nil
	}
	for _, u := range users {
		var facts []string
		if u.IsAdmin {
			facts = append(facts, "admin")
		}
		if u.HasPassword {
			facts = append(facts, "signs in with a password")
		} else {
			facts = append(facts, "signs in by pairing only")
		}
		devices := "no device signed in"
		switch {
		case u.Devices == 1:
			devices = "1 device"
		case u.Devices > 1:
			devices = strconv.Itoa(u.Devices) + " devices"
		}
		if u.LastActiveAt != nil {
			devices += ", last active " + time.UnixMilli(*u.LastActiveAt).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(stdout, "%d  %s: %s; %s\n", u.ID, u.Name, strings.Join(facts, ", "), devices)
	}
	return nil
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
