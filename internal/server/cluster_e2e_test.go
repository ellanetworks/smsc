package server_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/server"
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
	cfg.Cluster = &config.Cluster{Address: loopback, Port: freePort(t), PerformanceMultiplier: 1}

	return cfg
}

func startJoiner(t *testing.T, cfg smscConfig) *smsc {
	t.Helper()

	store, err := db.Open(context.Background(), cfg.DB.Path)
	if err != nil {
		t.Fatal(err)
	}

	srv := &server.Server{Config: cfg.Config, Logger: discardLogger()}
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

func eventuallyOK(t *testing.T, what string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

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

	b := startJoiner(t, clusterConfig(t))

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

		return da.Host == "smsc.node.epc.mnc001.mcc001.3gppnetwork.org" &&
			dbs.Host == "smsc2.node.epc.mnc001.mcc001.3gppnetwork.org"
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
		if m.DiameterLabel == "smsc2" {
			member = m.NodeID
		}
	}

	if code := a.api(t, http.MethodPut, "/api/v1/cluster/members/"+member, api.UpdateMemberParams{DiameterLabel: "west"}, nil); code != http.StatusOK {
		t.Fatalf("PUT member = %d", code)
	}

	eventuallyOK(t, "the new Diameter host", func() bool {
		var d api.DiameterStatus

		b.api(t, http.MethodGet, "/api/v1/diameter", nil, &d)

		return d.Host == "west.node.epc.mnc001.mcc001.3gppnetwork.org"
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
