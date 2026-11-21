// SPDX-License-Identifier: GPL-3.0-or-later

// Package model holds the plain structs that more than one package uses.
// Times are Unix milliseconds (docs/02-architecture.md, section 4).
package model

// User is a person who can sign in.
type User struct {
	ID      int64
	Name    string
	IsAdmin bool
}

// Device is what a client says about itself when it signs in
// (docs/04-api.md, section 3.2).
type Device struct {
	ID     string // made once by the client and kept
	Name   string // such as "Living room TV"; may be empty
	Client string // such as "runnel-androidtv/1.0.0"; may be empty
}

// Password is a password as it is stored: PBKDF2 with SHA-256.
type Password struct {
	Hash []byte
	Salt []byte
	Iter int
}
