// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func write(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, 300<<10)
	rand.Read(body)

	a, err := Fingerprint(write(t, dir, "a.mkv", body))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^307200-[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("fingerprint %q has the wrong shape", a)
	}

	// The same bytes under another name: the same fingerprint, which is
	// what lets a renamed file keep its id.
	if b, _ := Fingerprint(write(t, dir, "renamed.mkv", body)); b != a {
		t.Fatal("a copy has another fingerprint")
	}

	// A change in the middle is not seen: only the ends are read.
	middle := bytes.Clone(body)
	middle[150<<10] ^= 1
	if m, _ := Fingerprint(write(t, dir, "middle.mkv", middle)); m != a {
		t.Fatal("a change in the middle changed the fingerprint")
	}

	// A change at either end is.
	for _, at := range []int{0, len(body) - 1} {
		changed := bytes.Clone(body)
		changed[at] ^= 1
		if c, _ := Fingerprint(write(t, dir, "changed.mkv", changed)); c == a {
			t.Fatalf("a change at byte %d did not change the fingerprint", at)
		}
	}

	// Another size is another fingerprint, even with the same ends.
	longer := append(bytes.Clone(body[:150<<10]), body...)
	if l, _ := Fingerprint(write(t, dir, "longer.mkv", longer)); l == a {
		t.Fatal("a longer file has the same fingerprint")
	}
}

func TestFingerprintSmallFiles(t *testing.T) {
	dir := t.TempDir()
	small, err := Fingerprint(write(t, dir, "small.mkv", []byte("a short file")))
	if err != nil || !regexp.MustCompile(`^12-[0-9a-f]{32}$`).MatchString(small) {
		t.Fatalf("%q %v", small, err)
	}
	empty, err := Fingerprint(write(t, dir, "empty.mkv", nil))
	if err != nil || !regexp.MustCompile(`^0-[0-9a-f]{32}$`).MatchString(empty) {
		t.Fatalf("%q %v", empty, err)
	}
	if _, err := Fingerprint(filepath.Join(dir, "missing.mkv")); err == nil {
		t.Fatal("no error for a missing file")
	}
}
