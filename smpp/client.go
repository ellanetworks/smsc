package smpp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// The default rebind backoff of ClientConfig.
const (
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = time.Minute
)

// ConnStatus is where a client's connection is.
type ConnStatus string

const (
	// ConnConnecting is a client connecting and binding.
	ConnConnecting ConnStatus = "connecting"
	// ConnBound is a client bound.
	ConnBound ConnStatus = "bound"
	// ConnDisconnected is a client waiting to connect again, after a failure.
	ConnDisconnected ConnStatus = "disconnected"
	// ConnClosed is a client closed.
	ConnClosed ConnStatus = "closed"
)

// ClientState is the observable state of a client, for a user interface.
type ClientState struct {
	Status ConnStatus
	// Since is when Status was entered.
	Since time.Time
	// BindType is the bind state while bound.
	BindType State
	// SystemID is the SMSC's system_id while bound.
	SystemID string
	// LastError is the last failure to connect, to bind, or of a bound session, and LastErrorAt its time. They
	// stay after a successful bind, for the record.
	LastError   error
	LastErrorAt time.Time
	// NextAttempt is when a disconnected client connects again.
	NextAttempt time.Time
}

// ClientConfig configures an ESME.
type ClientConfig struct {
	// Addr is the SMSC's host:port.
	Addr string
	// TLS, when set, wraps the connection in TLS. A ServerName left empty is the host of Addr.
	TLS *tls.Config
	// Bind is the bind request: ID is bind_transceiver when zero, InterfaceVersion 0x34 when zero.
	Bind Bind
	// Session sets the timers and windows of each session.
	Session SessionConfig
	// Handler handles deliver_sm. When nil, each one is acknowledged ESME_ROK and dropped.
	Handler Handler
	// MinBackoff and MaxBackoff bound the wait before connecting again, doubled after each failure.
	// The backoff starts again from MinBackoff after a session that stayed bound for MaxBackoff.
	MinBackoff, MaxBackoff time.Duration
	// OnStateChange, when set, is called with each new state, in order.
	OnStateChange func(ClientState)
	Logger        *slog.Logger
}

// Client is an ESME that stays bound to one SMSC: it connects, binds, and connects and binds again with a backoff
// whenever the session fails.
type Client struct {
	cfg ClientConfig
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	start  sync.Once

	mu sync.Mutex
	// closeCtx is the context of Close, which bounds the unbind of the bound session.
	closeCtx context.Context
	state    ClientState
	session  *Session
	bound    chan struct{}
	notify   sync.Mutex
}

// NewClient checks cfg and returns a client, which connects once started.
func NewClient(cfg ClientConfig) (*Client, error) {
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return nil, fmt.Errorf("smpp: address %q: %w", cfg.Addr, err)
	}

	if cfg.Bind.ID == 0 {
		cfg.Bind.ID = CmdBindTransceiver
	}

	if !cfg.Bind.ID.IsBind() {
		return nil, fmt.Errorf("smpp: %s is not a bind", cfg.Bind.ID)
	}

	if cfg.Bind.InterfaceVersion == 0 {
		cfg.Bind.InterfaceVersion = InterfaceVersion
	}

	// Check the bind request once, rather than at each attempt.
	if _, err := Encode(&cfg.Bind, StatusOK, 1); err != nil {
		return nil, err
	}

	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = DefaultMinBackoff
	}

	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = max(DefaultMaxBackoff, cfg.MinBackoff)
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.Session.Logger == nil {
		cfg.Session.Logger = cfg.Logger
	}

	if cfg.Handler == nil {
		cfg.Handler = HandlerFunc(func(context.Context, *Session, Request) Response { return Response{} })
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		cfg: cfg, log: cfg.Logger.With(slog.String("smsc", cfg.Addr)), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), bound: make(chan struct{}),
		state: ClientState{Status: ConnDisconnected, Since: time.Now()},
	}, nil
}

// Start connects in the background, until Close.
func (c *Client) Start() {
	c.start.Do(func() { go c.run() })
}

// State is the client's current state.
func (c *Client) State() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.state
}

// Session is the bound session, or nil.
func (c *Client) Session() *Session {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.session
}

// WaitBound waits until the client is bound, or ctx ends.
func (c *Client) WaitBound(ctx context.Context) error {
	for {
		c.mu.Lock()
		bound, ch := c.session != nil, c.bound
		c.mu.Unlock()

		if bound {
			return nil
		}

		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return ErrClosed
		}
	}
}

// SubmitSM submits a short message (§4.4) and returns its message_id, or ErrNoSession when the client is not bound,
// or a *StatusError with the command_status of a refusal.
func (c *Client) SubmitSM(ctx context.Context, m *SubmitSM) (string, error) {
	s := c.Session()
	if s == nil {
		return "", ErrNoSession
	}

	return s.SubmitSM(ctx, m)
}

// Close stops connecting, unbinds the bound session (§4.2) until ctx ends, and waits for the client to stop.
func (c *Client) Close(ctx context.Context) error {
	c.start.Do(func() {
		c.setState(func(st *ClientState) { st.Status = ConnClosed })
		close(c.done)
	})

	// run unbinds the bound session, also one that binds while Close runs.
	c.mu.Lock()
	c.closeCtx = ctx
	c.mu.Unlock()

	c.cancel()

	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) setState(f func(*ClientState)) {
	c.notify.Lock()
	defer c.notify.Unlock()

	c.mu.Lock()
	prev := c.state
	f(&c.state)

	if c.state.Status != prev.Status {
		c.state.Since = time.Now()
	}

	st := c.state
	c.mu.Unlock()

	if c.cfg.OnStateChange != nil {
		c.cfg.OnStateChange(st)
	}
}

func (c *Client) fail(err error, next time.Time) {
	c.log.Warn("SMPP session to the SMSC failed", slog.Any("error", err), slog.Time("next_attempt", next))
	c.setState(func(st *ClientState) {
		st.Status, st.BindType, st.SystemID, st.NextAttempt = ConnDisconnected, StateOpen, "", next
		st.LastError, st.LastErrorAt = err, time.Now()
	})
}

func (c *Client) run() {
	defer func() {
		c.setState(func(st *ClientState) {
			st.Status, st.BindType, st.SystemID, st.NextAttempt = ConnClosed, StateOpen, "", time.Time{}
		})
		close(c.done)
	}()

	for failures := 0; ; {
		if c.ctx.Err() != nil {
			return
		}

		c.setState(func(st *ClientState) { st.Status, st.NextAttempt = ConnConnecting, time.Time{} })

		s, err := c.connect()
		if err == nil {
			c.mu.Lock()
			c.session = s
			close(c.bound)
			c.bound = make(chan struct{})
			c.mu.Unlock()

			c.log.Info("bound to the SMSC", slog.String("system_id", s.SystemID()), slog.String("state", s.State().String()))
			c.setState(func(st *ClientState) {
				st.Status, st.BindType, st.SystemID = ConnBound, s.State(), s.SystemID()
			})

			select {
			case <-s.Done():
				err = s.Err()
			case <-c.ctx.Done():
				c.mu.Lock()
				ctx := c.closeCtx
				c.mu.Unlock()

				_ = s.Unbind(ctx)
				_ = s.Close()
			}

			c.mu.Lock()
			c.session = nil
			c.mu.Unlock()

			if c.ctx.Err() != nil {
				return
			}

			// A session that binds and soon fails counts as a failure, so that the backoff grows.
			if time.Since(s.Info().BoundAt) >= c.cfg.MaxBackoff {
				failures = 0
			}
		}

		failures++
		wait := c.backoff(failures)
		c.fail(err, time.Now().Add(wait))

		t := time.NewTimer(wait)
		select {
		case <-c.ctx.Done():
			t.Stop()

			return
		case <-t.C:
		}
	}
}

// backoff is the wait after n failures in a row: MinBackoff doubled n-1 times, at most MaxBackoff, less up to a
// fifth at random so that clients do not reconnect in step.
func (c *Client) backoff(n int) time.Duration {
	d := c.cfg.MinBackoff
	for i := 1; i < n && d < c.cfg.MaxBackoff; i++ {
		d *= 2
	}

	d = min(d, c.cfg.MaxBackoff)

	return d - time.Duration(rand.Int64N(int64(d)/5+1))
}

// connect dials the SMSC and binds.
func (c *Client) connect() (*Session, error) {
	timeout := c.cfg.Session.withDefaults().ResponseTimeout
	if timeout == 0 {
		timeout = DefaultResponseTimeout
	}

	ctx, cancel := context.WithTimeout(c.ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return nil, err
	}

	if c.cfg.TLS != nil {
		cfg := c.cfg.TLS.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName, _, _ = net.SplitHostPort(c.cfg.Addr)
		}

		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()

			return nil, fmt.Errorf("smpp: TLS: %w", err)
		}

		conn = tc
	}

	s := newSession(conn, roleESME, c.cfg.Session, c.cfg.Handler, hooks{})
	s.start()

	// A bind under way waits for its response even after Close, so that a session the SMSC has bound is unbound
	// (§4.2) rather than dropped.
	bctx, bcancel := context.WithTimeout(context.WithoutCancel(c.ctx), timeout)
	defer bcancel()

	b := c.cfg.Bind
	if _, err := s.bind(bctx, &b); err != nil {
		_ = s.Close()

		var se *StatusError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("smpp: bind refused: %w", err)
		}

		return nil, err
	}

	return s, nil
}
