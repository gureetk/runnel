// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
)

// The limits of pairing (docs/04-api.md, section 3.3; P7).
const (
	pairFor          = 10 * time.Minute // how long a code waits
	pollEvery        = 2                // seconds between polls, as the device is told
	maxOpenPairings  = 20               // pairings that wait at once
	pairStartsMinute = 10               // pairings one address may start a minute
	pairLookupMinute = 10               // codes one token may look up or approve a minute
)

type deviceBody struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Client string `json:"client"`
}

// handlePairStart is POST /auth/pair/start: a device that wants in asks
// for a code. No authentication.
func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request) {
	addr := remoteIP(r).String()
	if blocked, wait := s.pairStarts.Blocked(addr); blocked {
		s.tooMany(w, r, wait)
		return
	}
	var req struct {
		Device deviceBody `json:"device"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Device.ID == "" || len(req.Device.ID) > 200 || len(req.Device.Name) > 100 || len(req.Device.Client) > 100 {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "device.id is needed, with at most 200 bytes; device.name and device.client have at most 100")
		return
	}
	s.pairStarts.Fail(addr)
	now := s.now()
	secret, hash := auth.NewPairingSecret()
	dev := model.Device{ID: req.Device.ID, Name: req.Device.Name, Client: req.Device.Client}
	for range 5 {
		code := auth.NewPairingCode()
		err := s.db.StartPairing(r.Context(), code, hash, dev, now.UnixMilli(), now.Add(pairFor).UnixMilli(), maxOpenPairings)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{"code": code, "secret": secret,
				"expires_in": int(pairFor.Seconds()), "poll_every": pollEvery})
			return
		case errors.Is(err, db.ErrTooMany):
			w.Header().Set("Retry-After", "60")
			s.fail(w, r, http.StatusTooManyRequests, "too_many", "too many devices are waiting to pair; try again in a minute")
			return
		case !errors.Is(err, db.ErrNameTaken):
			s.internal(w, r, err)
			return
		}
	}
	s.internal(w, r, errors.New("no free pairing code in five tries"))
}

// pairingAsked reads a code a signed-in person typed, counts the try
// against their token, and finds the pairing. It answers and reports
// false when it cannot go on.
func (s *Server) pairingAsked(w http.ResponseWriter, r *http.Request, typed string) (db.Pairing, bool) {
	c := callerOf(r.Context())
	key := "local"
	if !c.local {
		key = strconv.FormatInt(c.tokenID, 10)
	}
	if blocked, wait := s.pairLookups.Blocked(key); blocked {
		s.tooMany(w, r, wait)
		return db.Pairing{}, false
	}
	s.pairLookups.Fail(key)
	code, ok := auth.CleanPairingCode(typed)
	if !ok {
		s.fail(w, r, http.StatusNotFound, "not_found", "no device waits with that code")
		return db.Pairing{}, false
	}
	p, err := s.db.PairingOf(r.Context(), code, s.now().UnixMilli())
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no device waits with that code")
	case errors.Is(err, db.ErrExpired):
		s.fail(w, r, http.StatusGone, "expired", "that code ran out or was used")
	case err != nil:
		s.internal(w, r, err)
	default:
		return p, true
	}
	return db.Pairing{}, false
}

// handlePairLookup is GET /auth/pair/{code}: what a code would let in.
func (s *Server) handlePairLookup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pairingAsked(w, r, r.PathValue("code"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": p.Code,
		"device":     map[string]string{"name": p.Device.Name, "client": p.Device.Client},
		"expires_in": max(0, (p.ExpiresAt-s.now().UnixMilli())/1000)})
}

// handlePairApprove is POST /auth/pair/approve: signs the waiting device
// in as the approving user, or, for an admin or the local admin token, as
// the user named by user_id.
func (s *Server) handlePairApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string `json:"code"`
		UserID *int64 `json:"user_id"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	c := callerOf(r.Context())
	user := c.user.ID
	switch {
	case c.local && req.UserID == nil:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "with the local admin token, user_id is needed")
		return
	case req.UserID != nil && *req.UserID != c.user.ID && !c.local && !c.user.IsAdmin:
		s.fail(w, r, http.StatusForbidden, "forbidden", "only an admin signs a device in as someone else")
		return
	case req.UserID != nil:
		user = *req.UserID
	}
	if _, err := s.db.PasswordOf(r.Context(), user); errors.Is(err, db.ErrNotFound) {
		s.fail(w, r, http.StatusBadRequest, "bad_request", "no such user")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	p, ok := s.pairingAsked(w, r, req.Code)
	if !ok {
		return
	}
	err := s.db.ApprovePairing(r.Context(), p.Code, user, s.now().UnixMilli())
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no device waits with that code")
	case errors.Is(err, db.ErrExpired):
		s.fail(w, r, http.StatusGone, "expired", "that code ran out or was used")
	case errors.Is(err, db.ErrApproved):
		s.fail(w, r, http.StatusConflict, "conflict", "that code is approved already")
	default:
		s.internal(w, r, err)
	}
}

// handlePairPoll is POST /auth/pair/poll: the waiting device asks, with
// its secret, whether it is in. No authentication.
func (s *Server) handlePairPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string `json:"code"`
		Secret string `json:"secret"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	code, ok := auth.CleanPairingCode(req.Code)
	if !ok || req.Secret == "" {
		s.fail(w, r, http.StatusNotFound, "not_found", "no device waits with that code and secret")
		return
	}
	token, hash := auth.NewToken()
	u, approved, err := s.db.ClaimPairing(r.Context(), code, auth.HashSecret(req.Secret), hash, s.now().UnixMilli())
	switch {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no device waits with that code and secret")
	case errors.Is(err, db.ErrExpired):
		s.fail(w, r, http.StatusGone, "expired", "the code ran out or was used; start again")
	case err != nil:
		s.internal(w, r, err)
	case !approved:
		writeJSON(w, http.StatusAccepted, map[string]any{"waiting": true})
	default:
		writeJSON(w, http.StatusOK, struct {
			Token string   `json:"token"`
			User  userBody `json:"user"`
		}{token, userJSON(u)})
	}
}

// handleAdminPairings is GET /admin/pairings: the devices that wait.
func (s *Server) handleAdminPairings(w http.ResponseWriter, r *http.Request) {
	now := s.now().UnixMilli()
	list, err := s.db.OpenPairings(r.Context(), now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []map[string]any{}
	for _, p := range list {
		out = append(out, map[string]any{"code": p.Code, "device": map[string]string{"name": p.Device.Name, "client": p.Device.Client},
			"created_at": p.CreatedAt, "expires_in": max(0, (p.ExpiresAt-now)/1000)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": out})
}

// signedInOrLocal wraps a route that takes any user's token, or the local
// admin token from the server's own host, as approving a pairing does.
func (s *Server) signedInOrLocal(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.isLocalToken(bearer(r)) {
			s.admin(h)(w, r)
			return
		}
		s.signedIn(h)(w, r)
	}
}

// isLocalToken reports whether a token is the local admin token.
func (s *Server) isLocalToken(token string) bool {
	return s.adminHash != nil && auth.LooksLikeToken(token) &&
		subtle.ConstantTimeCompare(auth.HashToken(token), s.adminHash) == 1
}
