package server_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
)

const (
	visitedMME   = "mme1.visited.example"
	visitedRealm = "epc.mnc002.mcc002.3gppnetwork.org"
	ipxRealm     = "ipx.example"
)

// agentRequest is a request that an agent received.
type agentRequest struct {
	command          uint32
	destinationHost  string
	destinationRealm string
}

// fakeAgent is a Diameter agent, such as an IPX DEA or a DRA in front of the HSS, that the SMSC connects to. It
// advertises the Relay application, and hands the requests it receives to the nodes behind it, its backend, as a
// relay would, whatever their Destination-Host. The library's Node answers a request for another host itself, so the
// agent speaks Diameter over SCTP directly.
type fakeAgent struct {
	t       testing.TB
	host    string
	realm   string
	backend *fakeCore
	ln      *sctp.Listener
	port    int

	mu       sync.Mutex
	conns    []*agentConn
	requests []agentRequest
	// answer, when it returns an answer, answers a request instead of the backend.
	answer func(req *diameter.Message) *diameter.Message
	// applications are those the agent advertises; Relay when empty.
	applications []uint32
	closed       bool
}

type agentConn struct {
	c *sctp.SCTPConn

	mu      sync.Mutex
	pending map[uint32]chan *diameter.Message
	hbh     uint32
	// origin is the Origin-Host of the CER.
	origin string
}

func newFakeAgent(t testing.TB, host, realm string, backend *fakeCore) *fakeAgent {
	t.Helper()
	skipIfNoSCTP(t)

	var lc sctp.ListenConfig

	ln, err := lc.Listen(context.Background(), &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: loopback.AsSlice()}}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	addr, ok := ln.Addr().(*sctp.SCTPAddr)
	if !ok {
		t.Fatalf("unexpected listener address %v", ln.Addr())
	}

	a := &fakeAgent{t: t, host: host, realm: realm, backend: backend, ln: ln, port: addr.Port}

	go a.accept()

	t.Cleanup(a.shutdown)

	return a
}

func (a *fakeAgent) identity() diameter.Identity {
	return diameter.Identity{OriginHost: a.host, OriginRealm: a.realm}
}

func (a *fakeAgent) accept() {
	for {
		c, err := a.ln.Accept()
		if err != nil {
			return
		}

		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()

			_ = c.Close()

			return
		}

		ac := &agentConn{c: c, pending: make(map[uint32]chan *diameter.Message)}
		a.conns = append(a.conns, ac)
		a.mu.Unlock()

		go a.serve(ac)
	}
}

// shutdown closes the agent and its connections, as an agent that goes down would.
func (a *fakeAgent) shutdown() {
	a.mu.Lock()
	a.closed = true
	conns := a.conns
	a.conns = nil
	a.mu.Unlock()

	_ = a.ln.Close()

	for _, c := range conns {
		_ = c.c.Close()
	}
}

func (a *fakeAgent) serve(c *agentConn) {
	buf := make([]byte, 1<<16)

	for {
		n, _, err := c.c.ReadMsg(buf)
		if err != nil {
			return
		}

		m, err := diameter.Unmarshal(slices.Clone(buf[:n]))
		if err != nil {
			continue
		}

		if !m.IsRequest() {
			c.mu.Lock()
			ch := c.pending[m.HopByHopID]
			delete(c.pending, m.HopByHopID)
			c.mu.Unlock()

			if ch != nil {
				ch <- m
			}

			continue
		}

		go a.handle(c, m)
	}
}

func (a *fakeAgent) handle(c *agentConn, req *diameter.Message) {
	var ans *diameter.Message

	switch req.CommandCode {
	case diameter.CommandCapabilitiesExchange:
		if host, ok := req.Find(diameter.AVPOriginHost, 0); ok {
			c.mu.Lock()
			c.origin = host.UTF8String()
			c.mu.Unlock()
		}

		ans = diameter.NewAnswer(req, a.identity(), diameter.ResultSuccess)
		ans.AVPs = append(ans.AVPs,
			diameter.Address(diameter.AVPHostIPAddress, diameter.AVPFlagMandatory, 0, loopback),
			diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, 0),
			diameter.UTF8String(diameter.AVPProductName, 0, 0, "fake-agent"),
		)

		a.mu.Lock()
		apps := a.applications
		a.mu.Unlock()

		if len(apps) == 0 {
			apps = []uint32{diameter.RelayApplicationID}
		}

		for _, app := range apps {
			ans.AVPs = append(ans.AVPs, diameter.Unsigned32(diameter.AVPAuthApplicationID, diameter.AVPFlagMandatory, 0, app))
		}
	case diameter.CommandDeviceWatchdog, diameter.CommandDisconnectPeer:
		ans = diameter.NewAnswer(req, a.identity(), diameter.ResultSuccess)
	default:
		r := agentRequest{command: req.CommandCode}

		if h, ok := req.Find(diameter.AVPDestinationHost, 0); ok {
			r.destinationHost = h.UTF8String()
		}

		if d, ok := req.Find(diameter.AVPDestinationRealm, 0); ok {
			r.destinationRealm = d.UTF8String()
		}

		a.mu.Lock()
		a.requests = append(a.requests, r)
		answer := a.answer
		a.mu.Unlock()

		if answer != nil {
			ans = answer(req)
		}

		if ans == nil {
			ans = a.backend.serve(context.Background(), nil, req)
		}
	}

	if err := c.write(ans); err != nil {
		a.t.Logf("agent %s: write answer: %v", a.host, err)
	}
}

func (c *agentConn) write(m *diameter.Message) error {
	b, err := m.Marshal()
	if err != nil {
		return err
	}

	_, err = c.c.WriteMsg(b, &sctp.SndRcvInfo{PPID: sctp.PPIDWireOrder(diameter.PPID)})

	return err
}

// advertise makes the agent advertise applications in place of Relay.
func (a *fakeAgent) advertise(applications ...uint32) {
	a.mu.Lock()
	a.applications = applications
	a.mu.Unlock()
}

func (a *fakeAgent) setAnswer(f func(req *diameter.Message) *diameter.Message) {
	a.mu.Lock()
	a.answer = f
	a.mu.Unlock()
}

// received returns the requests of a command that the agent received.
func (a *fakeAgent) received(command uint32) []agentRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	var out []agentRequest

	for _, r := range a.requests {
		if r.command == command {
			out = append(out, r)
		}
	}

	return out
}

// connections returns the Origin-Host of each connection the SMSC opened to the agent.
func (a *fakeAgent) connections() []string {
	a.mu.Lock()
	conns := slices.Clone(a.conns)
	a.mu.Unlock()

	var out []string

	for _, c := range conns {
		c.mu.Lock()
		out = append(out, c.origin)
		c.mu.Unlock()
	}

	return out
}

// relay sends a request to the SMSC on its newest connection, as the agent relays it from a node behind it, and
// returns the answer.
func (a *fakeAgent) relay(req *diameter.Message) *diameter.Message {
	a.t.Helper()

	a.mu.Lock()
	if len(a.conns) == 0 {
		a.mu.Unlock()
		a.t.Fatal("the SMSC is not connected to the agent")
	}

	c := a.conns[len(a.conns)-1]
	a.mu.Unlock()

	ch := make(chan *diameter.Message, 1)

	c.mu.Lock()
	c.hbh++
	req.HopByHopID = c.hbh
	req.EndToEndID = c.hbh
	c.pending[req.HopByHopID] = ch
	c.mu.Unlock()

	if err := c.write(req); err != nil {
		a.t.Fatalf("relay: %v", err)
	}

	select {
	case ans := <-ch:
		return ans
	case <-time.After(waitTimeout):
		a.t.Fatal("timed out waiting for the SMSC's answer")
		return nil
	}
}

// peerParams is the configuration of a peer for the agent.
func (a *fakeAgent) peerParams(routes ...api.PeerRoute) api.PeerParams {
	return api.PeerParams{Host: a.host, Addresses: []string{loopback.String()}, Port: a.port, Routes: routes}
}

func defaultSGd() api.PeerRoute {
	return api.PeerRoute{Default: true, Application: "sgd"}
}

func sgdRoute(realm string, priority int) api.PeerRoute {
	return api.PeerRoute{Realm: realm, Application: "sgd", Priority: priority}
}

// addPeer configures a peer, and waits for the SMSC to connect to it.
func (s *smsc) addPeer(t *testing.T, params api.PeerParams) api.Peer {
	t.Helper()

	var p api.Peer
	if code := s.api(t, http.MethodPost, "/api/v1/diameter/peers", params, &p); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/diameter/peers = %d", code)
	}

	s.waitForPeer(t, p.ID, "open")

	return p
}

func (s *smsc) peer(t *testing.T, id int64) api.Peer {
	t.Helper()

	var p api.Peer
	if code := s.api(t, http.MethodGet, fmt.Sprintf("/api/v1/diameter/peers/%d", id), nil, &p); code != http.StatusOK {
		t.Fatalf("GET peer %d = %d", id, code)
	}

	return p
}

func (s *smsc) waitForPeer(t *testing.T, id int64, state string) {
	t.Helper()

	eventually(t, fmt.Sprintf("peer %d %s", id, state), func() bool {
		p := s.peer(t, id)
		return p.Status != nil && p.Status.State == state
	})
}

func (c *fakeCore) setServingMME(name, realm string) {
	c.mu.Lock()
	c.servingMME, c.servingRealm = name, realm
	c.mu.Unlock()
}

// roamingSMSC returns an HSS that knows bob roaming on the visited MME, the visited MME behind an agent, and the
// SMSC.
func roamingSMSC(t *testing.T) (hss, mme *fakeCore, s *smsc) {
	t.Helper()

	hss, s = newSMSC(t, alice, bob)
	hss.setServingMME(visitedMME, visitedRealm)
	mme = newRealmFakeCore(t, visitedMME, visitedRealm, alice, bob)

	return hss, mme, s
}

func assertTFRVia(t *testing.T, agent *fakeAgent, n int) {
	t.Helper()

	got := agent.received(sgd.CommandMTForwardShortMessage)
	if len(got) != n {
		t.Fatalf("%s received %d TFRs, want %d", agent.host, len(got), n)
	}

	for _, r := range got {
		if r.destinationHost != visitedMME || r.destinationRealm != visitedRealm {
			t.Fatalf("TFR via %s to %s in %s, want %s in %s", agent.host, r.destinationHost, r.destinationRealm,
				visitedMME, visitedRealm)
		}
	}
}

func TestMessageIsDeliveredToAVisitedMMEViaAnEdgeAgent(t *testing.T) {
	hss, mme, s := roamingSMSC(t)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	s.addPeer(t, dea.peerParams(sgdRoute(visitedRealm, 0)))

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	assertTFRVia(t, dea, 1)

	if got := mme.deliveredTo(bob.imsi); len(got) != 1 {
		t.Fatalf("the visited MME got %d messages", len(got))
	}

	if got := hss.deliveredTo(bob.imsi); len(got) != 0 {
		t.Fatalf("the HSS got %d messages", len(got))
	}

	if attempts := s.attempts(t, sent[0].ID); attempts[len(attempts)-1].Node != visitedMME {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestMessageIsDeliveredViaTheDefaultRoute(t *testing.T) {
	_, mme, s := roamingSMSC(t)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	s.addPeer(t, dea.peerParams(defaultSGd()))

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	assertTFRVia(t, dea, 1)

	var status api.DiameterStatus
	s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

	last := status.Routes[len(status.Routes)-1]
	if !last.Default || last.Realm != "" || last.Application != "sgd" || !slices.Equal(last.Peers, []string{dea.host}) {
		t.Fatalf("routes = %+v", status.Routes)
	}
}

func TestRealmRouteIsPreferredOverTheDefault(t *testing.T) {
	_, mme, s := roamingSMSC(t)
	dea1 := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	dea2 := newFakeAgent(t, "dea2.ipx.example", ipxRealm, mme)
	s.addPeer(t, dea2.peerParams(defaultSGd()))
	s.addPeer(t, dea1.peerParams(sgdRoute(visitedRealm, 5)))

	for i := range 3 {
		sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, fmt.Sprintf("hello %d", i))
		s.waitForAPIStatus(t, sent[0].ID, "delivered")
	}

	assertTFRVia(t, dea1, 3)
	assertTFRVia(t, dea2, 0)
}

func TestDeliveryWithoutARouteToTheVisitedRealmIsRetried(t *testing.T) {
	_, _, s := roamingSMSC(t)

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")

	eventually(t, "a delivery attempt", func() bool { return len(s.attempts(t, sent[0].ID)) == 2 })

	attempts := s.attempts(t, sent[0].ID)
	if a := attempts[1]; a.Step != "delivery" || a.Node != visitedMME || a.Outcome != "unreachable" {
		t.Fatalf("delivery attempt = %+v", a)
	}

	if m := s.message(t, sent[0].ID); m.Status != "pending" {
		t.Fatalf("status = %s", m.Status)
	}
}

func TestDeliveryFailsOverToTheSecondEdgeAgent(t *testing.T) {
	t.Run("agent down", func(t *testing.T) {
		_, mme, s := roamingSMSC(t)
		dea1 := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
		dea2 := newFakeAgent(t, "dea2.ipx.example", ipxRealm, mme)
		p1 := s.addPeer(t, dea1.peerParams(sgdRoute(visitedRealm, 0)))
		s.addPeer(t, dea2.peerParams(sgdRoute(visitedRealm, 1)))

		dea1.shutdown()
		s.waitForPeer(t, p1.ID, "down")

		sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
		s.waitForAPIStatus(t, sent[0].ID, "delivered")

		assertTFRVia(t, dea2, 1)
	})

	t.Run("unable to deliver", func(t *testing.T) {
		_, mme, s := roamingSMSC(t)
		dea1 := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
		dea2 := newFakeAgent(t, "dea2.ipx.example", ipxRealm, mme)
		s.addPeer(t, dea1.peerParams(sgdRoute(visitedRealm, 0)))
		s.addPeer(t, dea2.peerParams(sgdRoute(visitedRealm, 1)))

		dea1.setAnswer(func(req *diameter.Message) *diameter.Message {
			return diameter.NewAnswer(req, dea1.identity(), diameter.ResultUnableToDeliver)
		})

		sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
		s.waitForAPIStatus(t, sent[0].ID, "delivered")

		assertTFRVia(t, dea1, 1)
		assertTFRVia(t, dea2, 1)

		if got := mme.deliveredTo(bob.imsi); len(got) != 1 {
			t.Fatalf("the visited MME got %d messages", len(got))
		}
	})
}

func TestDirectlyConnectedMMEIsPreferredOverARoute(t *testing.T) {
	_, mme, s := roamingSMSC(t)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	s.addPeer(t, dea.peerParams(sgdRoute(visitedRealm, 0), defaultSGd()))

	mme.connect(s)

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	assertTFRVia(t, dea, 0)

	if got := mme.deliveredTo(bob.imsi); len(got) != 1 {
		t.Fatalf("the MME got %d messages", len(got))
	}
}

func TestRoutingInfoGoesThroughAConfiguredDRA(t *testing.T) {
	// The HSS is behind the DRA: it never connects to the SMSC.
	hss := newFakeCore(t, alice, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	dra := newFakeAgent(t, "dra1.dra.example", "dra.example", hss)
	s.addPeer(t, dra.peerParams(api.PeerRoute{Realm: realm, Application: "s6c"}))

	// The routing answer names the HSS's own MME, which is connected.
	mme := newNamedFakeCore(t, "mme1."+realm, alice, bob)
	hss.setServingMME(mme.host, realm)
	mme.connect(s)

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello home")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	if got := dra.received(s6c.CommandSendRoutingInfoForSM); len(got) != 1 || got[0].destinationHost != "" ||
		got[0].destinationRealm != realm {
		t.Fatalf("routing requests via the DRA = %+v", got)
	}

	if got := mme.deliveredTo(bob.imsi); len(got) != 1 {
		t.Fatalf("the MME got %d messages", len(got))
	}

	var status api.DiameterStatus
	s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

	// The MME advertises S6c in the operator's realm, so it counts as an HSS too.
	r := status.Routes[0]
	if r.Realm != realm || r.Application != "s6c" || !slices.Contains(r.Peers, dra.host) {
		t.Fatalf("HSS route = %+v", r)
	}

	if got := values(parseFamilies(t, scrape(t, s)))["ellasmsc_hss_peers"]; got != float64(len(r.Peers)) {
		t.Fatalf("ellasmsc_hss_peers = %v, want %d", got, len(r.Peers))
	}
}

func TestDefaultRouteDoesNotCatchTheHomeRealm(t *testing.T) {
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.Settings.Delivery.RetryIntervals = []time.Duration{200 * time.Millisecond}

	hss := newFakeCore(t, alice, bob)
	s := startSMSC(t, cfg)
	hss.connect(s)

	homeMME := newNamedFakeCore(t, "mme1."+realm, alice, bob)
	hss.setServingMME(homeMME.host, realm)

	// The home MME connected once, then closed: it has left the peer table.
	homeMME.connect(s)

	if err := homeMME.node.SetPeers(nil); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the home MME to disconnect", func() bool {
		var status api.DiameterStatus
		s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

		return !slices.ContainsFunc(status.Peers, func(p api.DiameterPeer) bool { return p.Host == homeMME.host })
	})

	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, homeMME)
	p := s.addPeer(t, dea.peerParams(defaultSGd()))

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello home")

	eventually(t, "a delivery attempt", func() bool { return len(s.attempts(t, sent[0].ID)) == 2 })

	if a := s.attempts(t, sent[0].ID)[1]; a.Node != homeMME.host || a.Outcome != "unreachable" {
		t.Fatalf("delivery attempt = %+v", a)
	}

	if got := dea.received(sgd.CommandMTForwardShortMessage); len(got) != 0 {
		t.Fatalf("the default route took the home realm: %+v", got)
	}

	// An explicit route to the home realm sends it to the agent.
	params := dea.peerParams(defaultSGd(), sgdRoute(realm, 0))
	if code := s.api(t, http.MethodPut, fmt.Sprintf("/api/v1/diameter/peers/%d", p.ID), params, nil); code != http.StatusOK {
		t.Fatalf("PUT peer = %d", code)
	}

	eventually(t, "the TFR via the agent", func() bool {
		return len(dea.received(sgd.CommandMTForwardShortMessage)) == 1
	})

	if got := dea.received(sgd.CommandMTForwardShortMessage)[0]; got.destinationHost != homeMME.host {
		t.Fatalf("TFR via the agent = %+v", got)
	}
}

func TestMobileOriginatedMessageThroughAnEdgeAgent(t *testing.T) {
	hss, mme, s := roamingSMSC(t)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	s.addPeer(t, dea.peerParams(defaultSGd()))

	// The OFR of the visited MME, which the agent relays with its Origin-Host.
	mme.smsc = smscHost
	ofr := mme.request(sgd.CommandMOForwardShortMessage, sgd.ApplicationID,
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(t, serviceCentreAddress)),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, bob.imsi),
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(t, bob.msisdn))),
		diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, smsSubmit(t, alice.msisdn, "from abroad")),
	)

	if rc := resultOf(t, dea.relay(ofr)); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	// alice roams on the same MME.
	eventually(t, "delivery to alice", func() bool { return len(mme.deliveredTo(alice.imsi)) == 1 })

	if got := hss.deliveredTo(alice.imsi); len(got) != 0 {
		t.Fatalf("the HSS got %d messages", len(got))
	}

	m, err := s.store.GetMessage(context.Background(), 1)
	if err != nil || m.Origin != db.OriginMobile {
		t.Fatalf("stored message = %+v, %v", m, err)
	}
}

func TestPeersFollowAnOperatorChange(t *testing.T) {
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.Settings.Operator.MNC = "99"

	s := startSMSC(t, cfg)
	mme := newRealmFakeCore(t, visitedMME, visitedRealm, alice, bob)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	p := s.addPeer(t, dea.peerParams(defaultSGd()))

	if got := dea.connections(); !slices.Equal(got, []string{"smsc.node.epc.mnc099.mcc001.3gppnetwork.org"}) {
		t.Fatalf("connections = %v", got)
	}

	var current api.OperatorSettings
	s.api(t, http.MethodGet, "/api/v1/operator", nil, &current)

	current.MNC = "01"

	if code := s.api(t, http.MethodPut, "/api/v1/operator", current, nil); code != http.StatusOK {
		t.Fatalf("put operator settings = %d", code)
	}

	eventually(t, "a connection with the new identity", func() bool {
		return slices.Contains(dea.connections(), smscHost)
	})

	s.waitForPeer(t, p.ID, "open")

	hss := newFakeCore(t, alice, bob)
	hss.setServingMME(visitedMME, visitedRealm)
	hss.connect(s)

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	assertTFRVia(t, dea, 1)
}

func TestPeerUpdateKeepsTheConnection(t *testing.T) {
	_, mme, s := roamingSMSC(t)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	p := s.addPeer(t, dea.peerParams(sgdRoute("epc.mnc003.mcc003.3gppnetwork.org", 0)))
	since := s.peer(t, p.ID).Status.Since

	params := dea.peerParams(sgdRoute("epc.mnc003.mcc003.3gppnetwork.org", 0), sgdRoute(visitedRealm, 0))
	if code := s.api(t, http.MethodPut, fmt.Sprintf("/api/v1/diameter/peers/%d", p.ID), params, nil); code != http.StatusOK {
		t.Fatalf("PUT peer = %d", code)
	}

	eventually(t, "the new route", func() bool {
		var status api.DiameterStatus
		s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

		return slices.ContainsFunc(status.Routes, func(r api.DiameterRoute) bool {
			return r.Realm == visitedRealm && slices.Equal(r.Peers, []string{dea.host})
		})
	})

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	assertTFRVia(t, dea, 1)

	if got := s.peer(t, p.ID).Status; got.State != "open" || got.Since != since {
		t.Fatalf("status = %+v, since %s before the update", got, since)
	}

	if got := dea.connections(); len(got) != 1 {
		t.Fatalf("the SMSC opened %d connections", len(got))
	}
}

func TestPeerDeleteClosesTheConnection(t *testing.T) {
	_, mme, s := roamingSMSC(t)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)
	p := s.addPeer(t, dea.peerParams(defaultSGd()))

	if code := s.api(t, http.MethodDelete, fmt.Sprintf("/api/v1/diameter/peers/%d", p.ID), nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE peer = %d", code)
	}

	eventually(t, "the peer to leave the status", func() bool {
		var status api.DiameterStatus
		s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

		return !slices.ContainsFunc(status.Peers, func(p api.DiameterPeer) bool { return p.Configured })
	})

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello abroad")

	eventually(t, "a delivery attempt", func() bool { return len(s.attempts(t, sent[0].ID)) == 2 })
	assertTFRVia(t, dea, 0)
}

func TestPeersAreDialledAfterARestart(t *testing.T) {
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	mme := newRealmFakeCore(t, visitedMME, visitedRealm, alice, bob)
	dea := newFakeAgent(t, "dea1.ipx.example", ipxRealm, mme)

	s := startSMSC(t, cfg)
	p := s.addPeer(t, dea.peerParams(defaultSGd()))
	s.stop()

	s = startSMSC(t, cfg)
	s.waitForPeer(t, p.ID, "open")
}

// syncBuffer is a log destination that the SMSC writes to concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestConfiguredHSSWithoutAnS6cRouteIsLogged(t *testing.T) {
	var logs syncBuffer

	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))

	s := startSMSC(t, cfg)
	hss := newFakeCore(t, alice, bob)
	// The HSS listens, and gives the operator's realm, but the SMSC has no s6c route to it.
	hssDRA := newFakeAgent(t, coreHost, realm, hss)
	s.addPeer(t, hssDRA.peerParams(sgdRoute(visitedRealm, 0)))

	const warning = "advertises S6c, but has no s6c route"

	eventually(t, "the warning", func() bool { return strings.Contains(logs.String(), warning) })

	if !strings.Contains(logs.String(), "Diameter peer open") {
		t.Fatalf("logs = %s", logs.String())
	}

	other := newFakeAgent(t, "dra1."+realm, realm, hss)
	s.addPeer(t, other.peerParams(api.PeerRoute{Realm: realm, Application: "s6c"}))

	if n := strings.Count(logs.String(), warning); n != 1 {
		t.Fatalf("%d warnings, want 1: %s", n, logs.String())
	}
}

func TestPeerWithoutS6cIsNotAnHSS(t *testing.T) {
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	dra := newFakeAgent(t, "dra1.dra.example", "dra.example", newFakeCore(t, alice, bob))
	dra.advertise(sgd.ApplicationID)
	s.addPeer(t, dra.peerParams(api.PeerRoute{Realm: realm, Application: "s6c"}))

	var status api.DiameterStatus
	s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

	if r := status.Routes[0]; r.Application != "s6c" || len(r.Peers) != 0 {
		t.Fatalf("HSS route = %+v", r)
	}

	if got := values(parseFamilies(t, scrape(t, s)))["ellasmsc_hss_peers"]; got != 0 {
		t.Fatalf("ellasmsc_hss_peers = %v", got)
	}
}

// The HSS names an IP-SM-GW without its realm (TS 29.338 clause 5.3.3.6). One that is not connected is in the home
// realm, reached through the agent of its route.
func TestMessageIsDeliveredToAnIPSMGWViaAnAgent(t *testing.T) {
	hss, s := newSMSC(t, alice, bob)
	ipSMGW := newNamedFakeCore(t, "ipsmgw."+realm, alice, bob)
	dra := newFakeAgent(t, "dra1.dra.example", "dra.example", ipSMGW)
	s.addPeer(t, dra.peerParams(sgdRoute(realm, 0)))

	hss.mu.Lock()
	hss.ipSMGW = ipSMGW.host
	hss.mu.Unlock()

	sent := s.send(t, "+"+alice.msisdn, "+"+bob.msisdn, "hello over IMS")
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	got := dra.received(sgd.CommandMTForwardShortMessage)
	if len(got) != 1 || got[0].destinationHost != ipSMGW.host || got[0].destinationRealm != realm {
		t.Fatalf("TFRs via the DRA = %+v", got)
	}
}
