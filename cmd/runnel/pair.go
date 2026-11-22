// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gureetk/runnel/internal/config"
)

const pairUsage = `Usage: runnel pair approve <code> --user <name>

Signs in the device that shows the code, as the user named, on the
running server. It prints the device's name before it approves.
`

func pairCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "approve" {
		fmt.Fprint(stderr, pairUsage)
		return 2
	}
	fs := flag.NewFlagSet("runnel pair approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "the user the device is signed in as")
	codes, err := parseArgs(fs, args[1:])
	if err != nil || len(codes) != 1 || *user == "" {
		fmt.Fprint(stderr, pairUsage)
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := pairApprove(ctx, client, codes[0], *user, stdout); err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	return 0
}

// pairApprove finds the user by name with GET /admin/users, shows what
// the code lets in with GET /auth/pair/{code}, and approves it with POST
// /auth/pair/approve (docs/04-api.md, sections 3.3 and 8.6).
func pairApprove(ctx context.Context, c *adminClient, code, name string, stdout io.Writer) error {
	var users struct {
		Users []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := c.do(ctx, "GET", "/admin/users", nil, &users); err != nil {
		return err
	}
	id := int64(0)
	for _, u := range users.Users {
		if strings.EqualFold(u.Name, name) {
			id, name = u.ID, u.Name
		}
	}
	if id == 0 {
		return fmt.Errorf("no user is called %q", name)
	}
	var p struct {
		Code   string `json:"code"`
		Device struct {
			Name   string `json:"name"`
			Client string `json:"client"`
		} `json:"device"`
	}
	if err := c.do(ctx, "GET", "/auth/pair/"+code, nil, &p); err != nil {
		return err
	}
	if err := c.do(ctx, "POST", "/auth/pair/approve", map[string]any{"code": p.Code, "user_id": id}, nil); err != nil {
		return err
	}
	device := p.Device.Name
	if device == "" {
		device = "the device"
	}
	if p.Device.Client != "" {
		device += " (" + p.Device.Client + ")"
	}
	fmt.Fprintf(stdout, "Approved %s: %s is signed in as %s when it next asks.\n", p.Code, device, name)
	return nil
}
