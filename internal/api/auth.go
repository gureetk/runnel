// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
)

const (
	// touchEvery is how often a token's last_used_at is written at most
	// (docs/08-decisions.md, P7).
	touchEvery = 10 * time.Minute
	// loginTries wrong sign-ins a minute from one address are allowed
	// (docs/04-api.md, section 3.2).
	loginTries = 5
)

// caller is who made a request: a signed-in user, or the subcommands with
// the local admin token, which have no user.
type caller struct {
	user    model.User
	tokenID int64 // the token the user signed in with
	local   bool
}

func callerOf(ctx context.Context) caller {
	c, _ := ctx.Value(callerKey).(caller)
	return c
}

// bearer is the token in the Authorization header, or "".
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > len("Bearer ") && strings.EqualFold(h[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
}

// signedIn wraps a route that needs a user's token in the Authorization
// header.
func (s *Server) signedIn(h http.HandlerFunc) http.HandlerFunc {
	return s.authenticate(h, false)
}

// media wraps a media route, which also takes the token as ?t=, for
// players that cannot send headers (docs/04-api.md, section 1).
func (s *Server) media(h http.HandlerFunc) http.HandlerFunc {
	return s.authenticate(h, true)
}

func (s *Server) authenticate(h http.HandlerFunc, query bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" && query {
			token = r.URL.Query().Get("t")
		}
		t, ok := s.userOf(w, r, token)
		if !ok {
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), callerKey, caller{user: t.User, tokenID: t.TokenID})))
	}
}

// admin wraps a route of docs/04-api.md, section 8: it takes an admin's
// token, or the local admin token from the server's own host.
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if s.adminHash != nil && auth.LooksLikeToken(token) &&
			subtle.ConstantTimeCompare(auth.HashToken(token), s.adminHash) == 1 {
			if !fromThisHost(r) {
				s.fail(w, r, http.StatusUnauthorized, "unauthorized", "the local admin token is taken only from the server's own host")
				return
			}
			h(w, r.WithContext(context.WithValue(r.Context(), callerKey, caller{local: true})))
			return
		}
		t, ok := s.userOf(w, r, token)
		if !ok {
			return
		}
		if !t.User.IsAdmin {
			s.fail(w, r, http.StatusForbidden, "forbidden", "this needs an admin")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), callerKey, caller{user: t.User, tokenID: t.TokenID})))
	}
}

// userOf looks up the user of a token, or answers 401 and reports false.
func (s *Server) userOf(w http.ResponseWriter, r *http.Request, token string) (db.TokenUser, bool) {
	if !auth.LooksLikeToken(token) {
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "a token is needed")
		return db.TokenUser{}, false
	}
	t, err := s.db.UserByToken(r.Context(), auth.HashToken(token))
	if errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "the token is unknown or signed out")
		return db.TokenUser{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return db.TokenUser{}, false
	}
	s.touch(t)
	return t, true
}

// touch writes a token's last_used_at when it is ten minutes old or more,
// outside the request, so that a request never waits for the writer.
func (s *Server) touch(t db.TokenUser) {
	now := s.now().UnixMilli()
	if now-t.LastUsedAt < touchEvery.Milliseconds() {
		return
	}
	s.touchMu.Lock()
	if last, ok := s.touched[t.TokenID]; ok && now-last < touchEvery.Milliseconds() {
		s.touchMu.Unlock()
		return
	}
	if len(s.touched) > 10_000 {
		clear(s.touched)
	}
	s.touched[t.TokenID] = now
	s.touchMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.db.TouchToken(ctx, t.TokenID, now); err != nil && !errors.Is(err, db.ErrClosed) {
			s.log.Warn("could not record a token's use", "err", err)
		}
	}()
}

// fromThisHost reports whether a request comes from the machine the server
// runs on: from a loopback address, or from the very address it arrived at.
func fromThisHost(r *http.Request) bool {
	ip := remoteIP(r)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if tcp, ok := la.(*net.TCPAddr); ok && tcp.IP.Equal(ip) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// decode reads a JSON body into v, or answers 400 and reports false.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "the body is not the JSON this call takes")
		return false
	}
	return true
}

// tooMany answers 429 with the seconds to wait (docs/04-api.md, section 1).
func (s *Server) tooMany(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	s.fail(w, r, http.StatusTooManyRequests, "too_many", "too many wrong tries; wait and try again")
}

type userBody struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

func userJSON(u model.User) userBody { return userBody{ID: u.ID, Name: u.Name, IsAdmin: u.IsAdmin} }

type loginRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Device   struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Client string `json:"client"`
	} `json:"device"`
}

// handleLogin is POST /auth/login (docs/04-api.md, section 3.2).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	addr := remoteIP(r).String()
	if blocked, wait := s.logins.Blocked(addr); blocked {
		s.tooMany(w, r, wait)
		return
	}
	var req loginRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Name == "" || req.Password == "" || req.Device.ID == "" {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "name, password and device.id are needed")
		return
	}
	if len(req.Device.ID) > 200 || len(req.Device.Name) > 100 || len(req.Device.Client) > 100 {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "device.id has at most 200 bytes, device.name and device.client 100")
		return
	}

	u, pw, err := s.db.UserForLogin(r.Context(), req.Name)
	switch {
	case errors.Is(err, db.ErrNotFound) || (err == nil && pw == nil):
		// No such user, or one who signs in by pairing only.
		auth.WasteTime(req.Password)
		s.logins.Fail(addr)
		s.fail(w, r, http.StatusUnauthorized, "wrong_login", "the name or the password is wrong")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if !auth.CheckPassword(req.Password, *pw) {
		s.logins.Fail(addr)
		s.fail(w, r, http.StatusUnauthorized, "wrong_login", "the name or the password is wrong")
		return
	}

	token, hash := auth.NewToken()
	dev := model.Device{ID: req.Device.ID, Name: req.Device.Name, Client: req.Device.Client}
	if err := s.db.ReplaceToken(r.Context(), u.ID, dev, hash, s.now().UnixMilli()); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Token string   `json:"token"`
		User  userBody `json:"user"`
	}{token, userJSON(u)})
}

type createUserRequest struct {
	Name     string  `json:"name"`
	Password *string `json:"password"`
	IsAdmin  bool    `json:"is_admin"`
}

// validName is a user name of 1 to 40 characters, with no control
// characters and no space at either end.
func validName(name string) bool {
	n := len([]rune(name))
	if n < 1 || n > 40 || strings.TrimSpace(name) != name {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// handleCreateUser is POST /admin/users (docs/04-api.md, section 8.6).
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !validName(req.Name) {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a name has 1 to 40 characters, no control characters, and no space at either end")
		return
	}
	var pw *model.Password
	if req.Password != nil {
		p, err := auth.HashPassword(*req.Password)
		if errors.Is(err, auth.ErrWeakPassword) {
			s.fail(w, r, http.StatusBadRequest, "weak_password", err.Error())
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		pw = &p
	}
	u, err := s.db.CreateUser(r.Context(), req.Name, pw, req.IsAdmin, s.now().UnixMilli())
	if errors.Is(err, db.ErrNameTaken) {
		s.fail(w, r, http.StatusConflict, "conflict", "a user with that name exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, userJSON(u))
}
