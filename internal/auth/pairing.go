// SPDX-License-Identifier: GPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PairingAlphabet is what a pairing code is made of: digits and capitals
// without the ones that look alike, 0, O, 1, I and L (docs/04-api.md,
// section 3.3).
const PairingAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// PairingCodeLen is how long a pairing code is.
const PairingCodeLen = 6

// SetupCodeLen is how long the setup code of the first run is (D35).
const SetupCodeLen = 8

// NewPairingCode makes a random pairing code, each character drawn
// evenly from PairingAlphabet.
func NewPairingCode() string { return newCode(PairingCodeLen) }

// NewSetupCode makes the setup code of the first run: eight characters
// of PairingAlphabet (docs/04-api.md, section 3.4).
func NewSetupCode() string { return newCode(SetupCodeLen) }

// ShowSetupCode is a setup code as it is shown: in two groups of four,
// "K7Q2-M9XD".
func ShowSetupCode(code string) string {
	if len(code) != SetupCodeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// CleanSetupCode is a setup code as a person typed it, or false.
func CleanSetupCode(s string) (string, bool) { return cleanCode(s, SetupCodeLen) }

func newCode(n int) string {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < n {
		if _, err := rand.Read(buf); err != nil {
			panic("auth: no randomness: " + err.Error())
		}
		// 248 is the largest multiple of 31 below 256: above it a byte
		// would favour the first characters.
		if buf[0] < 248 {
			b.WriteByte(PairingAlphabet[int(buf[0])%len(PairingAlphabet)])
		}
	}
	return b.String()
}

// CleanPairingCode is a code as a person typed it, in capitals and
// without spaces or dashes, or false when it cannot be one.
func CleanPairingCode(s string) (string, bool) { return cleanCode(s, PairingCodeLen) }

func cleanCode(s string, n int) (string, bool) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(s))
	if len(s) != n {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(PairingAlphabet, rune(s[i])) {
			return "", false
		}
	}
	return s, true
}

// NewPairingSecret makes the secret only the device that waits holds:
// "s_" and 32 hexadecimal characters, 128 random bits. Only its SHA-256 is
// stored.
func NewPairingSecret() (secret string, hash []byte) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("auth: no randomness: " + err.Error())
	}
	secret = "s_" + hex.EncodeToString(buf)
	return secret, HashSecret(secret)
}

// HashSecret is the SHA-256 of a pairing secret.
func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}
