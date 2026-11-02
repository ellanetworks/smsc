package api

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/ellanetworks/smsc/internal/ratelimit"
)

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

func clientKey(r *http.Request) string {
	addr, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return clientIP(r)
	}

	return ratelimit.Key(addr)
}
