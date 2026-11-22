// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"strings"
	"testing"
)

func TestPairingCodes(t *testing.T) {
	seen := map[byte]bool{}
	for range 2000 {
		c := NewPairingCode()
		if len(c) != PairingCodeLen {
			t.Fatalf("code %q", c)
		}
		for i := 0; i < len(c); i++ {
			if !strings.ContainsRune(PairingAlphabet, rune(c[i])) || strings.ContainsRune("0O1IL", rune(c[i])) {
				t.Fatalf("code %q has %q", c, c[i])
			}
			seen[c[i]] = true
		}
	}
	if len(seen) != len(PairingAlphabet) {
		t.Fatalf("2,000 codes used %d of %d characters", len(seen), len(PairingAlphabet))
	}
	for typed, want := range map[string]string{"k7q2m9": "K7Q2M9", "K7Q-2M9": "K7Q2M9", " k7q 2m9 ": "K7Q2M9", "K7Q2M": "", "K7Q2MO": "", "K7Q2M90": ""} {
		got, ok := CleanPairingCode(typed)
		if got != want || ok != (want != "") {
			t.Errorf("%q: %q %v, want %q", typed, got, ok, want)
		}
	}
	s, h := NewPairingSecret()
	if len(s) != 34 || !strings.HasPrefix(s, "s_") || string(HashSecret(s)) != string(h) {
		t.Fatalf("secret %q", s)
	}
}
