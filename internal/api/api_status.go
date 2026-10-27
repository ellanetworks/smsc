package api

import (
	"net/http"

	"github.com/ellanetworks/smsc/version"
)

type Status struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	// Initialized is whether the first user exists, which the UI needs before a login.
	Initialized bool `json:"initialized"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Failed to get status", err, cfg.Logger)
			return
		}

		v := version.Get()
		writeResponse(w, Status{Version: v.Version, Revision: v.Revision, Initialized: n > 0}, http.StatusOK, cfg.Logger)
	})
}
