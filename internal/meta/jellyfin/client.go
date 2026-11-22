// SPDX-License-Identifier: GPL-3.0-or-later

// Package jellyfin reads what the one-time import needs from a Jellyfin
// server: its version, its users, and its films, series and episodes with
// their paths, provider ids and each user's play state. The calls are the
// ones docs/06-library-and-metadata.md, section 7, lists, as Jellyfin
// 12.4's published API description gives them.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PageSize is how many items one listing asks for.
const PageSize = 500

// maxBody bounds what one answer may hold. A page of 500 items with paths
// and provider ids is far below it.
const maxBody = 64 << 20

// ErrBadKey is returned when the server refuses the key.
var ErrBadKey = errors.New("jellyfin: the server refused the key")

// UpstreamError is a server that cannot be reached, or that answered with
// something this client does not read.
type UpstreamError struct{ Err error }

func (e *UpstreamError) Error() string { return "jellyfin: " + e.Err.Error() }
func (e *UpstreamError) Unwrap() error { return e.Err }

// Client talks to one Jellyfin server with an API key.
type Client struct {
	base string // scheme, host and base path, without a trailing slash
	key  string
	http *http.Client
}

// ValidKey reports whether a key can be sent in the Authorization header:
// 1 to 200 printable ASCII characters, none of them a quote or a comma,
// which end a value there.
func ValidKey(key string) bool {
	if len(key) < 1 || len(key) > 200 {
		return false
	}
	for _, c := range key {
		if c <= ' ' || c > '~' || c == '"' || c == ',' {
			return false
		}
	}
	return true
}

// New makes a client for the server at rawURL, an http or https address
// with its base path if it has one. A nil hc has requests time out after a
// minute.
func New(rawURL, key string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("jellyfin: the address is an http or https URL, such as http://jellyfin.lan:8096")
	}
	if !ValidKey(key) {
		return nil, errors.New("jellyfin: the key has 1 to 200 printable characters, without quotes or commas")
	}
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	return &Client{base: strings.TrimRight(u.String(), "/"), key: key, http: hc}, nil
}

// get asks for path with the query q, with the key when auth is set, and
// decodes the JSON answer into out.
func (c *Client) get(ctx context.Context, path string, q url.Values, auth bool, out any) error {
	target := c.base + path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return &UpstreamError{err}
	}
	if auth {
		req.Header.Set("Authorization", `MediaBrowser Token="`+c.key+`"`)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return &UpstreamError{fmt.Errorf("cannot be reached: %w", err)}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return ErrBadKey
	case resp.StatusCode != http.StatusOK:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return &UpstreamError{fmt.Errorf("%s answered %s", path, resp.Status)}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return &UpstreamError{fmt.Errorf("%s answered with JSON that is not what Jellyfin sends: %w", path, err)}
	}
	return nil
}

// Version is the server's version, from GET /System/Info/Public.
func (c *Client) Version(ctx context.Context) (string, error) {
	var info struct {
		Version string `json:"Version"`
	}
	if err := c.get(ctx, "/System/Info/Public", nil, false, &info); err != nil {
		return "", err
	}
	if info.Version == "" {
		return "", &UpstreamError{errors.New("/System/Info/Public gave no version: is this a Jellyfin server?")}
	}
	return info.Version, nil
}

// User is a Jellyfin user.
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// Users lists the server's users, from GET /Users.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var users []User
	if err := c.get(ctx, "/Users", nil, true, &users); err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.ID == "" || u.Name == "" {
			return nil, &UpstreamError{errors.New("/Users gave a user without an id or a name")}
		}
	}
	return users, nil
}

// Item is a film, a series or an episode as Jellyfin lists it.
type Item struct {
	ID          string            `json:"Id"`
	Name        string            `json:"Name"`
	Type        string            `json:"Type"` // "Movie", "Series" or "Episode"
	Path        string            `json:"Path"`
	ProviderIDs map[string]string `json:"ProviderIds"`
	UserData    *UserData         `json:"UserData"`
}

// UserData is one user's play state of an item.
type UserData struct {
	Played         bool    `json:"Played"`
	PlayCount      int     `json:"PlayCount"`
	LastPlayedDate *string `json:"LastPlayedDate"`
	PositionTicks  int64   `json:"PlaybackPositionTicks"` // 100 ns each
}

// LastPlayed is when the item was last played, in Unix milliseconds, or 0.
func (u UserData) LastPlayed() int64 {
	if u.LastPlayedDate == nil {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, *u.LastPlayedDate)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// PositionMS is the position in milliseconds.
func (u UserData) PositionMS() int64 { return u.PositionTicks / 10_000 }

// Page is one page of a listing.
type Page struct {
	Items      []Item `json:"Items"`
	Total      int    `json:"TotalRecordCount"`
	StartIndex int    `json:"StartIndex"`
}

// Items lists PageSize films, series and episodes from start, with the
// play state of the user with userID, or without any when userID is "".
func (c *Client) Items(ctx context.Context, userID string, start int) (Page, error) {
	q := url.Values{
		"recursive":            {"true"},
		"includeItemTypes":     {"Movie,Series,Episode"},
		"excludeLocationTypes": {"Virtual"},
		"fields":               {"Path,ProviderIds"},
		"enableImages":         {"false"},
		"sortBy":               {"SortName"},
		"startIndex":           {strconv.Itoa(start)},
		"limit":                {strconv.Itoa(PageSize)},
	}
	if userID != "" {
		q.Set("userId", userID)
		q.Set("enableUserData", "true")
	}
	var p Page
	if err := c.get(ctx, "/Items", q, true, &p); err != nil {
		return Page{}, err
	}
	return p, nil
}

// Providers maps the provider names Jellyfin and its anime plugins write
// to those of docs/03-data-model.md, section 3. Names are compared without
// case (docs/06-library-and-metadata.md, section 7).
var Providers = map[string]string{"tmdb": "tmdb", "imdb": "imdb", "tvdb": "tvdb", "tmdbcollection": "tmdb_collection",
	"anidb": "anidb", "anilist": "anilist"}
