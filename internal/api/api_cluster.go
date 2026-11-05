package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
)

type Cluster interface {
	Status(ctx context.Context) (cluster.Status, error)
	MintToken(ctx context.Context, ttl time.Duration) (cluster.Token, error)
	Join(ctx context.Context, token string) error
	RemoveMember(ctx context.Context, nodeID string) error
	SetDiameterLabel(ctx context.Context, nodeID, label string) error
}

const (
	defaultTokenTTL = 30 * time.Minute
	maxJoinBody     = 16 << 10
)

type CreateJoinTokenParams struct {
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

type JoinClusterParams struct {
	Token string `json:"token"`
}

type UpdateMemberParams struct {
	DiameterLabel string `json:"diameter_label"`
}

func noCluster(w http.ResponseWriter, r *http.Request, cfg Config) bool {
	if cfg.Cluster != nil {
		return false
	}

	writeError(w, r, http.StatusServiceUnavailable, "The cluster is not available", nil, cfg.Logger)

	return true
}

func clusterError(w http.ResponseWriter, r *http.Request, cfg Config, message string, err error) {
	switch {
	case errors.Is(err, cluster.ErrNotClustered), errors.Is(err, cluster.ErrAlreadyMember):
		writeError(w, r, http.StatusConflict, err.Error(), err, cfg.Logger)
	case errors.Is(err, cluster.ErrInvalidJoin), errors.Is(err, db.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, err.Error(), err, cfg.Logger)
	case errors.Is(err, db.ErrTokenUsed), errors.Is(err, db.ErrTokenExpired), errors.Is(err, settings.ErrConflict):
		writeError(w, r, http.StatusConflict, err.Error(), err, cfg.Logger)
	case errors.Is(err, db.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "Member not found", err, cfg.Logger)
	case errors.Is(err, cluster.ErrJoinUnavailable):
		writeError(w, r, http.StatusBadGateway, err.Error(), err, cfg.Logger)
	case errors.Is(err, cluster.ErrRemoved):
		writeError(w, r, http.StatusGone, err.Error(), err, cfg.Logger)
	default:
		writeError(w, r, http.StatusInternalServerError, message, err, cfg.Logger)
	}
}

func GetCluster(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		st, err := cfg.Cluster.Status(r.Context())
		if err != nil {
			clusterError(w, r, cfg, "Failed to get the cluster status", err)
			return
		}

		writeResponse(w, r, st, http.StatusOK, cfg.Logger)
	})
}

func CreateJoinToken(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		var params CreateJoinTokenParams
		if r.ContentLength != 0 && !decodeBody(w, r, &params, cfg) {
			return
		}

		ttl := defaultTokenTTL
		if params.TTLSeconds != 0 {
			ttl = time.Duration(params.TTLSeconds) * time.Second
		}

		token, err := cfg.Cluster.MintToken(r.Context(), ttl)
		if err != nil {
			clusterError(w, r, cfg, "Failed to create a join token", err)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "create_join_token", p.Email, "Cluster join token created")
		writeResponse(w, r, token, http.StatusCreated, cfg.Logger)
	})
}

func JoinCluster(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		if !a.allowLogin(w, r) {
			return
		}

		var params JoinClusterParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		if params.Token == "" {
			writeError(w, r, http.StatusBadRequest, "token is required", nil, cfg.Logger)
			return
		}

		users, err := cfg.Auth.CountUsers(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to join the cluster", err, cfg.Logger)
			return
		}

		if users > 0 {
			writeError(w, r, http.StatusForbidden, "Only a node that is not initialized can join a cluster", nil,
				cfg.Logger)

			return
		}

		_, messages, err := cfg.Store.ListMessages(r.Context(), db.MessageFilter{}, 1, 1)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "Failed to join the cluster", err, cfg.Logger)
			return
		}

		if messages > 0 {
			writeError(w, r, http.StatusConflict, "This node holds messages: only an empty node can join a cluster", nil,
				cfg.Logger)

			return
		}

		if err := cfg.Cluster.Join(r.Context(), params.Token); err != nil {
			clusterError(w, r, cfg, "Failed to join the cluster", err)
			return
		}

		a.audit(r, "join_cluster", "", "Node joined a cluster")
		writeResponse(w, r, SuccessResponse{Message: "Joined the cluster"}, http.StatusOK, cfg.Logger)
	})
}

func UpdateMember(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		var params UpdateMemberParams
		if !decodeBody(w, r, &params, cfg) {
			return
		}

		id := r.PathValue("node_id")

		if err := cfg.Cluster.SetDiameterLabel(r.Context(), id, params.DiameterLabel); err != nil {
			clusterError(w, r, cfg, "Failed to update the member", err)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "update_member", p.Email, "Cluster member "+id+" updated")
		writeResponse(w, r, SuccessResponse{Message: "Member updated successfully"}, http.StatusOK, cfg.Logger)
	})
}

func RemoveMember(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noCluster(w, r, cfg) {
			return
		}

		id := r.PathValue("node_id")

		if err := cfg.Cluster.RemoveMember(r.Context(), id); err != nil {
			clusterError(w, r, cfg, "Failed to remove the member", err)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "remove_member", p.Email, "Cluster member "+id+" removed")
		writeResponse(w, r, SuccessResponse{Message: "Member removed successfully"}, http.StatusOK, cfg.Logger)
	})
}
