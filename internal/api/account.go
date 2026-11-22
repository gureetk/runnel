// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/db"
)

type myDeviceBody struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Client     string `json:"client"`
	SignedInAt int64  `json:"signed_in_at"`
	LastUsedAt *int64 `json:"last_used_at"`
	Current    bool   `json:"current"`
}

func myDeviceJSON(d db.Device, current int64) myDeviceBody {
	return myDeviceBody{ID: d.ID, Name: d.Name, Client: d.Client, SignedInAt: d.SignedInAt, LastUsedAt: int64OrNil(d.LastUsedAt),
		Current: d.ID == current}
}

// handleMe is GET /auth/me: the user and the device of the token
// (docs/04-api.md, section 3.5).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r.Context())
	dev, err := s.db.DeviceOf(r.Context(), c.tokenID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	pw, err := s.db.PasswordOf(r.Context(), c.user.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": struct {
			userBody
			HasPassword bool `json:"has_password"`
		}{userJSON(c.user), pw != nil},
		"device": myDeviceJSON(dev, c.tokenID),
	})
}

// handleLogout is POST /auth/logout: signs out the token it is called
// with.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r.Context())
	if err := s.db.SignOut(r.Context(), c.user.ID, c.tokenID); err != nil && !errors.Is(err, db.ErrNotFound) {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetMyPassword is PUT /auth/me/password. A wrong current password
// counts with failed sign-ins. Tokens stay valid.
func (s *Server) handleSetMyPassword(w http.ResponseWriter, r *http.Request) {
	addr := remoteIP(r).String()
	if blocked, wait := s.logins.Blocked(addr); blocked {
		s.tooMany(w, r, wait)
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	c := callerOf(r.Context())
	pw, err := s.db.PasswordOf(r.Context(), c.user.ID)
	switch {
	case err != nil:
		s.internal(w, r, err)
		return
	case pw == nil:
		s.fail(w, r, http.StatusForbidden, "forbidden", "you sign in by pairing only; an admin sets a first password")
		return
	case !auth.CheckPassword(req.Current, *pw):
		s.logins.Fail(addr)
		s.fail(w, r, http.StatusUnauthorized, "wrong_login", "the current password is wrong")
		return
	}
	hashed, err := auth.HashPassword(req.New)
	if errors.Is(err, auth.ErrWeakPassword) {
		s.fail(w, r, http.StatusBadRequest, "weak_password", "a password has at least ten characters")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.db.SetPassword(r.Context(), c.user.ID, hashed); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMyDevices is GET /auth/me/devices.
func (s *Server) handleMyDevices(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r.Context())
	list, err := s.db.Devices(r.Context(), c.user.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []myDeviceBody{}
	for _, d := range list {
		out = append(out, myDeviceJSON(d, c.tokenID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// handleSignOutDevice is DELETE /auth/me/devices/{id}: signs one of the
// user's devices out.
func (s *Server) handleSignOutDevice(w http.ResponseWriter, r *http.Request) {
	id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := db.ErrNotFound
	if perr == nil {
		err = s.db.SignOut(r.Context(), callerOf(r.Context()).user.ID, id)
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such device of yours")
	default:
		s.internal(w, r, err)
	}
}

// handleAdminUsers is GET /admin/users (docs/04-api.md, section 8.6).
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.Users(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type row struct {
		userBody
		HasPassword  bool   `json:"has_password"`
		Devices      int    `json:"devices"`
		LastActiveAt *int64 `json:"last_active_at"`
	}
	out := []row{}
	for _, u := range list {
		out = append(out, row{userJSON(u.User), u.HasPassword, u.Devices, int64OrNil(u.LastActiveAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}
