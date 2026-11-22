// SPDX-License-Identifier: GPL-3.0-or-later

package api

import "net/http"

// serverBody is GET /server (docs/04-api.md, section 3.1).
type serverBody struct {
	Product    string        `json:"product"`
	Name       string        `json:"name"`
	Version    string        `json:"version"`
	InstanceID string        `json:"instance_id"`
	SetupDone  bool          `json:"setup_done"`
	API        apiBody       `json:"api"`
	Transcode  transcodeBody `json:"transcode"`
}

type apiBody struct {
	Version  int      `json:"version"`
	Features []string `json:"features"`
}

type transcodeBody struct {
	HW      string   `json:"hw"`
	Video   []string `json:"video"`
	Depth10 []string `json:"depth10"`
	Slots   int      `json:"slots"`
}

// handleServer needs no token: it is what a client reads first, to check
// that an address is a Runnel server.
func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	done, err := s.db.SetupDone(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	name, err := s.serverName(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, serverBody{
		Product:    "runnel",
		Name:       name,
		Version:    s.info.Version,
		InstanceID: s.info.InstanceID,
		SetupDone:  done,
		API: apiBody{
			Version: 1,
			// A feature is listed once this server implements it.
			Features: []string{"changes", "list", "pairing", "prepare"},
		},
		// This server encodes nothing yet, so it lists nothing a client
		// could ask for.
		Transcode: transcodeBody{HW: "none", Video: []string{}, Depth10: []string{}, Slots: 0},
	})
}

// healthPath is outside the API, for a supervisor's health check
// (docs/04-api.md, section 8.7).
const healthPath = "/healthz"

// handleHealth needs no token and reads nothing: it says that the server is
// up and answering.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok\n"))
}
