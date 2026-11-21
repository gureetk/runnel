// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gureetk/runnel/internal/api"
	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/config"
)

// adminClient is how the subcommands change data: through the running
// server's API, with the local admin token that the server wrote into its
// data directory. They never open the database themselves
// (docs/02-architecture.md, section 6).
type adminClient struct {
	base  string // such as http://127.0.0.1:8610/api/v1
	token string
	http  *http.Client
}

// newAdminClient finds the server from the same RUNNEL_LISTEN and
// RUNNEL_DATA that the server reads.
func newAdminClient(cfg config.Config) (*adminClient, error) {
	token, err := auth.ReadAdminToken(cfg.DataDir)
	path := filepath.Join(cfg.DataDir, auth.AdminTokenFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("there is no %s: is the server running, with the same RUNNEL_DATA?", path)
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("cannot read %s: run this as the user the server runs as, for example with sudo -u runnel", path)
	case err != nil:
		return nil, err
	}
	return &adminClient{
		base:  "http://" + localAddress(cfg.Listen) + api.Prefix,
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// localAddress turns the address the server listens on into one to reach
// it from the same machine.
func localAddress(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// apiError is an error the server answered with (docs/04-api.md, section 9).
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string { return e.Message + " (" + e.Code + ")" }

// do sends body as JSON and decodes the answer into out, when out is not nil.
func (c *adminClient) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the server at %s: is runnel serve running? (%w)", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&e) != nil || e.Error.Code == "" {
			return fmt.Errorf("the server answered %s", resp.Status)
		}
		return &apiError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// parseArgs parses flags that may come before, between or after the
// positional arguments, which it returns.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
