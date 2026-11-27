// SPDX-License-Identifier: GPL-3.0-or-later

// Package tmdb talks to TMDB, the metadata provider
// (docs/06-library-and-metadata.md, section 6.4): one client, at most two
// requests at once, a pause after TMDB asks the server to wait, and every
// answer kept in provider_cache, so that reading an answer again never asks
// for it again.
package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/tasks"
)

// Provider is TMDB's name in provider_cache and external_ids.
const Provider = "tmdb"

// DefaultBase is TMDB's API address.
const DefaultBase = "https://api.themoviedb.org/3"

// Slots is how many requests run at once (06, section 6.3).
const Slots = 2

// Kept is how long an answer is kept in provider_cache without being
// fetched again (D53).
const Kept = 180 * 24 * time.Hour

// maxBody bounds what one answer may hold.
const maxBody = 16 << 20

// The pause after an answer 429 without Retry-After, doubled while the
// answers stay 429, up to maxPause; and the longest Retry-After followed
// (06, section 6.4).
const (
	firstPause = 10 * time.Second
	maxPause   = 5 * time.Minute
	maxRetry   = time.Hour
)

// statusEvery is how often a key that keeps working has its status
// written again (docs/03-data-model.md, section 4).
const statusEvery = 10 * time.Minute

var (
	// ErrNoKey is returned when no key is set.
	ErrNoKey = errors.New("tmdb: no key is set")
	// ErrBadKey is returned when TMDB refuses the key.
	ErrBadKey = errors.New("tmdb: TMDB refuses the key")
)

// Busy is returned while TMDB has asked the server to wait, until Until.
type Busy struct{ Until time.Time }

func (b *Busy) Error() string {
	return "tmdb: TMDB asked the server to wait, until " + b.Until.UTC().Format(time.RFC3339)
}

// UpstreamError is TMDB that cannot be reached, or that answered with
// something the client does not read. Its message never holds the key.
type UpstreamError struct{ Msg string }

func (e *UpstreamError) Error() string { return "tmdb: " + e.Msg }

// AsPause turns a Busy into the pause of the background work that met it
// (docs/06-library-and-metadata.md, section 9); other errors are as they
// are.
func AsPause(err error, now time.Time) error {
	var b *Busy
	if errors.As(err, &b) {
		return &tasks.Pause{Err: err, For: max(b.Until.Sub(now), time.Second)}
	}
	return err
}

// KeySource gives the key to send: the environment's, or the one kept in
// the database; "" when none is set.
type KeySource func(ctx context.Context) (string, error)

// Stored is the key requests are made with: the environment's when it
// sets one, else the one an admin set (docs/04-api.md, section 8.1).
func Stored(d *db.DB, env string) KeySource {
	return func(ctx context.Context) (string, error) {
		if env != "" {
			return env, nil
		}
		key, _, err := d.MetadataKey(ctx)
		return key, err
	}
}

// Client is the server's one client of TMDB.
type Client struct {
	db   *db.DB
	key  KeySource
	log  *slog.Logger
	http *http.Client
	base string
	ua   string
	now  func() time.Time

	slots chan struct{}

	mu      sync.Mutex
	until   time.Time     // no request before this, after a 429
	backoff time.Duration // the last pause without Retry-After
	ok      *bool         // the key's status, as last written
	written time.Time     // when it was last written
}

// New makes the client. version goes into the User-Agent.
func New(d *db.DB, key KeySource, version string, log *slog.Logger) *Client {
	return &Client{
		db: d, key: key, log: log,
		http:  &http.Client{Timeout: 20 * time.Second},
		base:  DefaultBase,
		ua:    "runnel/" + version,
		now:   time.Now,
		slots: make(chan struct{}, Slots),
	}
}

// SetBase points the client at another address: a stand-in, in tests.
func (c *Client) SetBase(base string) { c.base = strings.TrimSuffix(base, "/") }

// token is the form of an API read access token: a JSON Web Token, three
// parts of base64url joined by dots, the first a JSON object.
var token = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// IsToken reports whether a key is an API read access token, sent as a
// bearer token, rather than an API key, sent in the query.
func IsToken(key string) bool { return token.MatchString(key) }

// ValidKey reports whether a key has the form of either: 1 to 1000
// printable ASCII characters without spaces (docs/04-api.md, section 8.1).
func ValidKey(key string) bool {
	if len(key) < 1 || len(key) > 1000 {
		return false
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

// CacheKey is how provider_cache knows a request: its path, and its query
// with the parameters sorted by name. The key it is sent with is never in
// it.
func CacheKey(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// Get answers a request from provider_cache when the answer kept there is
// younger than maxAge, and otherwise asks TMDB and keeps its answer. The
// answer is TMDB's 200 or 404, as it is; a 404 is no error. ErrNoKey,
// ErrBadKey, *Busy and *UpstreamError say why there is none.
func (c *Client) Get(ctx context.Context, path string, q url.Values, maxAge time.Duration) (db.Cached, error) {
	ck := CacheKey(path, q)
	cached, found, err := c.db.CacheGet(ctx, Provider, ck)
	if err != nil {
		return db.Cached{}, err
	}
	if found && c.now().UnixMilli()-cached.FetchedAt < maxAge.Milliseconds() {
		return cached, nil
	}
	key, err := c.key(ctx)
	if err != nil {
		return db.Cached{}, err
	}
	if key == "" {
		return db.Cached{}, ErrNoKey
	}
	status, body, err := c.do(ctx, key, path, q)
	if errors.Is(err, ErrBadKey) {
		c.noteKey(ctx, false)
	}
	if err != nil {
		return db.Cached{}, err
	}
	c.noteKey(ctx, true)
	at := c.now().UnixMilli()
	if err := c.db.CachePut(ctx, Provider, ck, status, body, at); err != nil {
		return db.Cached{}, err
	}
	return db.Cached{Status: status, Body: body, FetchedAt: at}, nil
}

// Check asks TMDB with a key that is not kept yet, whether it takes it:
// one GET /configuration, whose answer is kept (06, section 6.4). nil is
// a key TMDB takes; ErrBadKey one it refuses; *Busy or *UpstreamError
// that it could not be asked.
func (c *Client) Check(ctx context.Context, key string) error {
	if !ValidKey(key) {
		return ErrBadKey
	}
	status, body, err := c.do(ctx, key, "/configuration", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &UpstreamError{"GET /configuration answered " + strconv.Itoa(status)}
	}
	return c.db.CachePut(ctx, Provider, "/configuration", status, body, c.now().UnixMilli())
}

// do sends one request with the key, in one of the client's slots, and
// reads its answer: 200 and 404 are answers; 401 is ErrBadKey; 429 pauses
// every request; the rest is an UpstreamError.
func (c *Client) do(ctx context.Context, key, path string, q url.Values) (int, []byte, error) {
	if until := c.pausedUntil(); !until.IsZero() {
		return 0, nil, &Busy{Until: until}
	}
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
	defer func() { <-c.slots }()

	query := url.Values{}
	for k, v := range q {
		query[k] = v
	}
	bearer := IsToken(key)
	if !bearer {
		query.Set("api_key", key)
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, &UpstreamError{"GET " + path + ": the request cannot be made"}
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, &UpstreamError{"GET " + path + ": " + redact(cause(err), key)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, &UpstreamError{"GET " + path + ": " + redact(err.Error(), key)}
	}
	if len(body) > maxBody {
		return 0, nil, &UpstreamError{fmt.Sprintf("GET %s: the answer is larger than %d MiB", path, maxBody>>20)}
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNotFound:
		c.mu.Lock()
		c.backoff = 0
		c.mu.Unlock()
		return resp.StatusCode, body, nil
	case http.StatusUnauthorized:
		return 0, nil, ErrBadKey
	case http.StatusTooManyRequests:
		until := c.pause(resp.Header.Get("Retry-After"))
		c.log.Info("TMDB asked the server to wait", "until", until.UTC().Format(time.RFC3339))
		return 0, nil, &Busy{Until: until}
	default:
		return 0, nil, &UpstreamError{"GET " + path + " answered " + strconv.Itoa(resp.StatusCode)}
	}
}

// cause is a transport error without the request's address, which holds
// the key when it is sent in the query.
func cause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// redact takes the key out of a message, should any part of the request
// have found its way into it.
func redact(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, "[the key]")
}

func (c *Client) pausedUntil() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until.After(c.now()) {
		return c.until
	}
	return time.Time{}
}

// pause holds every request until the time Retry-After gives, in seconds
// or as a date, up to maxRetry, or for a pause that doubles from
// firstPause to maxPause without one.
func (c *Client) pause(retryAfter string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var d time.Duration
	if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n > 0 {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(retryAfter); err == nil && t.After(now) {
		d = t.Sub(now)
	}
	if d <= 0 {
		c.backoff = min(max(2*c.backoff, firstPause), maxPause)
		d = c.backoff
	}
	c.until = now.Add(min(d, maxRetry))
	return c.until
}

// noteKey keeps whether the key works, as the last request found it:
// written when it changes, or at most every statusEvery otherwise.
func (c *Client) noteKey(ctx context.Context, ok bool) {
	c.mu.Lock()
	now := c.now()
	if c.ok != nil && *c.ok == ok && now.Sub(c.written) < statusEvery {
		c.mu.Unlock()
		return
	}
	c.ok, c.written = &ok, now
	c.mu.Unlock()
	if !ok {
		c.log.Warn("TMDB refuses the metadata key; set another in the settings")
	}
	if err := c.db.SetKeyStatus(ctx, ok, now.UnixMilli()); err != nil && ctx.Err() == nil {
		c.log.Error("cannot keep the metadata key's status", "err", err)
	}
}

// Forget is told that the key changed or went: its status is written
// again at the next request.
func (c *Client) Forget() {
	c.mu.Lock()
	c.ok = nil
	c.mu.Unlock()
}
