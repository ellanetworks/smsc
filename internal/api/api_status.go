package api

import (
	"net/http"

	"github.com/ellanetworks/smsc/version"
)

type Status struct {
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	Initialized bool   `json:"initialized"`
	Clustered   bool   `json:"clustered"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to get status", err, cfg.Logger)
			return
		}

		v := version.Get()
		st := Status{
			Version: v.Version, Revision: v.Revision, Initialized: n > 0,
			Clustered: cfg.Cluster != nil && cfg.Cluster.Clustered(),
		}
		writeResponse(w, r, st, http.StatusOK, cfg.Logger)
	})
}
