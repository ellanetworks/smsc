package s6c

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
)

func testNode(t *testing.T, host string, handler diameter.Handler, acceptUnknown bool) *diameter.Node {
	t.Helper()

	apps := []diameter.Application{{ID: s6c.ApplicationID, VendorID: tgpp.VendorID}}

	cfg := diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      host,
			OriginRealm:     "example.org",
			HostIPAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
			ProductName:     "test",
		},
		Handler:           handler,
		ReconnectInterval: 100 * time.Millisecond,
	}

	if acceptUnknown {
		cfg.AcceptUnknownPeers = true
		cfg.UnknownPeerApplications = apps
	}

	n, err := diameter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return n
}

func TestSendRoutingInfoForSMOverSCTP(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_SCTP)
	if err != nil {
		t.Skipf("SCTP not available: %v", err)
	}

	_ = syscall.Close(fd)

	received := make(chan *diameter.Message, 1)

	hss := testNode(t, "hss.example.org", diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
		received <- req

		ans := c.Answer(req, diameter.ResultSuccess)
		ans.AVPs = append(ans.AVPs,
			diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, "001010000000002"),
			mmeServingNode(t),
		)

		return ans
	}), true)

	var lc sctp.ListenConfig

	ln, err := lc.Listen(context.Background(), &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}})
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = hss.Serve(diameter.NewSCTPListener(ln, nil)) }()

	smsc := testNode(t, "smsc.example.org", diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
		return c.Answer(req, diameter.ResultCommandUnsupported)
	}), false)

	if err := smsc.SetPeers([]diameter.Peer{{
		ID:           "hss",
		Host:         "hss.example.org",
		Addresses:    []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		Port:         uint16(ln.Addr().(*sctp.SCTPAddr).Port),
		Applications: []diameter.Application{{ID: s6c.ApplicationID, VendorID: tgpp.VendorID}},
	}}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = smsc.Shutdown(ctx)
		_ = hss.Shutdown(ctx)
	})

	router := &Router{
		Node:                 hssByID{smsc},
		Identity:             smsc.Identity(),
		HSSRealm:             "example.org",
		ServiceCentreAddress: "15550000000",
	}

	var (
		routing s6c.Routing
		lastErr error
	)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		routing, lastErr = router.SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002"})
		if lastErr == nil {
			break
		}

		time.Sleep(50 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("SendRoutingInfoForSM: %v", lastErr)
	}

	if routing.IMSI != "001010000000002" || routing.Serving == nil || routing.Serving.MME == nil || routing.Serving.MME.Name != "mme.example.org" {
		t.Fatalf("routing = %+v", routing)
	}

	req := <-received
	if msisdn, ok := req.Find(tgpp.AVPMSISDN, tgpp.VendorID); !ok || len(msisdn.Data) != 6 {
		t.Fatalf("HSS received %+v", req)
	}
}

type hssByID struct{ node *diameter.Node }

func (h hssByID) Do(ctx context.Context, req *diameter.Message) (*diameter.Message, error) {
	return h.node.Do(ctx, "hss", req)
}

func (h hssByID) NewSessionID() string { return h.node.NewSessionID() }
