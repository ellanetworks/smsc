package api

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/ellanetworks/smsc/internal/ratelimit"
)

// clientIP is the address of the direct peer of a request. X-Forwarded-For is not trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// clientKey is the key of the client of a request for rate limiting: its IPv4 address, or the /64 of its IPv6
// address, since a single host commonly has a whole /64.
func clientKey(r *http.Request) string {
	addr, err := netip.ParseAddr(clientIP(r))
	if err != nil {
		return clientIP(r)
	}

	return ratelimit.Key(addr)
}
