package db

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ellanetworks/smsc/internal/settings"
)

func testPeer() settings.Peer {
	return settings.Peer{
		Host:      "dea1.ipx.example.net",
		Addresses: []netip.Addr{netip.MustParseAddr("198.51.100.10"), netip.MustParseAddr("192.0.2.10")},
		Port:      3868,
		Routes: []settings.Route{
			{Realm: "epc.mnc002.mcc208.3gppnetwork.org", Application: settings.ApplicationSGd, Priority: 1},
			{Default: true, Application: settings.ApplicationSGd},
			{Realm: "epc.mnc001.mcc001.3gppnetwork.org", Application: settings.ApplicationS6c},
		},
	}
}

func peersOf(t *testing.T, d *DB) []settings.Peer {
	t.Helper()

	s, err := d.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	return s.Peers
}

func TestPeersPersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "smsc.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	dea1 := testPeer()
	if dea1.ID, err = d.CreatePeer(ctx, dea1); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	hostless := settings.Peer{Addresses: []netip.Addr{netip.MustParseAddr("2001:db8::1")}, Port: 3869}
	if hostless.ID, err = d.CreatePeer(ctx, hostless); err != nil {
		t.Fatalf("CreatePeer without a host: %v", err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if d, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	// The addresses and routes keep their order.
	got := peersOf(t, d)
	if len(got) != 2 || !got[0].Equal(dea1) || !got[1].Equal(hostless) {
		t.Fatalf("Peers = %+v, want %+v and %+v", got, dea1, hostless)
	}
}

func TestUpdatePeer(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	p := testPeer()

	id, err := d.CreatePeer(ctx, p)
	if err != nil {
		t.Fatal(err)
	}

	p.ID = id
	p.Host = ""
	p.Addresses = p.Addresses[1:]
	p.Port = 3870
	p.Routes = []settings.Route{{Default: true, Application: settings.ApplicationSGd, Priority: 2}}

	if err := d.UpdatePeer(ctx, p); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}

	if got := peersOf(t, d); len(got) != 1 || !got[0].Equal(p) {
		t.Fatalf("Peers = %+v, want %+v", got, p)
	}

	p.ID = id + 1
	if err := d.UpdatePeer(ctx, p); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePeer of an unknown peer = %v", err)
	}
}

func TestPeerHostIsUnique(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.CreatePeer(ctx, testPeer()); err != nil {
		t.Fatal(err)
	}

	other := testPeer()
	other.Host = "DEA1.ipx.example.net"

	if _, err := d.CreatePeer(ctx, other); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("CreatePeer with the host of another = %v", err)
	}

	other.Host = "dea2.ipx.example.net"

	id, err := d.CreatePeer(ctx, other)
	if err != nil {
		t.Fatal(err)
	}

	other.ID, other.Host = id, "dea1.IPX.example.net"
	if err := d.UpdatePeer(ctx, other); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("UpdatePeer to the host of another = %v", err)
	}

	// Peers without a host do not clash.
	for range 2 {
		if _, err := d.CreatePeer(ctx, settings.Peer{Addresses: other.Addresses, Port: 3868}); err != nil {
			t.Fatalf("CreatePeer without a host: %v", err)
		}
	}
}

func TestDeletePeer(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreatePeer(ctx, testPeer())
	if err != nil {
		t.Fatal(err)
	}

	if err := d.DeletePeer(ctx, id); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	if err := d.DeletePeer(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeletePeer twice = %v", err)
	}

	if got := peersOf(t, d); len(got) != 0 {
		t.Fatalf("Peers = %+v", got)
	}

	// The addresses and routes go with the peer.
	for _, table := range []string{"diameter_peer_addresses", "diameter_routes"} {
		var n int
		if err := d.conn.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s has %d rows, %v", table, n, err)
		}
	}
}

func TestDefaultRouteIsStoredWithoutARealm(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	p := testPeer()
	// A default route that carries a realm is stored as a default route.
	p.Routes = []settings.Route{{Realm: "ignored.example.net", Default: true, Application: settings.ApplicationSGd}}

	if _, err := d.CreatePeer(ctx, p); err != nil {
		t.Fatal(err)
	}

	var realm string
	if err := d.conn.QueryRowContext(ctx, "SELECT realm FROM diameter_routes").Scan(&realm); err != nil || realm != "" {
		t.Fatalf("realm = %q, %v", realm, err)
	}

	got := peersOf(t, d)[0].Routes
	if want := []settings.Route{{Default: true, Application: settings.ApplicationSGd}}; !slices.Equal(got, want) {
		t.Fatalf("Routes = %+v, want %+v", got, want)
	}

	// Only SGd takes a default route.
	p.Host = "dea2.ipx.example.net"
	p.Routes = []settings.Route{{Default: true, Application: settings.ApplicationS6c}}

	if _, err := d.CreatePeer(ctx, p); err == nil {
		t.Fatal("expected a constraint error for a default s6c route")
	}
}
