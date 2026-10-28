package settings

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

const (
	visitedRealm = "epc.mnc002.mcc208.3gppnetwork.org"
	homeRealm    = "epc.mnc001.mcc001.3gppnetwork.org"
)

func validPeer() Peer {
	return Peer{
		Host:      "dea1.ipx.example.net",
		Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::10")},
		Port:      DefaultPort,
		Routes: []Route{
			{Default: true, Application: ApplicationSGd},
			{Realm: visitedRealm, Application: ApplicationSGd, Priority: 1},
			{Realm: homeRealm, Application: ApplicationS6c},
		},
	}
}

func TestPeerValidate(t *testing.T) {
	tests := map[string]func(*Peer){
		"invalid host":        func(p *Peer) { p.Host = "dea1..example.net" },
		"no address":          func(p *Peer) { p.Addresses = nil },
		"invalid address":     func(p *Peer) { p.Addresses = []netip.Addr{{}} },
		"unspecified address": func(p *Peer) { p.Addresses = []netip.Addr{netip.IPv4Unspecified()} },
		"mapped unspecified":  func(p *Peer) { p.Addresses = []netip.Addr{netip.MustParseAddr("::ffff:0.0.0.0")} },
		"mapped address twice": func(p *Peer) {
			p.Addresses = []netip.Addr{netip.MustParseAddr("::ffff:192.0.2.10"), netip.MustParseAddr("192.0.2.10")}
		},
		"multicast address":    func(p *Peer) { p.Addresses = []netip.Addr{netip.MustParseAddr("224.0.0.1")} },
		"address twice":        func(p *Peer) { p.Addresses = append(p.Addresses, p.Addresses[0]) },
		"zero port":            func(p *Peer) { p.Port = 0 },
		"large port":           func(p *Peer) { p.Port = 65536 },
		"unknown application":  func(p *Peer) { p.Routes[1].Application = "s6a" },
		"no realm":             func(p *Peer) { p.Routes[1].Realm = "" },
		"invalid realm":        func(p *Peer) { p.Routes[1].Realm = "-epc.example.net" },
		"default with a realm": func(p *Peer) { p.Routes[0].Realm = visitedRealm },
		"default s6c":          func(p *Peer) { p.Routes[0].Application = ApplicationS6c },
		"negative priority":    func(p *Peer) { p.Routes[1].Priority = -1 },
		"route twice": func(p *Peer) {
			p.Routes = append(p.Routes, Route{Realm: "EPC.mnc002.mcc208.3gppnetwork.org", Application: ApplicationSGd})
		},
		"default route twice": func(p *Peer) {
			p.Routes = append(p.Routes, Route{Default: true, Application: ApplicationSGd, Priority: 3})
		},
		"host with a port":       func(p *Peer) { p.Host = "dea1.ipx.example.net:3868" },
		"host with a scheme":     func(p *Peer) { p.Host = "aaa://dea1.ipx.example.net" },
		"realm with a wildcard":  func(p *Peer) { p.Routes[1].Realm = "*.3gppnetwork.org" },
		"realm with whitespaces": func(p *Peer) { p.Routes[1].Realm = " " + visitedRealm },
	}

	if err := validPeer().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			p := validPeer()
			edit(&p)

			if err := p.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	hostless := validPeer()
	hostless.Host, hostless.Routes = "", nil

	if err := hostless.Validate(); err != nil {
		t.Fatalf("a peer without a host or routes: %v", err)
	}
}

func TestSettingsValidatePeers(t *testing.T) {
	s := validSettings()
	s.Peers = []Peer{validPeer(), validPeer()}
	s.Peers[1].ID = 1

	if err := s.Validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("two peers with one host: %v", err)
	}

	s.Peers[1].Host = "dea2.ipx.example.net"

	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLivePeers(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings())
	ctx := context.Background()
	changed := live.Changed()

	dea1, err := live.CreatePeer(ctx, validPeer())
	if err != nil || dea1.ID != 1 {
		t.Fatalf("CreatePeer = %+v, %v", dea1, err)
	}

	select {
	case <-changed:
	default:
		t.Fatal("Changed was not signalled")
	}

	dea2 := validPeer()
	if _, err := live.CreatePeer(ctx, dea2); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreatePeer with the host of another = %v", err)
	}

	dea2.Host = "dea2.ipx.example.net"

	if dea2, err = live.CreatePeer(ctx, dea2); err != nil || dea2.ID != 2 {
		t.Fatalf("CreatePeer = %+v, %v", dea2, err)
	}

	if got := live.Get().Peers; len(got) != 2 || !got[0].Equal(dea1) || !got[1].Equal(dea2) {
		t.Fatalf("Peers = %+v", got)
	}

	dea2.Routes = dea2.Routes[:1]
	if err := live.UpdatePeer(ctx, dea2); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}

	if got := live.Get().Peers[1]; len(got.Routes) != 1 {
		t.Fatalf("Peer after update = %+v", got)
	}

	dea2.Host = dea1.Host
	if err := live.UpdatePeer(ctx, dea2); !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdatePeer to the host of another = %v", err)
	}

	if err := live.UpdatePeer(ctx, Peer{ID: 9, Addresses: dea1.Addresses, Port: DefaultPort}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePeer of an unknown peer = %v", err)
	}

	if err := live.DeletePeer(ctx, 1); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	if err := live.DeletePeer(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePeer twice = %v", err)
	}

	if got := live.Get().Peers; len(got) != 1 || got[0].ID != 2 || got[0].Host != "dea2.ipx.example.net" {
		t.Fatalf("Peers after delete = %+v", got)
	}
}

func TestLivePeerConflicts(t *testing.T) {
	live := NewLive(&fakeStore{}, validSettings())
	ctx := context.Background()

	self := validPeer()
	self.Host = "SMSC.node." + homeRealm

	if _, err := live.CreatePeer(ctx, self); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreatePeer with the SMSC's host = %v", err)
	}

	a := validPeer()
	a.Host = ""

	if _, err := live.CreatePeer(ctx, a); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	b := validPeer()
	b.Host, b.Addresses = "", []netip.Addr{netip.MustParseAddr("198.51.100.1"), a.Addresses[1]}

	if _, err := live.CreatePeer(ctx, b); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreatePeer of two peers without a host on one address = %v", err)
	}

	// A peer with a host is told by its host, so it may share an address.
	b.Host = "dea2.ipx.example.net"

	if _, err := live.CreatePeer(ctx, b); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}
}

func TestLiveKeepsPeersOnFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("disk full")}
	live := NewLive(store, validSettings())
	changed := live.Changed()

	if _, err := live.CreatePeer(context.Background(), validPeer()); err == nil {
		t.Fatal("expected a storage error")
	}

	if got := live.Get().Peers; len(got) != 0 {
		t.Fatalf("Peers = %+v", got)
	}

	select {
	case <-changed:
		t.Fatal("Changed was signalled")
	default:
	}
}

func TestPeerID(t *testing.T) {
	if id, ok := PeerID(Peer{ID: 42}.NodeID()); !ok || id != 42 {
		t.Fatalf("PeerID = %d, %t", id, ok)
	}

	for _, nodeID := range []string{"", "peer-", "peer-x", "core.example.org"} {
		if _, ok := PeerID(nodeID); ok {
			t.Errorf("PeerID(%q) is a peer", nodeID)
		}
	}
}

func TestOperatorCannotTakeAPeersHost(t *testing.T) {
	store := &fakeStore{}
	live := NewLive(store, validSettings())
	ctx := context.Background()

	next := validOperator()
	next.MNC = "02"

	p := validPeer()
	p.Host = next.Host()

	if _, err := live.CreatePeer(ctx, p); err != nil {
		t.Fatal(err)
	}

	if err := live.UpdateOperator(ctx, next); !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdateOperator to a peer's host = %v", err)
	}

	if got := live.Get().Operator; got != validOperator() {
		t.Fatalf("Operator = %+v", got)
	}
}
