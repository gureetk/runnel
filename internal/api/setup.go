// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gureetk/runnel/internal/auth"
	"github.com/gureetk/runnel/internal/db"
	"github.com/gureetk/runnel/internal/model"
)

// setupTries wrong setup codes a minute from one address are allowed
// (P7).
const setupTries = 5

// serverName is the name clients show (docs/04-api.md, section 3.1): the
// environment's when it sets one, else the admin's setting, else the
// machine's host name.
func (s *Server) serverName(ctx context.Context) (string, error) {
	if s.info.NameFromEnv {
		return s.info.Name, nil
	}
	v, ok, err := s.db.Setting(ctx, "server_name")
	if err != nil || !ok {
		return s.info.Name, err
	}
	return v, nil
}

// handleSetup is POST /setup: the first run makes the first person, an
// admin, with the setup code (D35; docs/04-api.md, section 3.4).
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	addr := remoteIP(r).String()
	if blocked, wait := s.setupCodes.Blocked(addr); blocked {
		s.tooMany(w, r, wait)
		return
	}
	var req struct {
		Code     string     `json:"code"`
		Name     string     `json:"name"`
		Password string     `json:"password"`
		Device   deviceBody `json:"device"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	done, err := s.db.SetupDone(r.Context())
	switch {
	case err != nil:
		s.internal(w, r, err)
		return
	case done:
		s.fail(w, r, http.StatusConflict, "setup_done", "this server has its first person already")
		return
	case !validName(req.Name):
		s.fail(w, r, http.StatusBadRequest, "bad_request", "a name has 1 to 40 characters, no control characters, and no space at either end")
		return
	case req.Device.ID == "" || len(req.Device.ID) > 200 || len(req.Device.Name) > 100 || len(req.Device.Client) > 100:
		s.fail(w, r, http.StatusBadRequest, "bad_request", "device.id is needed, with at most 200 bytes; device.name and device.client have at most 100")
		return
	}
	code, ok := auth.CleanSetupCode(req.Code)
	if !ok {
		s.setupCodes.Fail(addr)
		s.fail(w, r, http.StatusForbidden, "wrong_code", "the setup code is wrong")
		return
	}
	pw, err := auth.HashPassword(req.Password)
	if errors.Is(err, auth.ErrWeakPassword) {
		s.fail(w, r, http.StatusBadRequest, "weak_password", err.Error())
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	token, hash := auth.NewToken()
	dev := model.Device{ID: req.Device.ID, Name: req.Device.Name, Client: req.Device.Client}
	u, err := s.db.Setup(r.Context(), code, req.Name, pw, dev, hash, s.now().UnixMilli())
	switch {
	case errors.Is(err, db.ErrSetupDone):
		s.fail(w, r, http.StatusConflict, "setup_done", "this server has its first person already")
	case errors.Is(err, db.ErrWrongCode):
		s.setupCodes.Fail(addr)
		s.fail(w, r, http.StatusForbidden, "wrong_code", "the setup code is wrong")
	case err != nil:
		s.internal(w, r, err)
	default:
		s.log.Info("the first person was made with the setup code", "user", u.ID)
		writeJSON(w, http.StatusOK, struct {
			Token string   `json:"token"`
			User  userBody `json:"user"`
		}{token, userJSON(u)})
	}
}

// handleSetupCode is GET /admin/setup-code, for runnel setup-code: the
// local admin token only.
func (s *Server) handleSetupCode(w http.ResponseWriter, r *http.Request) {
	if !callerOf(r.Context()).local {
		s.fail(w, r, http.StatusForbidden, "forbidden", "the setup code is for the server's own shell")
		return
	}
	code, err := s.db.SetupCode(r.Context(), auth.NewSetupCode)
	switch {
	case err != nil:
		s.internal(w, r, err)
	case code == "":
		s.fail(w, r, http.StatusConflict, "setup_done", "this server has its first person already: no setup code is needed")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"code": auth.ShowSetupCode(code)})
	}
}

type settingBody struct {
	Value  string `json:"value"`
	Locked bool   `json:"locked"`
}

// settings answers what GET and PATCH /admin/settings return.
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	name, err := s.serverName(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	key, err := s.keySetting(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_name": settingBody{Value: name, Locked: s.info.NameFromEnv},
		"tmdb_key":    key,
	})
}

// handleSettings is GET /admin/settings (docs/04-api.md, section 8.1).
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) { s.settings(w, r) }

// handlePatchSettings is PATCH /admin/settings: the server's name, or
// with null the host name again; the metadata key, checked with the
// provider before it is kept, or with null none. Everything asked is
// checked before anything is written.
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]*string
	if !s.decode(w, r, &req) {
		return
	}
	for k := range req {
		switch k {
		case "server_name", "tmdb_key":
		default:
			s.fail(w, r, http.StatusBadRequest, "bad_request", "the settings are server_name and tmdb_key")
			return
		}
	}
	name, setName := req["server_name"]
	if setName {
		if s.info.NameFromEnv {
			s.fail(w, r, http.StatusConflict, "locked", "RUNNEL_NAME sets the server's name")
			return
		}
		if name != nil && !validName(*name) {
			s.fail(w, r, http.StatusBadRequest, "bad_request", "a server name has 1 to 40 characters, no control characters, and no space at either end")
			return
		}
	}
	key, setKey := req["tmdb_key"]
	if setKey {
		if s.envKey != "" {
			s.fail(w, r, http.StatusConflict, "locked", "RUNNEL_TMDB_KEY sets the metadata key")
			return
		}
		if key != nil && !s.checkKey(w, r, *key) {
			return
		}
	}
	if setName {
		if err := s.db.SetSetting(r.Context(), "server_name", name); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if setKey {
		if err := s.db.SetMetadataKey(r.Context(), key, s.now().UnixMilli()); err != nil {
			s.internal(w, r, err)
			return
		}
		if s.metadata != nil {
			s.metadata.Forget()
		}
		if key != nil {
			s.log.Info("the metadata key was changed")
		} else {
			s.log.Info("the metadata key was removed")
		}
	}
	s.settings(w, r)
}
