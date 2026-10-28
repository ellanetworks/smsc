package api

import (
	"net/http"
	"strconv"

	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/smsc/internal/settings"
)

type DiameterPeer struct {
	// ID is the ID of a configured peer, and zero for a peer that connected to the SMSC.
	ID           int64    `json:"id,omitempty"`
	Configured   bool     `json:"configured"`
	Host         string   `json:"host"`
	Realm        string   `json:"realm"`
	Address      string   `json:"address"`
	State        string   `json:"state"`
	Applications []string `json:"applications"`
	Since        string   `json:"since"`
	Error        string   `json:"error,omitempty"`
}

// DiameterRoute is a route, with an empty realm for a default route.
type DiameterRoute struct {
	Realm       string   `json:"realm"`
	Default     bool     `json:"default"`
	Application string   `json:"application"`
	Peers       []string `json:"peers"`
}

type DiameterStatus struct {
	Host   string          `json:"host"`
	Realm  string          `json:"realm"`
	Peers  []DiameterPeer  `json:"peers"`
	Routes []DiameterRoute `json:"routes"`
}

func GetDiameterStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		identity := cfg.Diameter.Identity()

		resp := DiameterStatus{
			Host:   identity.OriginHost,
			Realm:  identity.OriginRealm,
			Peers:  []DiameterPeer{},
			Routes: []DiameterRoute{},
		}

		for _, r := range cfg.Diameter.Routes() {
			route := DiameterRoute{
				Realm:       r.Realm,
				Default:     r.Default,
				Application: applicationName(r.ApplicationID),
				Peers:       []string{},
			}

			route.Peers = append(route.Peers, r.Peers...)
			resp.Routes = append(resp.Routes, route)
		}

		for _, p := range cfg.Diameter.Peers() {
			peer := DiameterPeer{
				Configured:   p.Configured,
				Host:         p.Host,
				Realm:        p.Realm,
				State:        p.State.String(),
				Applications: []string{},
				Since:        formatTime(p.Since),
				Error:        p.LastError,
			}

			if p.Configured {
				peer.ID, _ = settings.PeerID(p.ID)
			}

			if p.RemoteAddr.IsValid() {
				peer.Address = p.RemoteAddr.Unmap().String()
			}

			for _, a := range p.Applications {
				peer.Applications = append(peer.Applications, applicationName(a.ID))
			}

			resp.Peers = append(resp.Peers, peer)
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func applicationName(id uint32) string {
	switch id {
	case s6c.ApplicationID:
		return "s6c"
	case sgd.ApplicationID:
		return "sgd"
	default:
		return strconv.FormatUint(uint64(id), 10)
	}
}
