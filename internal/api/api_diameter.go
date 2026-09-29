package api

import (
	"net/http"
	"strconv"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
)

type DiameterPeer struct {
	Host         string   `json:"host"`
	Realm        string   `json:"realm"`
	Address      string   `json:"address"`
	State        string   `json:"state"`
	Applications []string `json:"applications"`
	Since        string   `json:"since"`
}

type DiameterStatus struct {
	Host         string         `json:"host"`
	Realm        string         `json:"realm"`
	HSSAvailable bool           `json:"hss_available"`
	Peers        []DiameterPeer `json:"peers"`
}

func GetDiameterStatus(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		identity := cfg.Diameter.Identity()

		resp := DiameterStatus{
			Host:         identity.OriginHost,
			Realm:        identity.OriginRealm,
			HSSAvailable: cfg.Diameter.HSSAvailable(),
			Peers:        []DiameterPeer{},
		}

		for _, p := range cfg.Diameter.Peers() {
			peer := DiameterPeer{
				Host:         p.Host,
				Realm:        p.Realm,
				State:        p.State.String(),
				Applications: []string{},
				Since:        formatTime(p.Since),
			}

			if p.RemoteAddr.IsValid() {
				peer.Address = p.RemoteAddr.Unmap().String()
			}

			for _, a := range p.Applications {
				peer.Applications = append(peer.Applications, applicationName(a))
			}

			resp.Peers = append(resp.Peers, peer)
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func applicationName(a diameter.Application) string {
	switch a.ID {
	case s6c.ApplicationID:
		return "s6c"
	case sgd.ApplicationID:
		return "sgd"
	default:
		return strconv.FormatUint(uint64(a.ID), 10)
	}
}
