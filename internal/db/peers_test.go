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

	named := settings.Peer{Host: "ims.ella.svc.cluster.local", Name: "ims.ella.svc.cluster.local", Port: 3868}
	if named.ID, err = d.CreatePeer(ctx, named); err != nil {
		t.Fatalf("CreatePeer with a host name: %v", err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if d, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	got := peersOf(t, d)
	if len(got) != 3 || !got[0].Equal(dea1) || !got[1].Equal(hostless) || !got[2].Equal(named) {
		t.Fatalf("Peers = %+v, want %+v, %+v and %+v", got, dea1, hostless, named)
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

	p.Name, p.Addresses = "dea1.ipx.example.net", nil

	if err := d.UpdatePeer(ctx, p); err != nil {
		t.Fatalf("UpdatePeer to a host name: %v", err)
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

	if _, err := d.CreatePeer(ctx, settings.Peer{Addresses: other.Addresses, Port: 3868}); err != nil {
		t.Fatalf("CreatePeer without a host: %v", err)
	}

	if _, err := d.CreatePeer(ctx, settings.Peer{Addresses: other.Addresses, Port: 3868}); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("second CreatePeer without a host at the same address = %v, want a conflict", err)
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

	for _, table := range []string{"diameter_peer_addresses", "diameter_routes"} {
		var n int
		if err := d.writer.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s has %d rows, %v", table, n, err)
		}
	}
}

func TestDefaultRouteIsStoredWithoutARealm(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	p := testPeer()
	p.Routes = []settings.Route{{Realm: "ignored.example.net", Default: true, Application: settings.ApplicationSGd}}

	if _, err := d.CreatePeer(ctx, p); err != nil {
		t.Fatal(err)
	}

	var realm string
	if err := d.writer.QueryRowContext(ctx, "SELECT realm FROM diameter_routes").Scan(&realm); err != nil || realm != "" {
		t.Fatalf("realm = %q, %v", realm, err)
	}

	got := peersOf(t, d)[0].Routes
	if want := []settings.Route{{Default: true, Application: settings.ApplicationSGd}}; !slices.Equal(got, want) {
		t.Fatalf("Routes = %+v, want %+v", got, want)
	}

	p.Host = "dea2.ipx.example.net"
	p.Routes = []settings.Route{{Default: true, Application: settings.ApplicationS6c}}

	if _, err := d.CreatePeer(ctx, p); err == nil {
		t.Fatal("expected a constraint error for a default s6c route")
	}
}

func TestMemberAndPeerHostsDoNotCollide(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	s, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	const taken, free = "01a1ac75-e354-762b-b38f-013c0a7ca824", "01a1ac75-e354-762b-b38f-0123456789ab"

	p := testPeer()
	p.Host = s.Operator.Host(settings.NodeLabel(taken))

	if _, err := d.CreatePeer(ctx, p); err != nil {
		t.Fatalf("CreatePeer: %v", err)
	}

	if err := d.RegisterMember(ctx, Member{NodeID: taken, Address: "192.0.2.1:7000", Version: "v"}); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("RegisterMember with a peer's host = %v", err)
	}

	if err := d.RegisterMember(ctx, Member{NodeID: free, Address: "192.0.2.2:7000", Version: "v"}); err != nil {
		t.Fatalf("RegisterMember: %v", err)
	}

	q := testPeer()
	q.Host = s.Operator.Host(settings.NodeLabel(free))

	if _, err := d.CreatePeer(ctx, q); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("CreatePeer with a member's host = %v", err)
	}
}
