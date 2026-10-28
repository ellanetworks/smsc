package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/ellanetworks/smsc/internal/settings"
)

// Peer is a Diameter agent or node that the SMSC connects to, such as an IPX DEA or a DRA in front of the HSS, and
// the realms it routes to.
type Peer struct {
	ID        int64       `json:"id"`
	Host      string      `json:"host"`
	Addresses []string    `json:"addresses"`
	Port      int         `json:"port"`
	Routes    []PeerRoute `json:"routes"`
	Status    *PeerStatus `json:"status,omitempty"`
}

// PeerRoute is a route of a peer: to a realm, or by default to the foreign realms that no other route serves.
type PeerRoute struct {
	Realm       string `json:"realm,omitempty"`
	Default     bool   `json:"default,omitempty"`
	Application string `json:"application"`
	Priority    int    `json:"priority"`
}

// PeerStatus is the connection to a peer, and the host and realm the peer gave.
type PeerStatus struct {
	State string `json:"state"`
	Host  string `json:"host,omitempty"`
	Realm string `json:"realm,omitempty"`
	Since string `json:"since"`
	Error string `json:"error,omitempty"`
}

type PeerParams struct {
	Host      string      `json:"host"`
	Addresses []string    `json:"addresses"`
	Port      int         `json:"port"`
	Routes    []PeerRoute `json:"routes"`
}

// maxPeerBody bounds a peer, which may route to many realms.
const maxPeerBody = 64 << 10

type ListPeersResponse struct {
	Items []Peer `json:"items"`
}

func ListPeers(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := ListPeersResponse{Items: []Peer{}}

		for _, p := range cfg.Settings.Get().Peers {
			resp.Items = append(resp.Items, withStatus(cfg, peerResponse(p)))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetPeer(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := lookupPeer(w, r, cfg); ok {
			writeResponse(w, withStatus(cfg, peerResponse(p)), http.StatusOK, cfg.Logger)
		}
	})
}

func CreatePeer(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := decodePeer(w, r, cfg)
		if !ok {
			return
		}

		p, err := cfg.Settings.CreatePeer(r.Context(), p)
		if !peerSaved(w, err, cfg, "create") {
			return
		}

		principal, _ := PrincipalFrom(r.Context())
		a.audit(r, "create_diameter_peer", principal.Email, fmt.Sprintf("Diameter peer %d created", p.ID))
		writeResponse(w, peerResponse(p), http.StatusCreated, cfg.Logger)
	})
}

func UpdatePeer(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := peerID(w, r, cfg)
		if !ok {
			return
		}

		p, ok := decodePeer(w, r, cfg)
		if !ok {
			return
		}

		p.ID = id

		if !peerSaved(w, cfg.Settings.UpdatePeer(r.Context(), p), cfg, "update") {
			return
		}

		principal, _ := PrincipalFrom(r.Context())
		a.audit(r, "update_diameter_peer", principal.Email, fmt.Sprintf("Diameter peer %d updated", id))

		// The peer as stored, as GET returns it.
		if stored, ok := findPeer(cfg, id); ok {
			p = stored
		}

		writeResponse(w, peerResponse(p), http.StatusOK, cfg.Logger)
	})
}

func DeletePeer(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := peerID(w, r, cfg)
		if !ok {
			return
		}

		if !peerSaved(w, cfg.Settings.DeletePeer(r.Context(), id), cfg, "delete") {
			return
		}

		principal, _ := PrincipalFrom(r.Context())
		a.audit(r, "delete_diameter_peer", principal.Email, fmt.Sprintf("Diameter peer %d deleted", id))
		writeResponse(w, SuccessResponse{Message: "Peer deleted successfully"}, http.StatusOK, cfg.Logger)
	})
}

func peerID(w http.ResponseWriter, r *http.Request, cfg Config) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid peer ID", err, cfg.Logger)
		return 0, false
	}

	return id, true
}

func lookupPeer(w http.ResponseWriter, r *http.Request, cfg Config) (settings.Peer, bool) {
	id, ok := peerID(w, r, cfg)
	if !ok {
		return settings.Peer{}, false
	}

	p, ok := findPeer(cfg, id)
	if !ok {
		writeError(w, http.StatusNotFound, "Peer not found", nil, cfg.Logger)
	}

	return p, ok
}

func findPeer(cfg Config, id int64) (settings.Peer, bool) {
	for _, p := range cfg.Settings.Get().Peers {
		if p.ID == id {
			return p, true
		}
	}

	return settings.Peer{}, false
}

// decodePeer reads and validates the peer of a request, or writes the error.
func decodePeer(w http.ResponseWriter, r *http.Request, cfg Config) (settings.Peer, bool) {
	var params PeerParams

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPeerBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
		return settings.Peer{}, false
	}

	p := settings.Peer{Host: params.Host, Port: params.Port, Routes: []settings.Route{}}
	if p.Port == 0 {
		p.Port = settings.DefaultPort
	}

	for _, s := range params.Addresses {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("address %q must be an IP address", s), nil, cfg.Logger)
			return settings.Peer{}, false
		}

		p.Addresses = append(p.Addresses, a.Unmap())
	}

	for _, rt := range params.Routes {
		p.Routes = append(p.Routes, settings.Route{
			Realm:       rt.Realm,
			Default:     rt.Default,
			Application: rt.Application,
			Priority:    rt.Priority,
		})
	}

	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), nil, cfg.Logger)
		return settings.Peer{}, false
	}

	return p, true
}

// peerSaved reports whether a change of peer succeeded, or writes its error.
func peerSaved(w http.ResponseWriter, err error, cfg Config, action string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, settings.ErrNotFound):
		writeError(w, http.StatusNotFound, "Peer not found", nil, cfg.Logger)
	case errors.Is(err, settings.ErrConflict):
		writeError(w, http.StatusConflict, err.Error(), nil, cfg.Logger)
	default:
		writeError(w, http.StatusInternalServerError, "Failed to "+action+" peer", err, cfg.Logger)
	}

	return false
}

// peerResponse is a peer as stored. The SMSC applies a change of peer after it
// is stored, so only a read returns the connection to the peer.
func peerResponse(p settings.Peer) Peer {
	resp := Peer{ID: p.ID, Host: p.Host, Addresses: []string{}, Port: p.Port, Routes: []PeerRoute{}}

	for _, a := range p.Addresses {
		resp.Addresses = append(resp.Addresses, a.String())
	}

	for _, r := range p.Routes {
		resp.Routes = append(resp.Routes, PeerRoute{
			Realm:       r.Realm,
			Default:     r.Default,
			Application: r.Application,
			Priority:    r.Priority,
		})
	}

	return resp
}

// withStatus adds the connection to a peer.
func withStatus(cfg Config, p Peer) Peer {
	if st, ok := cfg.Diameter.Peer(settings.Peer{ID: p.ID}.NodeID()); ok {
		p.Status = &PeerStatus{
			State: st.State.String(),
			Host:  st.Host,
			Realm: st.Realm,
			Since: formatTime(st.Since),
			Error: st.LastError,
		}
	}

	return p
}
