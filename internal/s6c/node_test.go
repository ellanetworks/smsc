package s6c

import (
	"context"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

func testNode(host string, handler diameter.Handler) *diameter.Node {
	return &diameter.Node{
		Identity: diameter.Identity{
			OriginHost:      host,
			OriginRealm:     "example.org",
			HostIPAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
			ProductName:     "test",
		},
		Applications:      []diameter.Application{{ID: ApplicationID, VendorID: tgpp.VendorID}},
		Handler:           handler,
		ReconnectInterval: 100 * time.Millisecond,
	}
}

func TestSendRoutingInfoForSMOverSCTP(t *testing.T) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_SCTP)
	if err != nil {
		t.Skipf("SCTP not available: %v", err)
	}

	_ = syscall.Close(fd)

	received := make(chan *diameter.Message, 1)

	hss := testNode("hss.example.org", diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
		received <- req

		ans := c.Answer(req, diameter.ResultSuccess)
		ans.AVPs = append(ans.AVPs,
			diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, "001010000000002"),
			mmeServingNode(t),
		)

		return ans
	}))

	var lc sctp.ListenConfig

	ln, err := lc.Listen(context.Background(), &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}})
	if err != nil {
		t.Fatal(err)
	}

	if err := hss.Serve(context.Background(), ln); err != nil {
		t.Fatal(err)
	}

	smsc := testNode("smsc.example.org", diameter.HandlerFunc(func(_ context.Context, c *diameter.Conn, req *diameter.Message) *diameter.Message {
		return c.Answer(req, diameter.ResultCommandUnsupported)
	}))
	smsc.Peers = []diameter.Peer{{Host: "hss.example.org", Address: ln.Addr().(*sctp.SCTPAddr)}}

	if err := smsc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		smsc.Shutdown(ctx)
		hss.Shutdown(ctx)
	})

	router := &Router{
		Node:                 smsc,
		Identity:             smsc.Identity,
		HSSHost:              "hss.example.org",
		HSSRealm:             "example.org",
		ServiceCentreAddress: "15550000000",
	}

	var (
		routing Routing
		lastErr error
	)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		routing, lastErr = router.SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})
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
