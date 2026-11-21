// SPDX-License-Identifier: GPL-3.0-or-later

// Command runnel is the Runnel media server.
//
// Nothing is implemented yet. The stages in docs/01-roadmap.md say what is
// built, and in which order.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "runnel: nothing is implemented yet. See docs/01-roadmap.md.")
	os.Exit(2)
}
