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

const (
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = time.Minute
)

type ConnStatus string

const (
	ConnConnecting   ConnStatus = "connecting"
	ConnBound        ConnStatus = "bound"
	ConnDisconnected ConnStatus = "disconnected"
	ConnClosed       ConnStatus = "closed"
)

type ClientState struct {
	Status      ConnStatus
	Since       time.Time
	BindType    State
	SystemID    string
	LastError   error
	LastErrorAt time.Time
	NextAttempt time.Time
}

type ClientConfig struct {
	Addr                   string
	TLS                    *tls.Config
	Bind                   Bind
	Session                SessionConfig
	Handler                Handler
	MinBackoff, MaxBackoff time.Duration
	OnStateChange          func(ClientState)
	Logger                 *slog.Logger
}

type Client struct {
	cfg ClientConfig
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	start  sync.Once

	mu       sync.Mutex
	closeCtx context.Context
	state    ClientState
	session  *Session
	bound    chan struct{}
	notify   sync.Mutex
}

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

func (c *Client) Start() {
	c.start.Do(func() { go c.run() })
}

func (c *Client) State() ClientState {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.state
}

func (c *Client) Session() *Session {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.session
}

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

// SMPP 3.4 section 4.4
func (c *Client) SubmitSM(ctx context.Context, m *SubmitSM) (string, error) {
	s := c.Session()
	if s == nil {
		return "", ErrNoSession
	}

	return s.SubmitSM(ctx, m)
}

// SMPP 3.4 section 4.2
func (c *Client) Close(ctx context.Context) error {
	c.start.Do(func() {
		c.setState(func(st *ClientState) { st.Status = ConnClosed })
		close(c.done)
	})

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

func (c *Client) backoff(n int) time.Duration {
	d := c.cfg.MinBackoff
	for i := 1; i < n && d < c.cfg.MaxBackoff; i++ {
		d *= 2
	}

	d = min(d, c.cfg.MaxBackoff)

	return d - time.Duration(rand.Int64N(int64(d)/5+1))
}

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
