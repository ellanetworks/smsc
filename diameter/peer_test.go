package diameter

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/sctp"
)

func newPeerNode(host string) *Node {
	return &Node{
		Identity: Identity{
			OriginHost:      host,
			OriginRealm:     "example.org",
			HostIPAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
			VendorID:        testVendorID,
			ProductName:     "test",
		},
		Applications: []Application{{ID: testAppID, VendorID: testVendorID}},
		Handler: HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
			return c.Answer(req, ResultSuccess)
		}),
		HandshakeTimeout:  2 * time.Second,
		ReconnectInterval: 100 * time.Millisecond,
	}
}

func listen(t *testing.T) *sctp.Listener {
	t.Helper()
	skipIfNoSCTP(t)

	var lc sctp.ListenConfig

	ln, err := lc.Listen(context.Background(), &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	return ln
}

func serve(t *testing.T, n *Node, ln *sctp.Listener) {
	t.Helper()

	if err := n.Serve(context.Background(), ln); err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func listenNode(t *testing.T, n *Node) *sctp.SCTPAddr {
	t.Helper()

	ln := listen(t)
	serve(t, n, ln)

	return ln.Addr().(*sctp.SCTPAddr)
}

func fastWatchdog(t *testing.T) {
	t.Helper()

	oldMin, oldJitter := minWatchdogInterval, watchdogJitter
	minWatchdogInterval, watchdogJitter = 0, 0

	t.Cleanup(func() { minWatchdogInterval, watchdogJitter = oldMin, oldJitter })
}

func startNode(t *testing.T, n *Node) {
	t.Helper()

	if err := n.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		n.Shutdown(ctx)
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(testTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

func request() *Message {
	return &Message{
		CommandCode:   8388647,
		ApplicationID: testAppID,
		AVPs: []AVP{
			UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "x"),
			UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
		},
	}
}

func doSucceeds(n *Node, peer string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ans, err := n.Do(ctx, peer, request())
	if err != nil {
		return false
	}

	rc, ok := ans.Find(AVPResultCode, 0)
	v, _ := rc.Unsigned32()

	return ok && v == ResultSuccess
}

func openConn(n *Node, peer string) *Conn {
	n.peersMu.Lock()
	defer n.peersMu.Unlock()

	if e, ok := n.peers[strings.ToLower(peer)]; ok {
		return e.open
	}

	return nil
}

func TestNodeDialsPeerAndExchangesRequestsBothWays(t *testing.T) {
	hss := newPeerNode("hss.example.org")
	hssAddr := listenNode(t, hss)
	startNode(t, hss)

	smsc := newPeerNode("smsc.example.org")
	smsc.Peers = []Peer{{Host: "hss.example.org", Address: hssAddr}}
	startNode(t, smsc)

	eventually(t, "the SMSC to reach the HSS", func() bool { return doSucceeds(smsc, "hss.example.org") })
	eventually(t, "the HSS to reach the SMSC over the same connection", func() bool { return doSucceeds(hss, "SMSC.example.org") })

	if c := openConn(smsc, "hss.example.org"); c == nil || !c.initiator || !c.unordered.Load() {
		t.Fatalf("SMSC side connection = %+v", c)
	}
}

func TestNodeDoUnknownPeer(t *testing.T) {
	n := newPeerNode("smsc.example.org")
	startNode(t, n)

	if _, err := n.Do(context.Background(), "nobody.example.org", request()); !errors.Is(err, ErrUnknownPeer) {
		t.Fatalf("err = %v, want ErrUnknownPeer", err)
	}
}

func TestNodeDoBeforeConnected(t *testing.T) {
	n := newPeerNode("smsc.example.org")
	n.Peers = []Peer{{Host: "hss.example.org", Address: &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, Port: 1}}}
	startNode(t, n)

	if _, err := n.Do(context.Background(), "hss.example.org", request()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

func TestElectionLeavesOneConnection(t *testing.T) {
	a := newPeerNode("a.example.org")
	b := newPeerNode("b.example.org")

	aLn, bLn := listen(t), listen(t)

	a.Peers = []Peer{{Host: "b.example.org", Address: bLn.Addr().(*sctp.SCTPAddr)}}
	b.Peers = []Peer{{Host: "a.example.org", Address: aLn.Addr().(*sctp.SCTPAddr)}}

	serve(t, a, aLn)
	serve(t, b, bLn)
	startNode(t, a)
	startNode(t, b)

	eventually(t, "a to reach b", func() bool { return doSucceeds(a, "b.example.org") })
	eventually(t, "b to reach a", func() bool { return doSucceeds(b, "a.example.org") })

	time.Sleep(300 * time.Millisecond)

	ca, cb := openConn(a, "b.example.org"), openConn(b, "a.example.org")
	if ca == nil || cb == nil {
		t.Fatal("expected an open connection on both sides")
	}

	if ca.initiator == cb.initiator {
		t.Fatalf("both sides report initiator=%v; want one initiator and one responder", ca.initiator)
	}
}

func TestNodeReconnectsAndReopens(t *testing.T) {
	fastWatchdog(t)

	hss := newPeerNode("hss.example.org")
	hss.WatchdogInterval = 100 * time.Millisecond
	hssAddr := listenNode(t, hss)
	startNode(t, hss)

	smsc := newPeerNode("smsc.example.org")
	smsc.WatchdogInterval = 100 * time.Millisecond
	smsc.Peers = []Peer{{Host: "hss.example.org", Address: hssAddr}}
	startNode(t, smsc)

	eventually(t, "the first connection", func() bool { return doSucceeds(smsc, "hss.example.org") })

	first := openConn(smsc, "hss.example.org")
	if first.reopen {
		t.Fatal("the first connection must start in OKAY, not REOPEN")
	}

	openConn(hss, "smsc.example.org").abort()

	eventually(t, "a new connection", func() bool {
		c := openConn(smsc, "hss.example.org")
		return c != nil && c != first
	})

	second := openConn(smsc, "hss.example.org")
	if !second.reopen {
		t.Fatal("a reconnection must start in REOPEN")
	}

	eventually(t, "the HSS to see the new connection", func() bool {
		c := openConn(hss, "smsc.example.org")
		return c != nil && !c.initiator
	})

	if openConn(hss, "smsc.example.org").reopen {
		t.Fatal("REOPEN applies only to connections this node opened")
	}

	eventually(t, "the reopened connection to become available", func() bool { return doSucceeds(smsc, "hss.example.org") })
}

func TestNodeHonoursDisconnectCauseBusy(t *testing.T) {
	fastWatchdog(t)

	hss := newPeerNode("hss.example.org")
	hss.WatchdogInterval = 100 * time.Millisecond
	hssAddr := listenNode(t, hss)
	startNode(t, hss)

	smsc := newPeerNode("smsc.example.org")
	smsc.WatchdogInterval = 100 * time.Millisecond
	smsc.Peers = []Peer{{Host: "hss.example.org", Address: hssAddr}}
	startNode(t, smsc)

	eventually(t, "the first connection", func() bool { return doSucceeds(smsc, "hss.example.org") })

	openConn(hss, "smsc.example.org").sendDPR(DisconnectCauseBusy)

	eventually(t, "the connection to close", func() bool { return openConn(smsc, "hss.example.org") == nil })

	time.Sleep(3 * smsc.ReconnectInterval)

	if openConn(smsc, "hss.example.org") != nil {
		t.Fatal("reconnected on its own after a BUSY disconnect")
	}

	if _, err := smsc.Do(context.Background(), "hss.example.org", request()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}

	eventually(t, "a request to trigger a reconnection", func() bool { return doSucceeds(smsc, "hss.example.org") })
}

func TestCEAWithoutCommonApplicationCloses(t *testing.T) {
	hss := newPeerNode("hss.example.org")
	hss.Applications = []Application{{ID: 16777312, VendorID: testVendorID}}
	hssAddr := listenNode(t, hss)
	startNode(t, hss)

	smsc := newPeerNode("smsc.example.org")
	smsc.Peers = []Peer{{Host: "hss.example.org", Address: hssAddr}}
	startNode(t, smsc)

	time.Sleep(500 * time.Millisecond)

	if openConn(smsc, "hss.example.org") != nil {
		t.Fatal("connection opened without a common application")
	}
}

func TestNewSessionID(t *testing.T) {
	n := newPeerNode("smsc.example.org")
	startNode(t, n)

	first, second := n.NewSessionID(), n.NewSessionID()
	if first == second || !strings.HasPrefix(first, "smsc.example.org;") || strings.Count(first, ";") != 2 {
		t.Fatalf("session IDs %q, %q", first, second)
	}
}

func TestNodeRetransmitsAfterFailover(t *testing.T) {
	fastWatchdog(t)

	var calls atomic.Int32

	hss := newPeerNode("hss.example.org")
	hss.WatchdogInterval = 100 * time.Millisecond
	hss.Handler = HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
		if calls.Add(1) == 1 {
			c.abort()
			time.Sleep(50 * time.Millisecond)
		}

		return c.Answer(req, ResultSuccess)
	})
	hssAddr := listenNode(t, hss)
	startNode(t, hss)

	smsc := newPeerNode("smsc.example.org")
	smsc.WatchdogInterval = 100 * time.Millisecond
	smsc.Peers = []Peer{{Host: "hss.example.org", Address: hssAddr}}
	startNode(t, smsc)

	eventually(t, "the first connection", func() bool {
		c := openConn(smsc, "hss.example.org")
		return c != nil && c.available.Load()
	})

	req := request()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	ans, err := smsc.Do(ctx, "hss.example.org", req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if rc, _ := ans.Find(AVPResultCode, 0); resultValue(rc) != ResultSuccess {
		t.Fatalf("answer = %+v", ans)
	}

	if req.Flags&FlagRetransmit == 0 {
		t.Fatal("the retransmitted request must carry the T flag")
	}

	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times; the retransmission must reuse the End-to-End ID and be answered as a duplicate", calls.Load())
	}
}

func resultValue(a AVP) uint32 {
	v, _ := a.Unsigned32()
	return v
}

func TestNodeDoDefaultTimeout(t *testing.T) {
	release := make(chan struct{})

	hss := newPeerNode("hss.example.org")
	hss.Handler = HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
		<-release
		return c.Answer(req, ResultSuccess)
	})
	hssAddr := listenNode(t, hss)
	startNode(t, hss)

	t.Cleanup(func() { close(release) })

	smsc := newPeerNode("smsc.example.org")
	smsc.RequestTimeout = 200 * time.Millisecond
	smsc.Peers = []Peer{{Host: "hss.example.org", Address: hssAddr}}
	startNode(t, smsc)

	eventually(t, "the connection", func() bool {
		c := openConn(smsc, "hss.example.org")
		return c != nil && c.available.Load()
	})

	start := time.Now()

	if _, err := smsc.Do(context.Background(), "hss.example.org", request()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}

	if time.Since(start) > 2*time.Second {
		t.Fatalf("Do took %s despite a 200ms RequestTimeout", time.Since(start))
	}
}
