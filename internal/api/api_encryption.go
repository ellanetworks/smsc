package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
)

type Encryption interface {
	RotateKeys(ctx context.Context) (cluster.EncryptionStatus, error)
}

func noEncryption(w http.ResponseWriter, r *http.Request, cfg Config) bool {
	if cfg.Encryption != nil {
		return false
	}

	writeError(w, r, http.StatusServiceUnavailable, "Encryption at rest is not available", nil, cfg.Logger)

	return true
}

func RotateEncryptionKeys(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noEncryption(w, r, cfg) {
			return
		}

		status, err := cfg.Encryption.RotateKeys(r.Context())
		if notLeader(w, r, cfg, err) {
			return
		}

		switch {
		case errors.Is(err, db.ErrKeyChange):
			writeError(w, r, http.StatusConflict, "A cluster key change is in progress: rotate the keys after it",
				err, cfg.Logger)

			return
		case errors.Is(err, db.ErrKeysChanged):
			writeError(w, r, http.StatusConflict, "The encryption keys changed meanwhile, retry", err, cfg.Logger)
			return
		case errors.Is(err, cluster.ErrEncryptionLocked):
			writeError(w, r, http.StatusServiceUnavailable, "The encryption keys are locked", err, cfg.Logger)
			return
		case err != nil:
			writeError(w, r, http.StatusInternalServerError, "Failed to rotate the encryption keys", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "rotate_encryption_keys", p.Email, fmt.Sprintf("Encryption keys rotated to term %d", status.ActiveTerm))
		writeResponse(w, r, SuccessResponse{Message: "Encryption keys rotated"}, http.StatusOK, cfg.Logger)
	})
}
