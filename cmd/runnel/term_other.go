// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package main

import "errors"

// The server runs on Linux. Elsewhere a password is read from standard
// input, as a line.

func isTerminal(fd uintptr) bool { return false }

func readSecret(fd uintptr) (string, error) {
	return "", errors.New("reading a password from a terminal needs Linux; give it on standard input")
}
