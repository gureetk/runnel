// SPDX-License-Identifier: GPL-3.0-or-later

// Package auth holds what sign-in needs that is not SQL: password hashes,
// tokens, the local admin token, and the limit on wrong tries.
// docs/02-architecture.md, section 10, and docs/04-api.md, section 3.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/gureetk/runnel/internal/model"
)

// Iterations is the PBKDF2 count for new passwords. It is stored with each
// hash, so raising it later leaves older hashes readable. One hash took
// 117 ms in the development sandbox (2 vCPUs).
const Iterations = 600_000

// MinPasswordLen is the shortest password accepted, in characters
// (docs/04-api.md, section 3.2).
const MinPasswordLen = 10

// ErrWeakPassword is returned for a password under MinPasswordLen.
var ErrWeakPassword = fmt.Errorf("a password has at least %d characters", MinPasswordLen)

// HashPassword makes a new salt and hashes password with it.
func HashPassword(password string) (model.Password, error) {
	if len([]rune(password)) < MinPasswordLen {
		return model.Password{}, ErrWeakPassword
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
	if err != nil {
		return model.Password{}, fmt.Errorf("auth: hash password: %w", err)
	}
	return model.Password{Hash: key, Salt: salt, Iter: Iterations}, nil
}

// CheckPassword reports whether password matches p. It takes as long for
// a wrong password as for a right one.
func CheckPassword(password string, p model.Password) bool {
	if len(p.Hash) == 0 || p.Iter <= 0 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, p.Salt, p.Iter, len(p.Hash))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, p.Hash) == 1
}

// dummy is checked against when a name is unknown, so that a wrong name
// takes as long to refuse as a wrong password.
var dummy = model.Password{Hash: make([]byte, 32), Salt: make([]byte, 16), Iter: Iterations}

// WasteTime does the work of one password check and throws it away.
func WasteTime(password string) { CheckPassword(password, dummy) }

// TokenPrefix starts every token (docs/08-decisions.md, D20).
const TokenPrefix = "rn_"

// tokenLen is the length of a token: the prefix, then 32 random bytes as 43
// base-62 characters (62^43 > 2^256).
const tokenLen = len(TokenPrefix) + 43

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewToken makes a token from 32 random bytes. Only its hash is stored.
func NewToken() (token string, hash []byte) {
	var b [32]byte
	rand.Read(b[:])
	n := new(big.Int).SetBytes(b[:])
	sixtyTwo, digit := big.NewInt(62), new(big.Int)
	out := []byte(TokenPrefix + strings.Repeat("0", 43))
	for i := len(out) - 1; i >= len(TokenPrefix); i-- {
		n.DivMod(n, sixtyTwo, digit)
		out[i] = base62[digit.Int64()]
	}
	token = string(out)
	return token, HashToken(token)
}

// HashToken is the SHA-256 of a token, as stored in tokens.hash.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// LooksLikeToken rules out what cannot be a token, before any lookup.
func LooksLikeToken(s string) bool {
	if len(s) != tokenLen || !strings.HasPrefix(s, TokenPrefix) {
		return false
	}
	for _, c := range s[len(TokenPrefix):] {
		if !strings.ContainsRune(base62, c) {
			return false
		}
	}
	return true
}

// AdminTokenFile is the name of the local admin token in the data directory.
const AdminTokenFile = "admin.token"

// WriteAdminToken makes a new local admin token, writes it to
// admin.token in dataDir, readable by the server's own user only, and
// returns its hash. The subcommands read the file to reach the running
// server (docs/02-architecture.md, sections 6 and 7). Every start makes a
// new one.
func WriteAdminToken(dataDir string) ([]byte, error) {
	token, hash := NewToken()
	tmp, err := os.CreateTemp(dataDir, ".admin.token-*")
	if err != nil {
		return nil, fmt.Errorf("auth: admin token: %w", err)
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("auth: admin token: %w", err)
	}
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("auth: admin token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("auth: admin token: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dataDir, AdminTokenFile)); err != nil {
		return nil, fmt.Errorf("auth: admin token: %w", err)
	}
	return hash, nil
}

// ReadAdminToken reads the local admin token from dataDir.
func ReadAdminToken(dataDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, AdminTokenFile))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if !LooksLikeToken(token) {
		return "", errors.New("auth: " + AdminTokenFile + " does not hold a token")
	}
	return token, nil
}
