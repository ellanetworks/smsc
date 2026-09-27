package diameter

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/core/sctp"
)

const (
	testAppID    uint32 = 16777313
	testVendorID uint32 = 10415
	testTimeout         = 5 * time.Second
)

func skipIfNoSCTP(t *testing.T) {
	t.Helper()

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_SCTP)
	if err != nil {
		t.Skipf("SCTP not available: %v", err)
	}

	_ = syscall.Close(fd)
}

type testPeer struct {
	t     *testing.T
	conn  *sctp.SCTPConn
	msgs  chan *Message
	flags chan uint16
	errs  chan error
}

func startServer(t *testing.T, srv *Node) *sctp.SCTPAddr {
	t.Helper()
	skipIfNoSCTP(t)

	srv.Identity = Identity{
		OriginHost:      "smsc.example.org",
		OriginRealm:     "example.org",
		HostIPAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		VendorID:        testVendorID,
		ProductName:     "smsc",
	}
	srv.Applications = []Application{{ID: testAppID, VendorID: testVendorID}}

	if srv.Handler == nil {
		srv.Handler = HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
			return c.Answer(req, ResultSuccess)
		})
	}

	ctx, cancel := context.WithCancel(context.Background())

	var lc sctp.ListenConfig

	ln, err := lc.Listen(ctx, &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if err := srv.Serve(ctx, ln); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	t.Cleanup(func() {
		shutdownCtx, done := context.WithTimeout(context.Background(), testTimeout)
		defer done()

		srv.Shutdown(shutdownCtx)
		cancel()
	})

	return ln.Addr().(*sctp.SCTPAddr)
}

func dialPeer(t *testing.T, addr *sctp.SCTPAddr) *testPeer {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	conn, err := sctp.Dial(ctx, "sctp", nil, addr, sctp.InitMsg{NumOstreams: 2, MaxInstreams: 2})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	p := &testPeer{t: t, conn: conn, msgs: make(chan *Message, 16), flags: make(chan uint16, 16), errs: make(chan error, 1)}

	go func() {
		buf := make([]byte, 65536)

		for {
			n, info, err := conn.ReadMsg(buf)
			if err != nil {
				p.errs <- err
				return
			}

			m, err := Unmarshal(append([]byte(nil), buf[:n]...))
			if err != nil {
				p.errs <- err
				return
			}

			p.flags <- info.Flags

			p.msgs <- m
		}
	}()

	t.Cleanup(func() { _ = conn.Close() })

	return p
}

func (p *testPeer) send(m *Message) {
	p.t.Helper()

	b, err := m.Marshal()
	if err != nil {
		p.t.Fatalf("Marshal: %v", err)
	}

	if _, err := p.conn.WriteMsg(b, &sctp.SndRcvInfo{PPID: sctp.PPIDWireOrder(PPID)}); err != nil {
		p.t.Fatalf("WriteMsg: %v", err)
	}
}

func (p *testPeer) recv() *Message {
	p.t.Helper()

	m, _ := p.recvWithFlags()

	return m
}

func (p *testPeer) recvWithFlags() (*Message, uint16) {
	p.t.Helper()

	select {
	case m := <-p.msgs:
		return m, <-p.flags
	case err := <-p.errs:
		p.t.Fatalf("read: %v", err)
	case <-time.After(testTimeout):
		p.t.Fatal("timed out waiting for a message")
	}

	return nil, 0
}

func (p *testPeer) expectClosed() {
	p.t.Helper()

	select {
	case m := <-p.msgs:
		p.t.Fatalf("expected the connection to close, got command %d", m.CommandCode)
	case <-p.errs:
	case <-time.After(testTimeout):
		p.t.Fatal("timed out waiting for the connection to close")
	}
}

func cer(apps ...AVP) *Message {
	return &Message{
		Flags:       FlagRequest,
		CommandCode: CommandCapabilitiesExchange,
		HopByHopID:  1,
		EndToEndID:  1,
		AVPs: append([]AVP{
			UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
			UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
			Address(AVPHostIPAddress, AVPFlagMandatory, 0, netip.MustParseAddr("127.0.0.1")),
			Unsigned32(AVPVendorID, AVPFlagMandatory, 0, 0),
			UTF8String(AVPProductName, 0, 0, "test"),
		}, apps...),
	}
}

func sgdApp() AVP {
	return Grouped(AVPVendorSpecificApplicationID, AVPFlagMandatory, 0,
		Unsigned32(AVPVendorID, AVPFlagMandatory, 0, testVendorID),
		Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, testAppID),
	)
}

func resultCode(t *testing.T, m *Message) uint32 {
	t.Helper()

	a, ok := m.Find(AVPResultCode, 0)
	if !ok {
		t.Fatalf("command %d has no Result-Code", m.CommandCode)
	}

	v, err := a.Unsigned32()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func openPeer(t *testing.T, srv *Node) *testPeer {
	t.Helper()

	p := dialPeer(t, startServer(t, srv))
	p.send(cer(sgdApp()))

	cea := p.recv()
	if cea.CommandCode != CommandCapabilitiesExchange || cea.IsRequest() || resultCode(t, cea) != ResultSuccess {
		t.Fatalf("CEA = %+v", cea)
	}

	return p
}

func TestCapabilitiesExchange(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(sgdApp()))

	cea := p.recv()
	if cea.HopByHopID != 1 || cea.EndToEndID != 1 || resultCode(t, cea) != ResultSuccess {
		t.Fatalf("CEA = %+v", cea)
	}

	if host, _ := cea.Find(AVPOriginHost, 0); host.String() != "smsc.example.org" {
		t.Fatalf("Origin-Host = %q", host.String())
	}

	addr, ok := cea.Find(AVPHostIPAddress, 0)
	if got, err := addr.Address(); !ok || err != nil || got != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("Host-IP-Address = %v %v", got, err)
	}

	if _, ok := cea.Find(AVPVendorSpecificApplicationID, 0); !ok {
		t.Fatal("Vendor-Specific-Application-Id missing")
	}
}

func TestCapabilitiesExchangeNoCommonApplication(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, 4)))

	if code := resultCode(t, p.recv()); code != ResultNoCommonApplication {
		t.Fatalf("Result-Code = %d", code)
	}

	p.expectClosed()
}

func TestCapabilitiesExchangeRelay(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, RelayApplicationID)))

	if code := resultCode(t, p.recv()); code != ResultSuccess {
		t.Fatalf("Result-Code = %d", code)
	}
}

func TestFirstMessageNotCERCloses(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(&Message{Flags: FlagRequest, CommandCode: CommandDeviceWatchdog, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})

	p.expectClosed()
}

func TestDeviceWatchdogAnswered(t *testing.T) {
	p := openPeer(t, &Node{})
	p.send(&Message{Flags: FlagRequest, CommandCode: CommandDeviceWatchdog, HopByHopID: 7, EndToEndID: 8, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})

	dwa := p.recv()
	if dwa.CommandCode != CommandDeviceWatchdog || dwa.IsRequest() || dwa.HopByHopID != 7 || resultCode(t, dwa) != ResultSuccess {
		t.Fatalf("DWA = %+v", dwa)
	}
}

func TestDisconnectPeerAnswered(t *testing.T) {
	p := openPeer(t, &Node{})
	p.send(&Message{Flags: FlagRequest, CommandCode: CommandDisconnectPeer, HopByHopID: 9, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
		Unsigned32(AVPDisconnectCause, AVPFlagMandatory, 0, DisconnectCauseRebooting),
	}})

	dpa := p.recv()
	if dpa.CommandCode != CommandDisconnectPeer || dpa.IsRequest() || resultCode(t, dpa) != ResultSuccess {
		t.Fatalf("DPA = %+v", dpa)
	}
}

func TestApplicationRequestHandled(t *testing.T) {
	handled := make(chan *Message, 1)

	p := openPeer(t, &Node{Handler: HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
		handled <- req

		if c.PeerHost() != "mme.example.org" {
			t.Errorf("PeerHost = %q", c.PeerHost())
		}

		return c.Answer(req, ResultSuccess)
	})})

	p.send(&Message{
		Flags:         FlagRequest | FlagProxiable,
		CommandCode:   8388645,
		ApplicationID: testAppID,
		HopByHopID:    11,
		EndToEndID:    12,
		AVPs: []AVP{
			UTF8String(AVPSessionID, AVPFlagMandatory, 0, "mme.example.org;1"),
			UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
			UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
		},
	})

	ans := p.recv()
	if ans.IsRequest() || ans.Flags&FlagProxiable == 0 || ans.Flags&FlagError != 0 ||
		ans.HopByHopID != 11 || ans.EndToEndID != 12 || ans.ApplicationID != testAppID {
		t.Fatalf("answer = %+v", ans)
	}

	if ans.AVPs[0].Code != AVPSessionID || ans.AVPs[0].String() != "mme.example.org;1" {
		t.Fatalf("first AVP = %+v", ans.AVPs[0])
	}

	if got := <-handled; got.CommandCode != 8388645 {
		t.Fatalf("handler got %+v", got)
	}
}

func TestUnsupportedApplicationRejected(t *testing.T) {
	p := openPeer(t, &Node{})
	p.send(&Message{Flags: FlagRequest, CommandCode: 8388647, ApplicationID: 16777312, HopByHopID: 13, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})

	ans := p.recv()
	if ans.Flags&FlagError == 0 || resultCode(t, ans) != ResultApplicationUnsupported {
		t.Fatalf("answer = %+v", ans)
	}
}

func TestServerRequestToPeer(t *testing.T) {
	conns := make(chan *Conn, 1)

	p := openPeer(t, &Node{Handler: HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
		conns <- c
		return c.Answer(req, ResultSuccess)
	})})

	p.send(&Message{Flags: FlagRequest, CommandCode: 8388645, ApplicationID: testAppID, HopByHopID: 1, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})
	p.recv()

	c := <-conns

	type result struct {
		ans *Message
		err error
	}

	done := make(chan result, 1)

	go func() {
		ans, err := c.Do(context.Background(), &Message{CommandCode: 8388646, ApplicationID: testAppID})
		done <- result{ans, err}
	}()

	req := p.recv()
	if !req.IsRequest() || req.CommandCode != 8388646 {
		t.Fatalf("request = %+v", req)
	}

	p.send(&Message{CommandCode: 8388646, ApplicationID: testAppID, HopByHopID: req.HopByHopID, EndToEndID: req.EndToEndID, AVPs: []AVP{
		Unsigned32(AVPResultCode, AVPFlagMandatory, 0, ResultSuccess),
	}})

	select {
	case r := <-done:
		if r.err != nil || resultCode(t, r.ans) != ResultSuccess {
			t.Fatalf("Do = %+v, %v", r.ans, r.err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Do did not return")
	}
}

func TestWatchdogProbesAndCloses(t *testing.T) {
	oldMin, oldJitter := minWatchdogInterval, watchdogJitter
	minWatchdogInterval, watchdogJitter = 0, 0

	t.Cleanup(func() { minWatchdogInterval, watchdogJitter = oldMin, oldJitter })

	p := openPeer(t, &Node{WatchdogInterval: 200 * time.Millisecond})

	dwr := p.recv()
	if dwr.CommandCode != CommandDeviceWatchdog || !dwr.IsRequest() {
		t.Fatalf("DWR = %+v", dwr)
	}

	p.expectClosed()
}

func TestWatchdogAnsweredKeepsConnection(t *testing.T) {
	oldMin, oldJitter := minWatchdogInterval, watchdogJitter
	minWatchdogInterval, watchdogJitter = 0, 0

	t.Cleanup(func() { minWatchdogInterval, watchdogJitter = oldMin, oldJitter })

	p := openPeer(t, &Node{WatchdogInterval: 200 * time.Millisecond})

	for range 4 {
		dwr := p.recv()
		if dwr.CommandCode != CommandDeviceWatchdog || !dwr.IsRequest() {
			t.Fatalf("DWR = %+v", dwr)
		}

		p.send(&Message{CommandCode: CommandDeviceWatchdog, HopByHopID: dwr.HopByHopID, EndToEndID: dwr.EndToEndID, AVPs: []AVP{
			Unsigned32(AVPResultCode, AVPFlagMandatory, 0, ResultSuccess),
		}})
	}
}

func TestShutdownSendsDisconnectPeer(t *testing.T) {
	srv := &Node{}
	p := openPeer(t, srv)

	done := make(chan struct{})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		srv.Shutdown(ctx)
		close(done)
	}()

	dpr := p.recv()
	if dpr.CommandCode != CommandDisconnectPeer || !dpr.IsRequest() {
		t.Fatalf("DPR = %+v", dpr)
	}

	if cause, ok := dpr.Find(AVPDisconnectCause, 0); !ok {
		t.Fatal("Disconnect-Cause missing")
	} else if v, _ := cause.Unsigned32(); v != DisconnectCauseRebooting {
		t.Fatalf("Disconnect-Cause = %d", v)
	}

	p.send(&Message{CommandCode: CommandDisconnectPeer, HopByHopID: dpr.HopByHopID, EndToEndID: dpr.EndToEndID, AVPs: []AVP{
		Unsigned32(AVPResultCode, AVPFlagMandatory, 0, ResultSuccess),
	}})

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("Shutdown did not return")
	}

	p.expectClosed()
}

func TestServeValidatesConfiguration(t *testing.T) {
	err := (&Node{}).Serve(context.Background(), nil)
	if err == nil {
		t.Fatal("expected a validation error")
	}

	srv := &Node{
		Identity: Identity{
			OriginHost: "a", OriginRealm: "b", ProductName: "c",
			HostIPAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		},
		Applications:     []Application{{ID: testAppID}},
		Handler:          HandlerFunc(func(context.Context, *Conn, *Message) *Message { return nil }),
		WatchdogInterval: time.Second,
	}

	if err := srv.Serve(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a watchdog interval below the minimum")
	}
}

func TestDoOnClosedConn(t *testing.T) {
	c := newConn(&Node{}, nil, false)
	c.disconnect()

	if _, err := c.Do(context.Background(), &Message{}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("err = %v, want ErrConnClosed", err)
	}
}

func TestCapabilitiesExchangeNoCommonSecurity(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(sgdApp(), Unsigned32(AVPInbandSecurityID, AVPFlagMandatory, 0, 1)))

	if code := resultCode(t, p.recv()); code != ResultNoCommonSecurity {
		t.Fatalf("Result-Code = %d", code)
	}

	p.expectClosed()
}

func TestCapabilitiesExchangeNoInbandSecurityOffered(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(sgdApp(), Unsigned32(AVPInbandSecurityID, AVPFlagMandatory, 0, 1), Unsigned32(AVPInbandSecurityID, AVPFlagMandatory, 0, InbandSecurityNone)))

	if code := resultCode(t, p.recv()); code != ResultSuccess {
		t.Fatalf("Result-Code = %d", code)
	}
}

func TestCapabilitiesAnswerAdvertisesSupportedVendor(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(sgdApp()))

	cea := p.recv()

	vendors := FindAll(cea.AVPs, AVPSupportedVendorID, 0)
	if len(vendors) != 1 {
		t.Fatalf("got %d Supported-Vendor-Id AVPs, want 1", len(vendors))
	}

	if v, _ := vendors[0].Unsigned32(); v != testVendorID {
		t.Fatalf("Supported-Vendor-Id = %d", v)
	}
}

func TestHandlerPanicAnswersUnableToComply(t *testing.T) {
	p := openPeer(t, &Node{Handler: HandlerFunc(func(context.Context, *Conn, *Message) *Message {
		panic("boom")
	})})

	p.send(&Message{Flags: FlagRequest, CommandCode: 8388645, ApplicationID: testAppID, HopByHopID: 21, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})

	ans := p.recv()
	if ans.HopByHopID != 21 || resultCode(t, ans) != ResultUnableToComply {
		t.Fatalf("answer = %+v", ans)
	}
}

func TestUnorderedAfterPeerConfirmsOpen(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{}))
	p.send(cer(sgdApp()))

	cea, flags := p.recvWithFlags()
	if cea.CommandCode != CommandCapabilitiesExchange || flags&sctp.SCTPUnordered != 0 {
		t.Fatalf("CEA flags = 0x%x, want ordered", flags)
	}

	p.send(&Message{Flags: FlagRequest, CommandCode: CommandDeviceWatchdog, HopByHopID: 2, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})

	dwa, flags := p.recvWithFlags()
	if dwa.CommandCode != CommandDeviceWatchdog || flags&sctp.SCTPUnordered == 0 {
		t.Fatalf("DWA flags = 0x%x, want unordered", flags)
	}
}

func TestHandshakeTimeoutClosesSilentPeer(t *testing.T) {
	p := dialPeer(t, startServer(t, &Node{HandshakeTimeout: 200 * time.Millisecond}))

	p.expectClosed()
}

func TestHandshakeTimeoutStoppedByCER(t *testing.T) {
	p := openPeer(t, &Node{HandshakeTimeout: 200 * time.Millisecond})

	time.Sleep(400 * time.Millisecond)

	p.send(&Message{Flags: FlagRequest, CommandCode: CommandDeviceWatchdog, HopByHopID: 5, AVPs: []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
	}})

	if dwa := p.recv(); dwa.CommandCode != CommandDeviceWatchdog || resultCode(t, dwa) != ResultSuccess {
		t.Fatalf("DWA = %+v", dwa)
	}
}

func TestServeRejectsNegativeHandshakeTimeout(t *testing.T) {
	srv := &Node{
		Identity: Identity{
			OriginHost: "a", OriginRealm: "b", ProductName: "c",
			HostIPAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		},
		Applications:     []Application{{ID: testAppID}},
		Handler:          HandlerFunc(func(context.Context, *Conn, *Message) *Message { return nil }),
		HandshakeTimeout: -time.Second,
	}

	if err := srv.Serve(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a negative handshake timeout")
	}
}

func appRequest(hopByHop, endToEnd uint32, extra ...AVP) *Message {
	return &Message{
		Flags:         FlagRequest,
		CommandCode:   8388645,
		ApplicationID: testAppID,
		HopByHopID:    hopByHop,
		EndToEndID:    endToEnd,
		AVPs: append([]AVP{
			UTF8String(AVPOriginHost, AVPFlagMandatory, 0, "mme.example.org"),
			UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, "example.org"),
		}, extra...),
	}
}

func TestRequestForAnotherDestination(t *testing.T) {
	tests := map[string]struct {
		avps []AVP
		want uint32
	}{
		"other host": {
			[]AVP{UTF8String(AVPDestinationHost, AVPFlagMandatory, 0, "other.example.org")},
			ResultUnableToDeliver,
		},
		"other realm": {
			[]AVP{UTF8String(AVPDestinationRealm, AVPFlagMandatory, 0, "other.org")},
			ResultRealmNotServed,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := openPeer(t, &Node{})
			p.send(appRequest(30, 30, tt.avps...))

			ans := p.recv()
			if ans.Flags&FlagError == 0 || resultCode(t, ans) != tt.want {
				t.Fatalf("answer = %+v", ans)
			}
		})
	}
}

func TestRequestForThisHostIsLocal(t *testing.T) {
	p := openPeer(t, &Node{})
	p.send(appRequest(31, 31,
		UTF8String(AVPDestinationHost, AVPFlagMandatory, 0, "SMSC.example.org"),
		UTF8String(AVPDestinationRealm, AVPFlagMandatory, 0, "example.org"),
	))

	if code := resultCode(t, p.recv()); code != ResultSuccess {
		t.Fatalf("Result-Code = %d", code)
	}
}

func TestDuplicateRequestGetsOriginalAnswer(t *testing.T) {
	var calls atomic.Int32

	p := openPeer(t, &Node{Handler: HandlerFunc(func(_ context.Context, c *Conn, req *Message) *Message {
		calls.Add(1)
		return c.Answer(req, ResultSuccess)
	})})

	p.send(appRequest(40, 99))
	first := p.recv()

	retransmit := appRequest(41, 99)
	retransmit.Flags |= FlagRetransmit
	p.send(retransmit)
	second := p.recv()

	if calls.Load() != 1 {
		t.Fatalf("handler called %d times, want 1", calls.Load())
	}

	if second.HopByHopID != 41 || second.EndToEndID != 99 || resultCode(t, second) != resultCode(t, first) {
		t.Fatalf("duplicate answer = %+v", second)
	}

	if second.Flags&FlagRetransmit != 0 {
		t.Fatal("answer must not carry the T flag")
	}

	p.send(appRequest(42, 100))
	p.recv()

	if calls.Load() != 2 {
		t.Fatalf("handler called %d times after a new request, want 2", calls.Load())
	}
}

func TestShutdownWaitsForInFlightRequests(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})

	srv := &Node{Handler: HandlerFunc(func(ctx context.Context, c *Conn, req *Message) *Message {
		close(started)
		<-release

		if ctx.Err() != nil {
			t.Errorf("handler context cancelled during shutdown: %v", ctx.Err())
		}

		return c.Answer(req, ResultSuccess)
	})}

	p := openPeer(t, srv)
	p.send(appRequest(50, 50))
	<-started

	done := make(chan struct{})

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		srv.Shutdown(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	close(release)

	ans := p.recv()
	if ans.HopByHopID != 50 || resultCode(t, ans) != ResultSuccess {
		t.Fatalf("in-flight answer = %+v", ans)
	}

	dpr := p.recv()
	if dpr.CommandCode != CommandDisconnectPeer {
		t.Fatalf("expected DPR after the in-flight answer, got %d", dpr.CommandCode)
	}

	p.send(&Message{CommandCode: CommandDisconnectPeer, HopByHopID: dpr.HopByHopID, EndToEndID: dpr.EndToEndID, AVPs: []AVP{
		Unsigned32(AVPResultCode, AVPFlagMandatory, 0, ResultSuccess),
	}})

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("Shutdown did not return")
	}
}

func TestRequestDuringShutdown(t *testing.T) {
	tests := map[string]struct {
		avps []AVP
		want uint32
	}{
		"addressed to this host": {
			[]AVP{UTF8String(AVPDestinationHost, AVPFlagMandatory, 0, "smsc.example.org")},
			ResultTooBusy,
		},
		"no destination host": {nil, ResultUnableToDeliver},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			srv := &Node{}
			p := openPeer(t, srv)

			srv.admitMu.Lock()
			srv.shuttingDown = true
			srv.admitMu.Unlock()

			p.send(appRequest(60, 60, tt.avps...))

			ans := p.recv()
			if ans.Flags&FlagError == 0 || resultCode(t, ans) != tt.want {
				t.Fatalf("answer = %+v", ans)
			}

			srv.admitMu.Lock()
			srv.shuttingDown = false
			srv.admitMu.Unlock()
		})
	}
}

func TestAdmitAfterShutdownStartsIsRefused(t *testing.T) {
	srv := &Node{}

	if code := srv.admit(appRequest(1, 1)); code != 0 {
		t.Fatalf("admit before shutdown = %d", code)
	}

	srv.admitMu.Lock()
	srv.shuttingDown = true
	srv.admitMu.Unlock()

	if code := srv.admit(appRequest(2, 2)); code == 0 {
		t.Fatal("request admitted after shutdown started")
	}

	srv.inflight.Done()
	srv.inflight.Wait()
}
