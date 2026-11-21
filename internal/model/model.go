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

// Probe is what ffprobe found in a file, mapped to the columns the
// database keeps (docs/06-library-and-metadata.md, section 3). A zero
// number or an empty string is unknown.
type Probe struct {
	Container        string // "matroska", "webm", "mp4", "mov", "avi", "mpegts", "asf", "mpeg", …
	DurationMS       int64
	BitRate          int64
	AttachmentsBytes int64
	Streams          []Stream
	Chapters         []Chapter
}

// Stream is one stream of a file. Which fields mean something depends on
// its Kind.
type Stream struct {
	Index           int    // ffprobe's stream index
	Kind            string // "video", "audio", "subtitle" or "attachment"
	Codec           string
	Profile         string
	Lang            string // as the file tags it
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool

	// Video.
	Width, Height  int
	PixFmt         string
	BitDepth       int
	Level          *int // nil when unknown; 0 is a level
	FPSNum, FPSDen int
	HDR            string // "hdr10", "hlg", "dv" or ""

	// Audio.
	Channels      int
	ChannelLayout string
	SampleRate    int
	BitRate       int64

	// Attachment.
	Filename string
	MIME     string
	Size     int64
}

// Chapter is one chapter of a file.
type Chapter struct {
	Index   int
	StartMS int64
	EndMS   *int64 // nil when unknown
	Title   string
}

// Library is a named set of roots holding one kind of media.
type Library struct {
	ID         int64
	Name       string
	Kind       string // "movies" or "shows"
	Profile    string // "default" or "anime"
	Roots      []Root
	UpdatedSeq int64
}

// Root is a folder that a library scans.
type Root struct {
	ID        int64
	LibraryID int64
	Path      string // absolute, no trailing slash
	Available bool
}

// ScanResult is what one scan of a root found, as the root records it.
type ScanResult struct {
	At      int64
	MS      int64
	Added   int // new files
	Changed int // files whose size or time changed, and files that moved
	Removed int // files newly marked missing
	Error   string
}
