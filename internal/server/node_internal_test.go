package server

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"slices"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/settings"
)

func TestFailedSetPeersKeepsThePeersTheNodeHas(t *testing.T) {
	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org", ProductName: "smsc"},
		Handler:  diameter.NewMux(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = node.Shutdown(context.Background()) })

	m := &nodeManager{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	good := []settings.Peer{{ID: 1, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.10")}, Port: 3868}}
	m.setPeers(node, good)

	if got := m.configuredPeers(); !slices.EqualFunc(got, good, settings.Peer.Equal) {
		t.Fatalf("peers = %+v", got)
	}

	// An address the node refuses, which the settings would not let through.
	bad := []settings.Peer{{ID: 2, Addresses: []netip.Addr{{}}, Port: 3868}}
	m.setPeers(node, bad)

	if got := m.configuredPeers(); !slices.EqualFunc(got, good, settings.Peer.Equal) {
		t.Fatalf("peers after a failed SetPeers = %+v, want the node's %+v", got, good)
	}

	if _, ok := node.Peer(good[0].NodeID()); !ok {
		t.Fatal("the node lost its peer")
	}
}
