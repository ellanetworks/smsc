package smpp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testServer is a Server on a loopback port that accepts the password "secret", files each ESME under its system_id,
// and answers each submit_sm with a message_id counting from 1.
type testServer struct {
	*Server
	addr      string
	submitted chan *SubmitSM
	ids       atomic.Int64
	bound     chan *Session
}

func newTestServer(t *testing.T, mut func(*ServerConfig)) *testServer {
	t.Helper()

	ts := &testServer{submitted: make(chan *SubmitSM, 100), bound: make(chan *Session, 100)}
	cfg := ServerConfig{
		SystemID: "EllaSMSC",
		Session:  SessionConfig{ResponseTimeout: 2 * time.Second},
		Authenticate: func(_ context.Context, req BindRequest) BindOutcome {
			if req.Bind.Password != "secret" {
				return BindOutcome{Status: StatusInvPaswd}
			}

			return BindOutcome{Status: StatusOK, Data: "data of " + req.Bind.SystemID}
		},
		Handler: HandlerFunc(func(_ context.Context, _ *Session, req Request) Response {
			ts.submitted <- req.PDU.(*SubmitSM)

			return Response{PDU: &SubmitSMResp{MessageID: strconv.FormatInt(ts.ids.Add(1), 10)}}
		}),
		OnBound: func(s *Session) { ts.bound <- s },
	}

	if mut != nil {
		mut(&cfg)
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}

	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	ts.Server, ts.addr = srv, l.Addr().String()

	served := make(chan error, 1)

	go func() { served <- srv.Serve(l) }()

	t.Cleanup(func() {
		_ = srv.Close()

		if err := <-served; !errors.Is(err, ErrServerClosed) {
			t.Errorf("Serve = %v", err)
		}
	})

	return ts
}

func newTestClient(t *testing.T, addr string, mut func(*ClientConfig)) *Client {
	t.Helper()

	cfg := ClientConfig{
		Addr:       addr,
		Bind:       Bind{SystemID: "simota", Password: "secret"},
		Session:    SessionConfig{ResponseTimeout: 2 * time.Second},
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
	}

	if mut != nil {
		mut(&cfg)
	}

	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}

	c.Start()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_ = c.Close(ctx)
	})

	return c
}

func waitBound(t *testing.T, c *Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.WaitBound(ctx); err != nil {
		t.Fatalf("not bound: %v, state %+v", err, c.State())
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// TestLoopback runs our client against our server: bind, submit_sm both ways of deliver_sm, the registry.
func TestLoopback(t *testing.T) {
	ts := newTestServer(t, nil)

	var states []ConnStatus

	var mu sync.Mutex

	delivered := make(chan *DeliverSM, 10)
	c := newTestClient(t, ts.addr, func(cfg *ClientConfig) {
		cfg.Handler = HandlerFunc(func(_ context.Context, _ *Session, req Request) Response {
			d := req.PDU.(*DeliverSM)
			delivered <- d

			if string(d.ShortMessage) == "reject" {
				return Response{Status: StatusXPAppn}
			}

			return Response{}
		})
		cfg.OnStateChange = func(st ClientState) {
			mu.Lock()

			states = append(states, st.Status)
			mu.Unlock()
		}
	})

	waitBound(t, c)

	if st := c.State(); st.Status != ConnBound || st.BindType != StateBoundTRX || st.SystemID != "EllaSMSC" {
		t.Fatalf("state %+v", st)
	}

	sess := <-ts.bound
	if sess.Account() != "simota" || sess.Data() != "data of simota" || sess.Info().InterfaceVersion != InterfaceVersion {
		t.Fatalf("server session %+v", sess.Info())
	}

	id, err := c.SubmitSM(context.Background(), &SubmitSM{Message: message()})
	if err != nil || id != "1" {
		t.Fatalf("SubmitSM = %q, %v", id, err)
	}

	if m := <-ts.submitted; m.Destination.Addr != "15551230002" {
		t.Fatalf("submitted %+v", m)
	}

	if got := ts.Sessions("simota"); len(got) != 1 || got[0].State != StateBoundTRX || got[0].SystemID != "simota" {
		t.Fatalf("Sessions = %+v", got)
	}

	if got := ts.Sessions(""); len(got) != 1 {
		t.Fatalf("all sessions = %+v", got)
	}

	// deliver_sm with optional parameters: the client's bind was v3.4.
	receipt := &DeliverSM{Message: message()}
	receipt.ESMClass = 0x04
	receipt.Options.ReceiptedMessageID = new("1")
	receipt.Options.MessageState = new(StateDelivered)

	if _, err := ts.Deliver(context.Background(), "simota", receipt); err != nil {
		t.Fatal(err)
	}

	if d := <-delivered; *d.Options.ReceiptedMessageID != "1" || *d.Options.MessageState != StateDelivered {
		t.Fatalf("delivered %+v", d)
	}

	reject := &DeliverSM{Message: message()}
	reject.ShortMessage = []byte("reject")

	var se *StatusError
	if _, err := ts.Deliver(context.Background(), "simota", reject); !errors.As(err, &se) || se.Status != StatusXPAppn {
		t.Fatalf("Deliver = %v", err)
	}

	if _, err := ts.Deliver(context.Background(), "other", receipt); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Deliver to no session = %v", err)
	}

	// The account's sessions closed: the client binds again.
	if n := ts.CloseAccount("simota"); n != 1 {
		t.Fatalf("CloseAccount = %d", n)
	}

	waitFor(t, "a new bind", func() bool { return len(ts.Sessions("simota")) == 1 && ts.Sessions("simota")[0].ID != sess.ID() })
	waitBound(t, c)

	if st := c.State(); st.LastError == nil {
		t.Fatalf("state %+v, want the lost session as last error", st)
	}

	mu.Lock()
	defer mu.Unlock()

	if want := []ConnStatus{ConnConnecting, ConnBound, ConnDisconnected, ConnConnecting, ConnBound}; len(states) < len(want) {
		t.Fatalf("states %v, want %v", states, want)
	}
}

func TestClientBindRefused(t *testing.T) {
	ts := newTestServer(t, nil)
	c := newTestClient(t, ts.addr, func(cfg *ClientConfig) { cfg.Bind.Password = "wrong" })

	waitFor(t, "a failed bind", func() bool { return c.State().LastError != nil })

	var se *StatusError
	if st := c.State(); st.Status == ConnBound || !errors.As(st.LastError, &se) || se.Status != StatusInvPaswd ||
		se.Command != CmdBindTransceiverResp {
		t.Fatalf("state %+v", st)
	}

	if _, err := c.SubmitSM(context.Background(), &SubmitSM{Message: message()}); !errors.Is(err, ErrNoSession) {
		t.Fatalf("SubmitSM unbound = %v", err)
	}
}

func TestClientSubmitRefused(t *testing.T) {
	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Handler = HandlerFunc(func(context.Context, *Session, Request) Response {
			return Response{Status: StatusInvDstAdr}
		})
	})
	c := newTestClient(t, ts.addr, nil)
	waitBound(t, c)

	var se *StatusError
	if _, err := c.SubmitSM(context.Background(), &SubmitSM{Message: message()}); !errors.As(err, &se) ||
		se.Status != StatusInvDstAdr || se.Command != CmdSubmitSMResp {
		t.Fatalf("SubmitSM = %v", err)
	}
}

func TestClientTransmitterAndReceiver(t *testing.T) {
	ts := newTestServer(t, nil)
	tx := newTestClient(t, ts.addr, func(cfg *ClientConfig) { cfg.Bind.ID = CmdBindTransmitter })
	rx := newTestClient(t, ts.addr, func(cfg *ClientConfig) { cfg.Bind.ID = CmdBindReceiver })

	waitBound(t, tx)
	waitBound(t, rx)

	if _, err := tx.SubmitSM(context.Background(), &SubmitSM{Message: message()}); err != nil {
		t.Fatal(err)
	}

	var se *StateError
	if _, err := rx.SubmitSM(context.Background(), &SubmitSM{Message: message()}); !errors.As(err, &se) {
		t.Fatalf("submit_sm on a receiver = %v", err)
	}

	// deliver_sm goes to the receiver only.
	for range 3 {
		info, err := ts.Deliver(context.Background(), "simota", &DeliverSM{Message: message()})
		if err != nil || info.State != StateBoundRX {
			t.Fatalf("Deliver = %+v, %v", info, err)
		}
	}
}

func TestMaxSessions(t *testing.T) {
	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Authenticate = func(context.Context, BindRequest) BindOutcome {
			return BindOutcome{Status: StatusOK, Account: "acct", MaxSessions: 1}
		}
	})

	first := newTestClient(t, ts.addr, nil)
	waitBound(t, first)

	second := newTestClient(t, ts.addr, nil)
	waitFor(t, "a refused bind", func() bool { return second.State().LastError != nil })

	var se *StatusError
	if err := second.State().LastError; !errors.As(err, &se) || se.Status != StatusBindFail {
		t.Fatalf("second bind: %v", err)
	}

	if got := ts.Sessions("acct"); len(got) != 1 {
		t.Fatalf("Sessions = %+v", got)
	}
}

// TestShutdown checks that Shutdown unbinds the bound sessions (§4.2) and closes the rest.
func TestShutdown(t *testing.T) {
	ts := newTestServer(t, nil)
	c := newTestClient(t, ts.addr, nil)
	waitBound(t, c)

	sess := <-ts.bound

	// A connection that never binds.
	raw, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ts.addr)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = raw.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := ts.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	if !errors.Is(sess.Err(), ErrUnbound) {
		t.Fatalf("server session closed for %v", sess.Err())
	}

	waitFor(t, "the client to lose its session", func() bool { return c.State().Status != ConnBound })

	_ = raw.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := raw.Read(make([]byte, 1)); err == nil {
		t.Fatal("the unbound connection is still open")
	}
}

// TestClientClose checks that Close unbinds (§4.2).
func TestClientClose(t *testing.T) {
	ts := newTestServer(t, nil)
	c := newTestClient(t, ts.addr, nil)
	waitBound(t, c)

	sess := <-ts.bound

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if c.State().Status != ConnClosed {
		t.Fatalf("state %+v", c.State())
	}

	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the server session did not close")
	}

	if !errors.Is(sess.Err(), ErrUnbound) {
		t.Fatalf("server session closed for %v", sess.Err())
	}
}

// TestClientRebindsAfterServerRestart checks the backoff loop against a listener that comes back.
func TestClientRebindsAfterServerRestart(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	c := newTestClient(t, addr, nil)
	waitFor(t, "a failed connection", func() bool { return c.State().LastError != nil })

	if st := c.State(); st.Status == ConnBound || st.NextAttempt.IsZero() && st.Status == ConnDisconnected {
		t.Fatalf("state %+v", st)
	}

	srv, err := NewServer(ServerConfig{
		SystemID:     "EllaSMSC",
		Authenticate: func(context.Context, BindRequest) BindOutcome { return BindOutcome{} },
		Handler:      submitOK,
	})
	if err != nil {
		t.Fatal(err)
	}

	l, err = (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Skipf("port %s taken again: %v", addr, err)
	}

	go func() { _ = srv.Serve(l) }()

	t.Cleanup(func() { _ = srv.Close() })

	waitBound(t, c)
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smsc"}, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestTLS(t *testing.T) {
	cert := selfSigned(t)

	var sawTLS atomic.Bool

	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		auth := cfg.Authenticate
		cfg.Authenticate = func(ctx context.Context, req BindRequest) BindOutcome {
			sawTLS.Store(req.TLS)

			return auth(ctx, req)
		}
	})

	pool := x509.NewCertPool()

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	pool.AddCert(leaf)

	c := newTestClient(t, ts.addr, func(cfg *ClientConfig) {
		cfg.TLS = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	})
	waitBound(t, c)

	if !sawTLS.Load() {
		t.Fatal("Authenticate did not see TLS")
	}

	if id, err := c.SubmitSM(context.Background(), &SubmitSM{Message: message()}); err != nil || id != "1" {
		t.Fatalf("SubmitSM = %q, %v", id, err)
	}

	// A client without the CA fails the handshake, and says so.
	bad := newTestClient(t, ts.addr, func(cfg *ClientConfig) { cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12} })
	waitFor(t, "a failed handshake", func() bool { return bad.State().LastError != nil })

	// A plain client to a TLS server does not bind.
	plain := newTestClient(t, ts.addr, func(cfg *ClientConfig) {
		cfg.Session.ResponseTimeout = 200 * time.Millisecond
	})
	waitFor(t, "a failed plain bind", func() bool { return plain.State().LastError != nil })
}

func TestConfigErrors(t *testing.T) {
	auth := func(context.Context, BindRequest) BindOutcome { return BindOutcome{} }

	for _, cfg := range []ServerConfig{
		{Authenticate: auth, Handler: submitOK},
		{SystemID: "0123456789abcdef", Authenticate: auth, Handler: submitOK},
		{SystemID: "s", Handler: submitOK},
		{SystemID: "s", Authenticate: auth},
	} {
		if _, err := NewServer(cfg); err == nil {
			t.Errorf("NewServer(%+v) accepted", cfg)
		}
	}

	for _, cfg := range []ClientConfig{
		{Addr: "nohost"},
		{Addr: "127.0.0.1:2775", Bind: Bind{ID: CmdSubmitSM}},
		{Addr: "127.0.0.1:2775", Bind: Bind{Password: "toolongpw"}},
	} {
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("NewClient(%+v) accepted", cfg)
		}
	}
}

func TestBackoff(t *testing.T) {
	c := &Client{cfg: ClientConfig{MinBackoff: time.Second, MaxBackoff: 10 * time.Second}}

	for _, tc := range []struct {
		n    int
		want time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {4, 8 * time.Second}, {5, 10 * time.Second}, {50, 10 * time.Second}} {
		got := c.backoff(tc.n)
		if got > tc.want || got < tc.want*4/5 {
			t.Errorf("backoff(%d) = %v, want %v less up to a fifth", tc.n, got, tc.want)
		}
	}
}

// TestBindClosedDuringAuthenticate checks that a session closed while Authenticate runs, here by the
// session_init_timer, holds no MaxSessions slot: the account binds again.
func TestBindClosedDuringAuthenticate(t *testing.T) {
	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Session.SessionInitTimeout = 100 * time.Millisecond
		cfg.Authenticate = func(_ context.Context, req BindRequest) BindOutcome {
			if req.Bind.SystemID == "slow" {
				<-req.Session.Done()
			}

			return BindOutcome{Status: StatusOK, Account: "acct", MaxSessions: 1}
		}
	})

	c, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ts.addr)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = c.Close() }()

	b, err := Encode(&Bind{ID: CmdBindTransceiver, SystemID: "slow", InterfaceVersion: InterfaceVersion}, StatusOK, 1)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the slow session to close", func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()

		return len(ts.sessions) == 0
	})

	ts.mu.Lock()
	reserved, accounts := len(ts.reserved), len(ts.accounts)
	ts.mu.Unlock()

	if reserved != 0 || accounts != 0 {
		t.Fatalf("%d sessions reserved in %d accounts after the session closed", reserved, accounts)
	}

	fresh := newTestClient(t, ts.addr, func(cfg *ClientConfig) { cfg.Bind.SystemID = "fresh" })
	waitBound(t, fresh)
}

// TestDeliverSkipsUnbinding checks that Deliver does not pick a session that is unbinding.
func TestDeliverSkipsUnbinding(t *testing.T) {
	ts := newTestServer(t, nil)

	// A raw ESME that binds and never answers the unbind.
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", ts.addr)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	raw := &rawPeer{t: t, c: conn, r: bufio.NewReader(conn)}
	raw.send(&Bind{ID: CmdBindTransceiver, SystemID: "simota", Password: "secret", InterfaceVersion: InterfaceVersion},
		StatusOK, 1)
	raw.expect(CmdBindTransceiverResp, StatusOK, 1)

	unbinding := <-ts.bound

	go func() { _ = unbinding.Unbind(context.Background()) }()

	if h, _ := raw.read(); h.ID != CmdUnbind {
		t.Fatalf("raw ESME got %s", h.ID)
	}

	c := newTestClient(t, ts.addr, nil)
	waitBound(t, c)

	for range 3 {
		info, err := ts.Deliver(context.Background(), "simota", &DeliverSM{Message: message()})
		if err != nil || info.ID == unbinding.ID() {
			t.Fatalf("Deliver = session %d, %v; session %d is unbinding", info.ID, err, unbinding.ID())
		}
	}
}

// TestClientCloseWhileBinding checks that Close unbinds a session that the SMSC binds while Close runs (§4.2).
func TestClientCloseWhileBinding(t *testing.T) {
	authenticating := make(chan struct{})
	release := make(chan struct{})

	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Authenticate = func(context.Context, BindRequest) BindOutcome {
			close(authenticating)
			<-release

			return BindOutcome{Status: StatusOK}
		}
	})

	c := newTestClient(t, ts.addr, nil)

	<-authenticating

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	closed := make(chan error, 1)

	go func() { closed <- c.Close(ctx) }()

	time.Sleep(50 * time.Millisecond) // Close has cancelled the client

	close(release)

	sess := <-ts.bound

	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the server session did not close")
	}

	if !errors.Is(sess.Err(), ErrUnbound) {
		t.Fatalf("server session closed for %v", sess.Err())
	}
}

// TestBackoffAfterShortSession checks that a session that binds and soon fails does not reset the backoff: an SMSC
// that binds then drops is retried less and less often.
func TestBackoffAfterShortSession(t *testing.T) {
	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.OnBound = func(s *Session) { go func() { _ = s.Close() }() }
	})

	var (
		mu    sync.Mutex
		waits []time.Duration
	)

	newTestClient(t, ts.addr, func(cfg *ClientConfig) {
		cfg.MinBackoff, cfg.MaxBackoff = 10*time.Millisecond, 5*time.Second
		cfg.OnStateChange = func(st ClientState) {
			if st.Status == ConnDisconnected {
				mu.Lock()

				waits = append(waits, st.NextAttempt.Sub(st.LastErrorAt))
				mu.Unlock()
			}
		}
	})

	waitFor(t, "four failed sessions", func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(waits) >= 4
	})

	mu.Lock()
	defer mu.Unlock()

	// 10, 20, 40, 80 ms, less up to a fifth.
	if waits[3] < 60*time.Millisecond {
		t.Fatalf("waits %v: the backoff did not grow", waits)
	}
}
