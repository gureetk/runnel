// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/gureetk/runnel/internal/config"
)

const setupCodeUsage = `Usage: runnel setup-code

Prints the setup code that the first run asks for, while the running
server has no person yet.
`

func setupCodeCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprint(stderr, setupCodeUsage)
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
	if err := setupCode(ctx, client, stdout); err != nil {
		fmt.Fprintf(stderr, "runnel: %v\n", err)
		return 1
	}
	return 0
}

// setupCode prints the setup code, from GET /admin/setup-code.
func setupCode(ctx context.Context, c *adminClient, stdout io.Writer) error {
	var out struct {
		Code string `json:"code"`
	}
	err := c.do(ctx, "GET", "/admin/setup-code", nil, &out)
	var e *apiError
	if errors.As(err, &e) && e.Code == "setup_done" {
		return errors.New("the server has its first person already: no setup code is needed")
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, out.Code)
	return nil
}
