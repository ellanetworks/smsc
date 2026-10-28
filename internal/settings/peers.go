package settings

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// DefaultPort is the Diameter port of a peer that does not name one (RFC 6733 section 2.1).
const DefaultPort = 3868

const (
	ApplicationS6c = "s6c"
	ApplicationSGd = "sgd"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict is a peer that clashes with another peer, or with the SMSC itself.
	ErrConflict = errors.New("conflict")
)

// Peer is a Diameter agent or node that the SMSC connects to, and the realms it routes to (RFC 6733 sections 2.6
// and 2.7). The SMSC keeps accepting the connections of the cores and HSSs that are not peers.
type Peer struct {
	ID int64
	// Host is the peer's Diameter identity: when set, the peer must give it as its Origin-Host.
	Host string
	// Addresses are the peer's SCTP addresses, more than one for a multi-homed peer.
	Addresses []netip.Addr
	Port      int
	Routes    []Route
}

// Route sends the requests of an application for a realm to a peer. A default route takes the realms that no other
// route or connected peer serves, other than the SMSC's own (RFC 6733 section 2.7).
type Route struct {
	// Realm is empty for a default route.
	Realm       string
	Default     bool
	Application string
	// Priority orders the peers of a route, lowest first; peers of equal priority share the requests.
	Priority int
}

// NodeID is the ID of the peer in the Diameter node.
func (p Peer) NodeID() string { return "peer-" + strconv.FormatInt(p.ID, 10) }

// PeerID returns the ID of the configured peer whose NodeID is nodeID.
func PeerID(nodeID string) (int64, bool) {
	rest, ok := strings.CutPrefix(nodeID, "peer-")
	if !ok {
		return 0, false
	}

	id, err := strconv.ParseInt(rest, 10, 64)

	return id, err == nil
}

func (p Peer) Validate() error {
	if p.Host != "" && !isDiameterIdentity(p.Host) {
		return fmt.Errorf("host %q must be a Diameter identity such as dea1.ipx.example.net", p.Host)
	}

	if len(p.Addresses) == 0 {
		return errors.New("addresses must list at least one address")
	}

	// An IPv4-mapped IPv6 address is the IPv4 address, which is how the peer is stored and dialled.
	for i, a := range p.Addresses {
		if a = a.Unmap(); !a.IsValid() || a.IsUnspecified() || a.IsMulticast() {
			return fmt.Errorf("address %v must be a unicast IP address", a)
		}

		if slices.ContainsFunc(p.Addresses[:i], func(o netip.Addr) bool { return o.Unmap() == a }) {
			return fmt.Errorf("address %v is listed twice", a)
		}
	}

	if p.Port < 1 || p.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}

	for i, r := range p.Routes {
		if err := r.Validate(); err != nil {
			return err
		}

		if slices.ContainsFunc(p.Routes[:i], r.sameKey) {
			if r.Default {
				return fmt.Errorf("the default %s route is listed twice", r.Application)
			}

			return fmt.Errorf("the %s route for %s is listed twice", r.Application, r.Realm)
		}
	}

	return nil
}

func (r Route) Validate() error {
	switch {
	case r.Application != ApplicationS6c && r.Application != ApplicationSGd:
		return fmt.Errorf("application %q must be s6c or sgd", r.Application)
	case r.Default && r.Realm != "":
		return errors.New("a default route takes no realm")
	// S6c goes to the SMSC's own realm, which a default route never covers.
	case r.Default && r.Application != ApplicationSGd:
		return errors.New("only sgd takes a default route")
	case !r.Default && !isDiameterIdentity(r.Realm):
		return fmt.Errorf("realm %q must be a Diameter realm such as epc.mnc001.mcc001.3gppnetwork.org", r.Realm)
	case r.Priority < 0:
		return errors.New("priority must not be negative")
	}

	return nil
}

func (r Route) sameKey(o Route) bool {
	return r.Application == o.Application && r.Default == o.Default && strings.EqualFold(r.Realm, o.Realm)
}

// Equal reports whether p and q are the same peer, with the same configuration.
func (p Peer) Equal(q Peer) bool {
	return p.ID == q.ID && p.Host == q.Host && slices.Equal(p.Addresses, q.Addresses) && p.Port == q.Port &&
		slices.Equal(p.Routes, q.Routes)
}

// conflict is why p cannot join peers, the other peers of the SMSC whose host is self.
func (p Peer) conflict(peers []Peer, self string) error {
	if strings.EqualFold(p.Host, self) {
		return fmt.Errorf("%w: host %s is the SMSC's own", ErrConflict, p.Host)
	}

	for _, o := range peers {
		switch {
		case o.ID == p.ID:
		case p.Host != "" && strings.EqualFold(o.Host, p.Host):
			return fmt.Errorf("%w: peer %d has the host %s", ErrConflict, o.ID, p.Host)
		// The SMSC tells a peer without a host by its address.
		case p.Host == "" && o.Host == "":
			for _, a := range p.Addresses {
				if slices.Contains(o.Addresses, a) {
					return fmt.Errorf("%w: peer %d without a host has the address %v", ErrConflict, o.ID, a)
				}
			}
		}
	}

	return nil
}

// isDiameterIdentity reports whether s is a DiameterIdentity: an FQDN (RFC 6733 section 4.3.1).
func isDiameterIdentity(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}

	for label := range strings.SplitSeq(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}

	return true
}

type PeerStore interface {
	CreatePeer(ctx context.Context, p Peer) (int64, error)
	UpdatePeer(ctx context.Context, p Peer) error
	DeletePeer(ctx context.Context, id int64) error
}

// CreatePeer adds a peer and returns it with its ID.
func (l *Live) CreatePeer(ctx context.Context, p Peer) (Peer, error) {
	p.ID = 0

	if err := p.Validate(); err != nil {
		return Peer{}, err
	}

	p = clonePeer(p)

	err := l.change(func(s *Settings) error {
		if err := p.conflict(s.Peers, s.Operator.Host()); err != nil {
			return err
		}

		id, err := l.store.CreatePeer(ctx, p)
		if err != nil {
			return fmt.Errorf("save settings: %w", err)
		}

		p.ID = id
		s.Peers = append(slices.Clone(s.Peers), p)

		return nil
	})

	return p, err
}

// UpdatePeer replaces the peer of p.ID.
func (l *Live) UpdatePeer(ctx context.Context, p Peer) error {
	if err := p.Validate(); err != nil {
		return err
	}

	p = clonePeer(p)

	return l.change(func(s *Settings) error {
		i := slices.IndexFunc(s.Peers, func(o Peer) bool { return o.ID == p.ID })
		if i < 0 {
			return fmt.Errorf("peer %d: %w", p.ID, ErrNotFound)
		}

		if err := p.conflict(s.Peers, s.Operator.Host()); err != nil {
			return err
		}

		if err := l.store.UpdatePeer(ctx, p); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}

		s.Peers = slices.Clone(s.Peers)
		s.Peers[i] = p

		return nil
	})
}

func (l *Live) DeletePeer(ctx context.Context, id int64) error {
	return l.change(func(s *Settings) error {
		i := slices.IndexFunc(s.Peers, func(o Peer) bool { return o.ID == id })
		if i < 0 {
			return fmt.Errorf("peer %d: %w", id, ErrNotFound)
		}

		if err := l.store.DeletePeer(ctx, id); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}

		s.Peers = slices.Delete(slices.Clone(s.Peers), i, i+1)

		return nil
	})
}

func clonePeer(p Peer) Peer {
	p.Addresses = slices.Clone(p.Addresses)
	p.Routes = slices.Clone(p.Routes)

	for i, a := range p.Addresses {
		p.Addresses[i] = a.Unmap()
	}

	return p
}
