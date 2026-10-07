package api

import (
	"net/http"

	"github.com/ellanetworks/smsc/version"
)

type Status struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

func GetStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		v := version.Get()
		writeResponse(w, Status{Version: v.Version, Revision: v.Revision}, http.StatusOK, cfg.Logger)
	})
}
