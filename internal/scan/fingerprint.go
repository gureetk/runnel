// SPDX-License-Identifier: GPL-3.0-or-later

// Package scan walks a library's roots, compares what it finds with the
// database, and computes the fingerprints that let a file keep its id
// through a rename (docs/06-library-and-metadata.md, section 2).
package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
)

// edge is how much of each end of a file the fingerprint reads.
const edge = 64 << 10

// Fingerprint is a file's size and a hash of its first and last 64 KiB:
// two small reads (docs/03-data-model.md, section 4). It is written as
// "<size>-<32 hex digits>", which is safe in a file name. For a file
// shorter than 64 KiB both reads cover the whole file.
func Fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := fi.Size()

	h := sha256.New()
	n := min(size, edge)
	if _, err := io.CopyN(h, io.NewSectionReader(f, 0, n), n); err != nil {
		return "", fmt.Errorf("fingerprint %s: %w", path, err)
	}
	if _, err := io.CopyN(h, io.NewSectionReader(f, size-n, n), n); err != nil {
		return "", fmt.Errorf("fingerprint %s: %w", path, err)
	}
	return strconv.FormatInt(size, 10) + "-" + hex.EncodeToString(h.Sum(nil)[:16]), nil
}
