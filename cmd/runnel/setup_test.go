// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/gureetk/runnel/internal/config"
)

func TestSetupCode(t *testing.T) {
	srv := startServer(t)
	ctx := context.Background()
	client, err := newAdminClient(config.Config{Listen: srv.addr, DataDir: srv.data})
	if err != nil {
		t.Fatal(err)
	}
	// The server printed the code when it started without people, and the
	// subcommand prints the same.
	logged := srv.waitFor(t, `setup_code=([2-9A-HJKMNP-Z]{4}-[2-9A-HJKMNP-Z]{4})`)
	var out bytes.Buffer
	if err := setupCode(ctx, client, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != regexp.MustCompile(`setup_code=(\S+)`).FindStringSubmatch(logged)[1] {
		t.Fatalf("printed %q, logged %q", got, logged)
	}
	if err := userAdd(ctx, client, "ada", "correct horse", false, &out); err != nil {
		t.Fatal(err)
	}
	if err := setupCode(ctx, client, &out); err == nil || !strings.Contains(err.Error(), "no setup code is needed") {
		t.Fatalf("after the first person: %v", err)
	}
}
