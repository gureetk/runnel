// SPDX-License-Identifier: GPL-3.0-or-later

// Package api serves the HTTP API of docs/04-api.md under /api/v1: the
// handlers, the middleware and the JSON shapes.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/db"
)

// Prefix is where the API lives.
const Prefix = "/api/v1"

// jsonDeadline is how long a JSON route has to respond
// (docs/02-architecture.md, section 9).
const jsonDeadline = 15 * time.Second

// Info holds the facts about this server that do not change while it runs.
type Info struct {
	// Name is shown by clients: the environment's, or the host name, which
	// an admin's setting replaces unless NameFromEnv is set.
	Name        string
	NameFromEnv bool
	Version     string
	InstanceID  string
}

// Config is what the API is built from.
type Config struct {
	Info
	// AdminTokenHash is the hash of the local admin token that the server
	// wrote into its data directory at start. Nil takes no admin token.
	AdminTokenHash []byte
	// Now is the clock: time.Now outside tests.
	Now func() time.Time
	// Scans is the scanner, which admin calls ask to scan.
	Scans Scans
	// Gate is the playback gate, which a starting playback holds. Nil
	// holds nothing.
	Gate Gate
	// Images is where images' files are kept. Nil serves none.
	Images Images
	// Work is the background queue, which admin calls look at and wake.
	// Nil has nothing paused by a provider.
	Work Work
	// StartedAt is when the server started; the API's own start when zero.
	StartedAt time.Time
}

// Server is the API. Build one with New and serve its Handler.
type Server struct {
	db        *db.DB
	log       *slog.Logger
	info      Info
	adminHash []byte
	now       func() time.Time
	scans     Scans
	gate      Gate
	images    Images
	work      Work
	startedAt time.Time
	mux       *http.ServeMux
	deadline  time.Duration // for JSON routes: jsonDeadline, shorter in tests

	warm          warmups
	sessions      sessions      // playbacks that report progress
	firstByteHold time.Duration // firstByteHold, shorter in tests
	prepareHold   time.Duration // prepareHold, shorter in tests

	logins      *auth.Limiter // wrong sign-ins by address
	pairStarts  *auth.Limiter // pairings started, by address
	pairLookups *auth.Limiter // pairing codes looked up or approved, by token
	setupCodes  *auth.Limiter // wrong setup codes, by address

	touchMu sync.Mutex
	touched map[int64]int64 // token id -> when its use was last written

	changesMu   sync.Mutex
	changesRows int // rows /changes wrote since memory was last handed back
}

// New builds the API on an open database.
func New(d *db.DB, log *slog.Logger, c Config) *Server {
	now := c.Now
	if now == nil {
		now = time.Now
	}
	var gate Gate = noGate{}
	if c.Gate != nil {
		gate = c.Gate
	}
	s := &Server{
		db:            d,
		log:           log,
		info:          c.Info,
		adminHash:     c.AdminTokenHash,
		now:           now,
		scans:         c.Scans,
		gate:          gate,
		images:        c.Images,
		work:          c.Work,
		startedAt:     c.StartedAt,
		mux:           http.NewServeMux(),
		deadline:      jsonDeadline,
		warm:          warmups{by: map[int64]*warmup{}},
		sessions:      sessions{by: map[sessionKey]*session{}},
		firstByteHold: firstByteHold,
		prepareHold:   prepareHold,
		logins:        auth.NewLimiter(loginTries, time.Minute, now),
		pairStarts:    auth.NewLimiter(pairStartsMinute, time.Minute, now),
		pairLookups:   auth.NewLimiter(pairLookupMinute, time.Minute, now),
		setupCodes:    auth.NewLimiter(setupTries, time.Minute, now),
		touched:       map[int64]int64{},
	}
	if s.startedAt.IsZero() {
		s.startedAt = now()
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.Handle("GET "+Prefix+"/server", s.json(s.handleServer))
	s.mux.Handle("POST "+Prefix+"/auth/login", s.json(s.handleLogin))
	s.mux.Handle("POST "+Prefix+"/setup", s.json(s.handleSetup))
	s.mux.Handle("POST "+Prefix+"/auth/pair/start", s.json(s.handlePairStart))
	s.mux.Handle("GET "+Prefix+"/auth/pair/{code}", s.json(s.signedInOrLocal(s.handlePairLookup)))
	s.mux.Handle("POST "+Prefix+"/auth/pair/approve", s.json(s.signedInOrLocal(s.handlePairApprove)))
	s.mux.Handle("POST "+Prefix+"/auth/pair/poll", s.json(s.handlePairPoll))
	s.mux.Handle("GET "+Prefix+"/auth/me", s.json(s.signedIn(s.handleMe)))
	s.mux.Handle("POST "+Prefix+"/auth/logout", s.json(s.signedIn(s.handleLogout)))
	s.mux.Handle("PUT "+Prefix+"/auth/me/password", s.json(s.signedIn(s.handleSetMyPassword)))
	s.mux.Handle("GET "+Prefix+"/auth/me/devices", s.json(s.signedIn(s.handleMyDevices)))
	s.mux.Handle("DELETE "+Prefix+"/auth/me/devices/{id}", s.json(s.signedIn(s.handleSignOutDevice)))

	s.mux.Handle("GET "+Prefix+"/libraries", s.json(s.signedIn(s.handleLibraries)))
	s.mux.Handle("GET "+Prefix+"/items", s.json(s.signedIn(s.handleItems)))
	s.mux.Handle("GET "+Prefix+"/items/letters", s.json(s.signedIn(s.handleLetters)))
	s.mux.Handle("GET "+Prefix+"/home", s.json(s.signedIn(s.handleHome)))
	s.mux.Handle("GET "+Prefix+"/search", s.json(s.signedIn(s.handleSearch)))
	s.mux.Handle("GET "+Prefix+"/genres", s.json(s.signedIn(s.handleGenres)))
	s.mux.Handle("GET "+Prefix+"/changes", s.json(s.signedIn(s.handleChanges)))
	s.mux.Handle("GET "+Prefix+"/items/{id}", s.json(s.signedIn(s.handleItem)))
	s.mux.Handle("GET "+Prefix+"/files/{id}", s.json(s.signedIn(s.handleFile)))
	s.mux.Handle("GET "+Prefix+"/files/{id}/raw", s.media(s.handleRaw))
	s.mux.Handle("GET "+Prefix+"/files/{id}/external/{external_id}", s.media(s.handleExternal))
	s.mux.Handle("POST "+Prefix+"/files/{id}/prepare", s.json(s.signedIn(s.handlePrepare)))
	s.mux.Handle("POST "+Prefix+"/progress", s.json(s.signedIn(s.handleProgress)))
	s.mux.Handle("PUT "+Prefix+"/items/{id}/played", s.json(s.signedIn(s.handlePlayed(true))))
	s.mux.Handle("DELETE "+Prefix+"/items/{id}/played", s.json(s.signedIn(s.handlePlayed(false))))
	s.mux.Handle("PUT "+Prefix+"/items/played", s.json(s.signedIn(s.handlePlayedMany(true))))
	s.mux.Handle("DELETE "+Prefix+"/items/played", s.json(s.signedIn(s.handlePlayedMany(false))))
	s.mux.Handle("PUT "+Prefix+"/items/state", s.json(s.signedIn(s.handleRestore)))
	s.mux.Handle("PUT "+Prefix+"/items/{id}/list", s.json(s.signedIn(s.handleListed(true))))
	s.mux.Handle("DELETE "+Prefix+"/items/{id}/list", s.json(s.signedIn(s.handleListed(false))))
	s.mux.Handle("PUT "+Prefix+"/series/{id}/prefs", s.json(s.signedIn(s.handleSeriesPrefs(false))))
	s.mux.Handle("DELETE "+Prefix+"/series/{id}/prefs", s.json(s.signedIn(s.handleSeriesPrefs(true))))
	s.mux.Handle("GET "+Prefix+"/auth/me/prefs", s.json(s.signedIn(s.handleMyPrefs)))
	s.mux.Handle("PUT "+Prefix+"/auth/me/prefs", s.json(s.signedIn(s.handleSetMyPrefs)))
	s.mux.Handle("GET "+Prefix+"/img/{key}/{w}", http.HandlerFunc(s.handleImage))

	s.mux.Handle("GET "+Prefix+"/admin/users", s.json(s.admin(s.handleAdminUsers)))
	s.mux.Handle("POST "+Prefix+"/admin/users", s.json(s.admin(s.handleCreateUser)))
	s.mux.Handle("PATCH "+Prefix+"/admin/users/{id}", s.json(s.admin(s.handleChangeUser)))
	s.mux.Handle("DELETE "+Prefix+"/admin/users/{id}", s.json(s.admin(s.handleDeleteUser)))
	s.mux.Handle("GET "+Prefix+"/admin/tokens", s.json(s.admin(s.handleAdminTokens)))
	s.mux.Handle("DELETE "+Prefix+"/admin/tokens/{id}", s.json(s.admin(s.handleRevokeToken)))
	s.mux.Handle("GET "+Prefix+"/admin/pairings", s.json(s.admin(s.handleAdminPairings)))
	s.mux.Handle("GET "+Prefix+"/admin/setup-code", s.json(s.admin(s.handleSetupCode)))
	s.mux.Handle("GET "+Prefix+"/admin/settings", s.json(s.admin(s.handleSettings)))
	s.mux.Handle("PATCH "+Prefix+"/admin/settings", s.json(s.admin(s.handlePatchSettings)))
	s.mux.Handle("GET "+Prefix+"/admin/libraries", s.json(s.admin(s.handleAdminLibraries)))
	s.mux.Handle("POST "+Prefix+"/admin/libraries", s.json(s.admin(s.handleCreateLibrary)))
	s.mux.Handle("PATCH "+Prefix+"/admin/libraries/{id}", s.json(s.admin(s.handleChangeLibrary)))
	s.mux.Handle("DELETE "+Prefix+"/admin/libraries/{id}", s.json(s.admin(s.handleDeleteLibrary)))
	s.mux.Handle("GET "+Prefix+"/admin/fs", s.json(s.admin(s.handleFS)))
	s.mux.Handle("GET "+Prefix+"/admin/tasks", s.json(s.admin(s.handleTasks)))
	s.mux.Handle("POST "+Prefix+"/admin/tasks/cancel", s.json(s.admin(s.handleWork(false))))
	s.mux.Handle("POST "+Prefix+"/admin/tasks/retry", s.json(s.admin(s.handleWork(true))))
	s.mux.Handle("POST "+Prefix+"/admin/scan", s.json(s.admin(s.handleScan)))
	s.mux.Handle("GET "+Prefix+"/admin/parse-report", s.json(s.admin(s.handleParseReport)))
	s.mux.Handle("GET "+Prefix+"/admin/playlog", s.json(s.admin(s.handlePlaylog)))
	s.mux.Handle("GET "+Prefix+"/admin/stats", s.json(s.admin(s.handleStats)))
}

// Handler is the whole server: every route inside the middleware that every
// request passes through.
func (s *Server) Handler() http.Handler {
	return s.base(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request no route takes, under the API, gets the API's error
		// shape. Anything else is left to the mux.
		if _, pattern := s.mux.Handler(r); pattern == "" && strings.HasPrefix(r.URL.Path, Prefix+"/") {
			s.fail(w, r, http.StatusNotFound, "not_found", "no such endpoint")
			return
		}
		s.mux.ServeHTTP(w, r)
	}))
}

// writeJSON sends v with the given status. It is not to be stored by a
// cache, unless the handler said otherwise.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// fail sends an error in the shape of docs/04-api.md, section 1, with a
// code from section 9.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = message
	writeJSON(w, status, b)
}

// internal logs err once, with the request's id, and sends 500 with that id
// in the message so that the log line can be found.
func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	id := requestID(r.Context())
	s.log.Error("request failed", "id", id, "method", r.Method, "path", r.URL.Path, "err", err)
	s.fail(w, r, http.StatusInternalServerError, "internal", "internal error, logged as "+id)
}
