// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
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

type adminUserBody struct {
	userBody
	HasPassword  bool   `json:"has_password"`
	Devices      int    `json:"devices"`
	LastActiveAt *int64 `json:"last_active_at"`
}

func adminUserJSON(u db.UserRow) adminUserBody {
	return adminUserBody{userJSON(u.User), u.HasPassword, u.Devices, int64OrNil(u.LastActiveAt)}
}

// handleAdminUsers is GET /admin/users (docs/04-api.md, section 8.6).
func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.Users(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []adminUserBody{}
	for _, u := range list {
		out = append(out, adminUserJSON(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// pathID reads the {id} of the path, or answers 404 with what it names.
func (s *Server) pathID(w http.ResponseWriter, r *http.Request, what string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, r, http.StatusNotFound, "not_found", "no such "+what)
		return 0, false
	}
	return id, true
}

// handleChangeUser is PATCH /admin/users/{id}: a new name, a new password
// or none, and admin or not, all of it or none (docs/04-api.md, section
// 8.6). It returns the person as GET /admin/users lists them.
func (s *Server) handleChangeUser(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "user")
	if !ok {
		return
	}
	var req map[string]json.RawMessage
	if !s.decode(w, r, &req) {
		return
	}
	var c db.UserChange
	for k, raw := range req {
		switch k {
		case "name":
			if json.Unmarshal(raw, &c.Name) != nil || c.Name == nil || !validName(*c.Name) {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "a name has 1 to 40 characters, no control characters, and no space at either end")
				return
			}
		case "password":
			var pw *string
			if json.Unmarshal(raw, &pw) != nil {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "password is a string, or null for none")
				return
			}
			if pw == nil {
				c.NoPassword = true
				continue
			}
			hashed, err := auth.HashPassword(*pw)
			if errors.Is(err, auth.ErrWeakPassword) {
				s.fail(w, r, http.StatusBadRequest, "weak_password", "a password has at least ten characters")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			c.Password = &hashed
		case "is_admin":
			if json.Unmarshal(raw, &c.IsAdmin) != nil || c.IsAdmin == nil {
				s.fail(w, r, http.StatusBadRequest, "bad_request", "is_admin is true or false")
				return
			}
		default:
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a person's changes are name, password and is_admin")
			return
		}
	}
	switch err := s.db.ChangeUser(r.Context(), id, c); {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such user")
		return
	case errors.Is(err, db.ErrNameTaken):
		s.fail(w, r, http.StatusConflict, "conflict", "a user with that name exists")
		return
	case errors.Is(err, db.ErrLastAdmin):
		s.fail(w, r, http.StatusConflict, "last_admin", "the server would have no admin")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	list, err := s.db.Users(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, u := range list {
		if u.ID == id {
			writeJSON(w, http.StatusOK, adminUserJSON(u))
			return
		}
	}
	s.fail(w, r, http.StatusNotFound, "not_found", "no such user")
}

// handleDeleteUser is DELETE /admin/users/{id}: the person goes with
// their progress, their list and their sign-ins.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "user")
	if !ok {
		return
	}
	switch err := s.db.DeleteUser(r.Context(), id); {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such user")
	case errors.Is(err, db.ErrLastAdmin):
		s.fail(w, r, http.StatusConflict, "last_admin", "the server would have no admin")
	case err != nil:
		s.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleAdminTokens is GET /admin/tokens: every signed-in device, one
// entry for each person on a device, shaped as GET /auth/me/devices
// shapes them, with the person.
func (s *Server) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.AllDevices(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type ref struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	type row struct {
		myDeviceBody
		User ref `json:"user"`
	}
	current := callerOf(r.Context()).tokenID
	out := []row{}
	for _, d := range list {
		out = append(out, row{myDeviceJSON(d.Device, current), ref{d.UserID, d.UserName}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// handleRevokeToken is DELETE /admin/tokens/{id}: signs a device out.
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathID(w, r, "device")
	if !ok {
		return
	}
	switch err := s.db.RevokeToken(r.Context(), id); {
	case errors.Is(err, db.ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "not_found", "no such device")
	case err != nil:
		s.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
