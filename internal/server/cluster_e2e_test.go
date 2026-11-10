package server_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/server"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/pdu"
	"golang.org/x/crypto/bcrypt"
)

func freePort(t *testing.T) int {
	t.Helper()

	var lc net.ListenConfig

	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = ln.Close() }()

	return ln.Addr().(*net.TCPAddr).Port
}

func clusterConfig(t *testing.T) smscConfig {
	t.Helper()

	skipIfNoSCTP(t)

	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.Cluster = &config.Cluster{Address: loopback, Port: freePort(t)}

	return cfg
}

func startJoiner(t *testing.T, cfg smscConfig) *smsc {
	t.Helper()

	store, err := db.Open(context.Background(), cfg.DB.Path)
	if err != nil {
		t.Fatal(err)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = discardLogger()
	}

	srv := &server.Server{Config: cfg.Config, Logger: logger}
	srv.SetAttemptTimeout(cfg.AttemptTimeout)
	srv.SetBcryptCost(bcrypt.MinCost)

	if err := srv.Start(context.Background()); err != nil {
		_ = store.Close()

		t.Fatalf("Start: %v", err)
	}

	s := &smsc{server: srv, store: store, client: http.DefaultClient, url: "http://" + srv.APIAddr().String()}

	t.Cleanup(func() { s.stop() })

	return s
}

const fenceWait = 90 * time.Second

func eventuallyOK(t *testing.T, what string, f func() bool) {
	t.Helper()

	eventuallyWithin(t, waitTimeout, what, f)
}

func eventuallyWithin(t *testing.T, timeout time.Duration, what string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if f() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

func TestClusterJoinThroughTheAPI(t *testing.T) {
	a := startSMSC(t, clusterConfig(t))

	var token cluster.Token
	if code := a.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	bCfg := clusterConfig(t)
	bCfg.Diameter.HostLabel = "smsc-b"
	b := startJoiner(t, bCfg)

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusConflict {
		t.Fatalf("second join = %d, want %d", code, http.StatusConflict)
	}

	var login api.LoginResponse

	eventuallyOK(t, "a login on the joined node", func() bool {
		return b.apiAs(t, "", http.MethodPost, "/api/v1/auth/login",
			api.LoginParams{Email: "admin@example.com", Password: "correct horse"}, &login) == http.StatusOK
	})

	b.token = login.Token

	var status cluster.Status

	eventuallyOK(t, "two members", func() bool {
		return b.api(t, http.MethodGet, "/api/v1/cluster", nil, &status) == http.StatusOK && len(status.Members) == 2
	})

	eventuallyOK(t, "a Diameter host per node", func() bool {
		var da, dbs api.DiameterStatus

		a.api(t, http.MethodGet, "/api/v1/diameter", nil, &da)
		b.api(t, http.MethodGet, "/api/v1/diameter", nil, &dbs)

		return da.Host == a.host() && dbs.Host == b.host() && da.Host != dbs.Host
	})

	if code := b.api(t, http.MethodPost, "/api/v1/users", api.CreateUserParams{
		Email: "ops@example.com", Password: "correct horse", RoleID: db.RoleReadOnly,
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/users on the follower = %d", code)
	}

	eventuallyOK(t, "the user on the leader", func() bool {
		return a.api(t, http.MethodGet, "/api/v1/users/ops@example.com", nil, nil) == http.StatusOK
	})

	var member string

	for _, m := range status.Members {
		if m.DiameterLabel == b.server.DiameterLabel() {
			member = m.NodeID
		}
	}

	var bs api.Status
	if code := b.apiAs(t, "", http.MethodGet, "/api/v1/status", nil, &bs); code != http.StatusOK ||
		bs.NodeID != member || bs.DiameterLabel != "smsc-b" || bs.Generated || bs.DiameterHost != "smsc-b.node."+realm {
		t.Fatalf("GET /api/v1/status on the joined node = %d, %+v, want node %s", code, bs, member)
	}

	if b.host() != "smsc-b.node."+realm {
		t.Fatalf("the joined node's Diameter host = %s", b.host())
	}

	for _, m := range status.Members {
		want := settings.NodeLabel(m.NodeID)
		if m.NodeID == member {
			want = "smsc-b"
		}

		if m.DiameterLabel != want || m.DiameterHost != want+".node."+realm || m.Generated != (m.NodeID != member) {
			t.Fatalf("member %+v, want the Diameter host label %s", m, want)
		}
	}

	if code := a.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	cCfg := clusterConfig(t)
	cCfg.Diameter.HostLabel = "smsc-b"
	c := startJoiner(t, cCfg)

	if code := c.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusConflict {
		t.Fatalf("join with a member's Diameter host label = %d, want %d", code, http.StatusConflict)
	}

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{DisplayName: "paris-1"}, nil); code != http.StatusOK {
		t.Fatalf("PUT member = %d", code)
	}

	eventuallyOK(t, "the display name on the joined node", func() bool {
		var st api.Status

		return b.apiAs(t, "", http.MethodGet, "/api/v1/status", nil, &st) == http.StatusOK && st.DisplayName == "paris-1"
	})

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{DisplayName: strings.Repeat("a", 64)}, nil); code != http.StatusBadRequest {
		t.Fatalf("PUT a long display name = %d, want %d", code, http.StatusBadRequest)
	}

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{}, nil); code != http.StatusOK {
		t.Fatalf("PUT an empty display name = %d", code)
	}

	eventuallyOK(t, "the cleared display name", func() bool {
		var st cluster.Status

		a.api(t, http.MethodGet, "/api/v1/cluster", nil, &st)

		for _, m := range st.Members {
			if m.NodeID == member {
				return m.DisplayName == ""
			}
		}

		return false
	})

	if code := a.api(t, http.MethodDelete, "/api/v1/cluster/members/"+member, nil, nil); code != http.StatusConflict {
		t.Fatalf("DELETE an active member = %d, want %d", code, http.StatusConflict)
	}

	if code := a.api(t, http.MethodPost, "/api/v1/cluster/members/"+member+"/drain", nil, nil); code != http.StatusOK {
		t.Fatalf("POST drain = %d", code)
	}

	eventuallyOK(t, "the member to drain", func() bool {
		var st cluster.Status

		a.api(t, http.MethodGet, "/api/v1/cluster", nil, &st)

		for _, m := range st.Members {
			if m.NodeID == member {
				return m.DrainState == "drained"
			}
		}

		return false
	})

	if code := a.api(t, http.MethodDelete, "/api/v1/cluster/members/"+member, nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE member = %d", code)
	}

	eventuallyOK(t, "the removed node to fail readiness", func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, b.url+"/api/v1/readyz", nil)
		if err != nil {
			return false
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode == http.StatusServiceUnavailable
	})
}

func joinedPair(t *testing.T) (*smsc, *smsc) {
	t.Helper()

	a := startSMSC(t, clusterConfig(t))

	var token cluster.Token
	if code := a.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	b := startJoiner(t, clusterConfig(t))

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	return a, b
}

func (c *fakeCore) connectAll(nodes ...*smsc) {
	c.t.Helper()

	var peers []diameter.Peer

	for i, s := range nodes {
		addr, ok := s.server.Addr().(*sctp.SCTPAddr)
		if !ok {
			c.t.Fatalf("unexpected SMSC address %v", s.server.Addr())
		}

		peers = append(peers, diameter.Peer{
			ID:         "smsc" + strconv.Itoa(i),
			Host:       s.server.DiameterHost(),
			Addresses:  []netip.Addr{loopback},
			Transports: []diameter.Transport{diameter.TransportSCTP},
			Dial:       &diameter.Dial{Port: uint16(addr.Port)},
			Applications: []diameter.Application{
				{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
				{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
			},
		})
	}

	c.smsc = nodes[0].server.DiameterHost()

	if err := c.node.SetPeers(peers); err != nil {
		c.t.Fatalf("SetPeers: %v", err)
	}

	for _, s := range nodes {
		host := s.server.DiameterHost()

		eventuallyOK(c.t.(*testing.T), "the core connected to "+host, func() bool {
			for _, p := range c.node.Peers() {
				if strings.EqualFold(p.Host, host) && p.State == diameter.PeerOpen {
					return true
				}
			}

			return false
		})
	}
}

// RFC 6733 section 6.1
func TestLeaderDeliversToACoreConnectedToEveryNode(t *testing.T) {
	a, b := joinedPair(t)

	core := newFakeCore(t, alice, bob)
	core.connectAll(b, a)

	if rc := core.submit(alice, bob, "to every node"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	a.waitForStatus(t, 1, db.StatusDelivered)

	if got := core.deliveredTo(bob.imsi); len(got) != 1 {
		t.Fatalf("messages delivered to bob = %d", len(got))
	}
}

func smppClusterConfig(t *testing.T) smscConfig {
	t.Helper()

	cfg := clusterConfig(t)
	cfg.SMPP = &config.SMPP{Address: loopback}

	return cfg
}

func TestESMEOnAFollowerGetsItsReceiptFromItsNode(t *testing.T) {
	a := startSMSC(t, smppClusterConfig(t))

	if code := a.api(t, http.MethodPost, "/api/v1/users", api.CreateUserParams{
		Email: "ota@example.com", Password: "correct horse", RoleID: db.RoleSubmitter,
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/users = %d", code)
	}

	if code := a.api(t, http.MethodPost, "/api/v1/esmes", api.CreateESMEParams{
		SystemID: esmeSystemID, User: "ota@example.com", Password: ptrTo(esmePassword),
		AllowedNetworks: []string{"127.0.0.0/8"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/esmes = %d", code)
	}

	var token cluster.Token
	if code := a.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	b := startJoiner(t, smppClusterConfig(t))

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	var login api.LoginResponse

	eventuallyOK(t, "a login on the joined node", func() bool {
		return b.apiAs(t, "", http.MethodPost, "/api/v1/auth/login",
			api.LoginParams{Email: "admin@example.com", Password: "correct horse"}, &login) == http.StatusOK
	})

	b.token = login.Token

	eventuallyOK(t, "the ESME on the joined node", func() bool {
		return b.api(t, http.MethodGet, "/api/v1/esmes/"+esmeSystemID, nil, nil) == http.StatusOK
	})

	core := newFakeCore(t, alice, bob)
	core.connectAll(a, b)

	esme := bindESME(t, b, trx, gosmpp.NonTLSDialer)
	id := submitted(t, esme, otaSubmit(t, 0x01))

	eventuallyOK(t, "the SMS-DELIVER", func() bool { return len(core.forwardedTo(bob.imsi)) == 1 })

	receipt := next[*pdu.DeliverSM](t, esme)
	text, _ := receipt.Message.GetMessage()

	if receipt.EsmClass != 0x04 || !strings.HasPrefix(text, receiptText(id)+"dlvrd:001 ") {
		t.Fatalf("receipt esm_class %#x %q", receipt.EsmClass, text)
	}

	for range 2 {
		bindESME(t, a, rx, gosmpp.NonTLSDialer)
	}

	bindESME(t, b, rx, gosmpp.NonTLSDialer)

	if _, err := gosmpp.NewSession(gosmpp.RXConnector(gosmpp.NonTLSDialer, gosmpp.Auth{
		SMSC: b.server.SMPPAddr().String(), SystemID: esmeSystemID, Password: esmePassword,
	}), gosmpp.Settings{ReadTimeout: 5 * time.Second}, -1); err == nil {
		t.Fatal("a fifth session across the cluster was bound")
	}
}

func TestDrainingTheLeaderHandsItOver(t *testing.T) {
	a, b := joinedPair(t)

	var st cluster.Status
	if code := a.api(t, http.MethodGet, "/api/v1/cluster", nil, &st); code != http.StatusOK {
		t.Fatalf("GET /api/v1/cluster = %d", code)
	}

	if code := a.api(t, http.MethodPost, "/api/v1/cluster/members/"+st.NodeID+"/drain", nil, nil); code != http.StatusOK {
		t.Fatalf("POST drain = %d", code)
	}

	deadline := time.Now().Add(time.Minute)

	for {
		var after cluster.Status

		a.api(t, http.MethodGet, "/api/v1/cluster", nil, &after)

		drained := false

		for _, m := range after.Members {
			if m.NodeID == st.NodeID && m.DrainState == "drained" {
				drained = true
			}
		}

		if drained && after.LeaderID != "" && after.LeaderID != st.NodeID {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("leader %s, members %+v", after.LeaderID, after.Members)
		}

		time.Sleep(100 * time.Millisecond)
	}

	if code := b.apiAs(t, a.token, http.MethodGet, "/api/v1/cluster", nil, nil); code != http.StatusOK {
		t.Fatalf("GET /api/v1/cluster on the new leader = %d", code)
	}
}

func TestNodeRemovedWhileDownIsFencedOnRestart(t *testing.T) {
	a := startSMSC(t, clusterConfig(t))

	var token cluster.Token
	if code := a.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	bCfg := clusterConfig(t)
	bCfg.Diameter.HostLabel = "smsc-b"
	b := startJoiner(t, bCfg)

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	var bs api.Status
	if code := b.apiAs(t, "", http.MethodGet, "/api/v1/status", nil, &bs); code != http.StatusOK {
		t.Fatalf("GET /api/v1/status = %d", code)
	}

	eventuallyOK(t, "the joined node to report", func() bool {
		var st cluster.Status

		a.api(t, http.MethodGet, "/api/v1/cluster", nil, &st)

		for _, m := range st.Members {
			if m.NodeID == bs.NodeID {
				return m.Healthy != nil
			}
		}

		return false
	})

	b.stop()

	if code := a.api(t, http.MethodDelete, "/api/v1/cluster/members/"+bs.NodeID+"?force=true", nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE member = %d", code)
	}

	b = startJoiner(t, bCfg)

	addr, ok := b.server.Addr().(*sctp.SCTPAddr)
	if !ok {
		t.Fatalf("Diameter address %v", b.server.Addr())
	}

	eventuallyWithin(t, fenceWait, "the restarted node to fail its member check", func() bool {
		code, res := b.readyz(t)

		for _, c := range res.Checks {
			if c.Name == cluster.CheckMember {
				return code == http.StatusServiceUnavailable && !c.OK
			}
		}

		return false
	})

	eventuallyOK(t, "the fenced node to close its Diameter listener", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		conn, err := sctp.Dial(ctx, "sctp", nil, addr, sctp.InitMsg{NumOstreams: 2, MaxInstreams: 5, MaxAttempts: 1})
		if err == nil {
			_ = conn.Close()
			return false
		}

		return true
	})

	b.stop()

	b = startJoiner(t, bCfg)

	if b.server.Addr() != nil {
		t.Fatalf("the fenced node serves Diameter on %v", b.server.Addr())
	}

	livez, err := http.NewRequestWithContext(context.Background(), http.MethodGet, b.url+"/api/v1/livez", nil)
	if err != nil {
		t.Fatal(err)
	}

	live, err := b.client.Do(livez)
	if err != nil {
		t.Fatal(err)
	}

	_ = live.Body.Close()

	if live.StatusCode != http.StatusNoContent {
		t.Fatalf("GET /api/v1/livez on the fenced node = %d", live.StatusCode)
	}

	code, res := b.readyz(t)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/readyz on the fenced node = %d", code)
	}

	if i := slices.IndexFunc(res.Checks, func(c cluster.Check) bool { return c.Name == cluster.CheckMember }); i < 0 ||
		res.Checks[i].OK || !strings.Contains(res.Checks[i].Detail, "removed") {
		t.Fatalf("checks of the fenced node = %+v", res.Checks)
	}

	var st api.Status
	if code := b.apiAs(t, "", http.MethodGet, "/api/v1/status", nil, &st); code != http.StatusOK ||
		st.Formation.State != cluster.FormationRemoved || st.NodeID != bs.NodeID {
		t.Fatalf("GET /api/v1/status on the fenced node = %d, %+v", code, st)
	}

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/init", map[string]string{"email": "a@b.c", "password": "p"}, nil); code != http.StatusGone {
		t.Fatalf("POST /api/v1/init on the fenced node = %d, want %d", code, http.StatusGone)
	}
}

func (s *smsc) readyz(t *testing.T) (int, api.Readiness) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.url+"/api/v1/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = resp.Body.Close() }()

	var body struct {
		Result api.Readiness `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, body.Result
}

func readyzCheck(t *testing.T, s *smsc, name string) (int, cluster.Check, bool) {
	t.Helper()

	code, res := s.readyz(t)

	for _, c := range res.Checks {
		if c.Name == name {
			return code, c, true
		}
	}

	return code, cluster.Check{}, false
}

func TestDiameterStaysClosedUntilTheClusterRecordsTheLabel(t *testing.T) {
	aCfg := clusterConfig(t)
	aCfg.Diameter.HostLabel = "smsc-a"
	a := startSMSC(t, aCfg)

	var token cluster.Token
	if code := a.api(t, http.MethodPost, "/api/v1/cluster/tokens", api.CreateJoinTokenParams{}, &token); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/cluster/tokens = %d", code)
	}

	bCfg := clusterConfig(t)
	bCfg.Diameter.HostLabel = "smsc-b"
	b := startJoiner(t, bCfg)

	if code := b.apiAs(t, "", http.MethodPost, "/api/v1/cluster/join", api.JoinClusterParams{Token: token.Token}, nil); code != http.StatusOK {
		t.Fatalf("POST /api/v1/cluster/join = %d", code)
	}

	eventuallyOK(t, "Diameter open on the joined node", func() bool {
		return b.server.Addr() != nil && b.server.DiameterLabel() == "smsc-b"
	})

	b.stop()

	bCfg.Diameter.HostLabel = "smsc-a"
	b = startJoiner(t, bCfg)

	eventuallyOK(t, "the label check to fail on the restarted node", func() bool {
		code, c, ok := readyzCheck(t, b, cluster.CheckLabel)
		return ok && code == http.StatusServiceUnavailable && !c.OK && strings.Contains(c.Detail, "smsc-a")
	})

	if addr := b.server.Addr(); addr != nil {
		t.Fatalf("Diameter open on %v with another member's label", addr)
	}

	b.stop()

	bCfg.Diameter.HostLabel = "smsc-new"
	b = startJoiner(t, bCfg)

	eventuallyOK(t, "Diameter open with the new label once the cluster records it", func() bool {
		_, c, ok := readyzCheck(t, b, cluster.CheckLabel)
		return ok && c.OK && b.server.Addr() != nil && b.server.DiameterLabel() == "smsc-new"
	})
}
