// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestPasswords(t *testing.T) {
	p, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Hash) != 32 || len(p.Salt) != 16 || p.Iter != Iterations {
		t.Fatalf("hash %d bytes, salt %d bytes, %d iterations", len(p.Hash), len(p.Salt), p.Iter)
	}
	if !CheckPassword("correct horse", p) {
		t.Fatal("the right password was refused")
	}
	if CheckPassword("correct horsE", p) {
		t.Fatal("a wrong password was accepted")
	}
	q, _ := HashPassword("correct horse")
	if string(q.Salt) == string(p.Salt) || string(q.Hash) == string(p.Hash) {
		t.Fatal("two hashes of one password share a salt")
	}
	if _, err := HashPassword("nine char"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("a nine-character password: %v", err)
	}
	// Characters are counted, not bytes.
	if _, err := HashPassword("ääääääääää"); err != nil {
		t.Fatalf("ten two-byte characters: %v", err)
	}
	if CheckPassword("", dummy) {
		t.Fatal("the dummy hash matched")
	}
}

func TestTokens(t *testing.T) {
	seen := map[string]bool{}
	re := regexp.MustCompile(`^rn_[0-9A-Za-z]{43}$`)
	for i := 0; i < 200; i++ {
		tok, hash := NewToken()
		if !re.MatchString(tok) || !LooksLikeToken(tok) {
			t.Fatalf("token %q has the wrong shape", tok)
		}
		if string(hash) != string(HashToken(tok)) || len(hash) != 32 {
			t.Fatal("the hash is not the SHA-256 of the token")
		}
		if seen[tok] {
			t.Fatal("a token repeated")
		}
		seen[tok] = true
	}
	for _, s := range []string{"", "rn_", "rn_" + "!" + "0123456789012345678901234567890123456789ab", "xx_0123456789012345678901234567890123456789abc"} {
		if LooksLikeToken(s) {
			t.Errorf("%q looks like a token", s)
		}
	}
}

func TestAdminTokenFile(t *testing.T) {
	dir := t.TempDir()
	hash, err := WriteAdminToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, AdminTokenFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("admin.token: %v %v", fi.Mode(), err)
	}
	tok, err := ReadAdminToken(dir)
	if err != nil || string(HashToken(tok)) != string(hash) {
		t.Fatalf("read back %q, %v", tok, err)
	}
	// A new start makes a new token, and leaves no temporary file.
	hash2, _ := WriteAdminToken(dir)
	if string(hash2) == string(hash) {
		t.Fatal("the admin token did not change")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the data directory holds %d entries", len(entries))
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := NewLimiter(5, time.Minute, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if blocked, _ := l.Blocked("192.0.2.1"); blocked {
			t.Fatalf("blocked after %d tries", i)
		}
		l.Fail("192.0.2.1")
		now = now.Add(time.Second)
	}
	blocked, wait := l.Blocked("192.0.2.1")
	if !blocked || wait != 55*time.Second {
		t.Fatalf("after five tries: blocked %v, wait %v", blocked, wait)
	}
	if blocked, _ := l.Blocked("192.0.2.2"); blocked {
		t.Fatal("another address is blocked")
	}
	now = now.Add(55 * time.Second)
	if blocked, _ := l.Blocked("192.0.2.1"); blocked {
		t.Fatal("still blocked once the first try left the window")
	}
}
