// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "testing"

// The module is empty so far. This keeps `go test ./...` meaningful in CI
// from the first commit.
func TestVersionHasADefault(t *testing.T) {
	if version == "" {
		t.Fatal("version must never be empty")
	}
}
