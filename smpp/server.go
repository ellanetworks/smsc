package smpp

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"
)

var (
	ErrServerClosed = errors.New("smpp: server closed")
	ErrNoSession    = errors.New("smpp: no session bound")
)

type BindRequest struct {
	Bind       *Bind
	Session    *Session
	RemoteAddr net.Addr
	TLS        bool
}

type BindOutcome struct {
	Status      CommandStatus
	Account     string
	Data        any
	MaxSessions int
}

type ServerConfig struct {
	SystemID          string
	TLS               *tls.Config
	Session           SessionConfig
	Authenticate      func(ctx context.Context, req BindRequest) BindOutcome
	Handler           Handler
	OnBound, OnClosed func(*Session)
	OnDecodeError     func(s *Session, h Header, err error)
	OnRefused         func(s *Session, h Header, status CommandStatus)
	Logger            *slog.Logger
}

type Server struct {
	cfg    ServerConfig
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	closing   bool
	listeners map[net.Listener]struct{}
	sessions  map[uint64]*Session
	accounts  map[string]map[uint64]*Session
	reserved  map[uint64]string
	next      uint64
	wg        sync.WaitGroup
}

func NewServer(cfg ServerConfig) (*Server, error) {
	switch {
	case cfg.SystemID == "" || len(cfg.SystemID) >= maxSystemID:
		return nil, fmt.Errorf("smpp: system_id %q must be 1 to %d characters", cfg.SystemID, maxSystemID-1)
	case cfg.Authenticate == nil:
		return nil, errors.New("smpp: Authenticate is required")
	case cfg.Handler == nil:
		return nil, errors.New("smpp: Handler is required")
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.Session.Logger == nil {
		cfg.Session.Logger = cfg.Logger
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Server{
		cfg: cfg, log: cfg.Logger, ctx: ctx, cancel: cancel, listeners: map[net.Listener]struct{}{}, sessions: map[uint64]*Session{},
		accounts: map[string]map[uint64]*Session{}, reserved: map[uint64]string{},
	}, nil
}

func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}

	return s.Serve(l)
}

func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()

		_ = l.Close()

		return ErrServerClosed
	}

	s.listeners[l] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.listeners, l)
		s.mu.Unlock()

		_ = l.Close()
	}()

	backoff := 5 * time.Millisecond

	for {
		c, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()

			if closing {
				return ErrServerClosed
			}

			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(backoff)
				backoff = min(2*backoff, time.Second)

				continue
			}

			return err
		}

		backoff = 5 * time.Millisecond

		s.wg.Add(1)

		go func() {
			defer s.wg.Done()

			s.serveConn(c)
		}()
	}
}

func (s *Server) serveConn(c net.Conn) {
	isTLS := false

	if s.cfg.TLS != nil {
		tc := tls.Server(c, s.cfg.TLS)
		timeout := s.cfg.Session.withDefaults().SessionInitTimeout

		if timeout == 0 {
			timeout = DefaultSessionInitTimeout
		}

		ctx, cancel := context.WithTimeout(s.ctx, timeout)
		err := tc.HandshakeContext(ctx)

		cancel()

		if err != nil {
			if s.ctx.Err() != nil {
				_ = c.Close()

				return
			}

			s.log.Info("SMPP TLS handshake failed", slog.String("remote", c.RemoteAddr().String()), slog.Any("error", err))

			_ = c.Close()

			return
		}

		c, isTLS = tc, true
	}

	sess := newSession(c, roleSMSC, s.cfg.Session, s.cfg.Handler, hooks{
		systemID: s.cfg.SystemID,
		bind: func(ctx context.Context, sess *Session, b *Bind) bindDecision {
			return s.bind(ctx, sess, b, isTLS)
		},
		bound:       s.cfg.OnBound,
		closed:      s.closed,
		decodeError: s.cfg.OnDecodeError,
		refused:     s.cfg.OnRefused,
	})

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()

		_ = c.Close()

		return
	}

	s.sessions[sess.id] = sess
	s.mu.Unlock()

	sess.start()
	<-sess.Done()
}

func (s *Server) bind(ctx context.Context, sess *Session, b *Bind, isTLS bool) bindDecision {
	out := s.cfg.Authenticate(ctx, BindRequest{Bind: b, Session: sess, RemoteAddr: sess.RemoteAddr(), TLS: isTLS})
	if out.Status != StatusOK {
		return bindDecision{status: out.Status}
	}

	account := out.Account
	if account == "" {
		account = b.SystemID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closing {
		return bindDecision{status: StatusSysErr}
	}

	select {
	case <-sess.Done():
		return bindDecision{status: StatusBindFail}
	default:
	}

	if out.MaxSessions > 0 && len(s.accounts[account]) >= out.MaxSessions {
		s.log.InfoContext(ctx, "SMPP bind refused: too many sessions", slog.String("account", account),
			slog.Int("max_sessions", out.MaxSessions))

		return bindDecision{status: StatusBindFail}
	}

	if s.accounts[account] == nil {
		s.accounts[account] = map[uint64]*Session{}
	}

	s.accounts[account][sess.id] = sess
	s.reserved[sess.id] = account

	return bindDecision{status: StatusOK, account: account, data: out.Data}
}

func (s *Server) closed(sess *Session) {
	s.mu.Lock()
	delete(s.sessions, sess.id)

	if acc, ok := s.reserved[sess.id]; ok {
		delete(s.reserved, sess.id)
		delete(s.accounts[acc], sess.id)

		if len(s.accounts[acc]) == 0 {
			delete(s.accounts, acc)
		}
	}
	s.mu.Unlock()

	if s.cfg.OnClosed != nil {
		s.cfg.OnClosed(sess)
	}
}

func (s *Server) bound(account string) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*Session

	add := func(m map[uint64]*Session) {
		for _, sess := range m {
			if sess.State().Bound() && !sess.isUnbinding() {
				out = append(out, sess)
			}
		}
	}

	if account == "" {
		for _, m := range s.accounts {
			add(m)
		}
	} else {
		add(s.accounts[account])
	}

	slices.SortFunc(out, func(a, b *Session) int { return cmp.Compare(a.id, b.id) })

	return out
}

func (s *Server) Sessions(account string) []SessionInfo {
	sessions := s.bound(account)
	out := make([]SessionInfo, len(sessions))

	for i, sess := range sessions {
		out[i] = sess.Info()
	}

	return out
}

func (s *Server) CloseAccount(account string) int {
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.accounts[account]))

	for _, sess := range s.accounts[account] {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()

	for _, sess := range sessions {
		_ = sess.Close()
	}

	return len(sessions)
}

func (s *Server) Deliver(ctx context.Context, account string, m *DeliverSM) (SessionInfo, error) {
	var receivers []*Session

	for _, sess := range s.bound(account) {
		if sess.State().Receiver() {
			receivers = append(receivers, sess)
		}
	}

	if len(receivers) == 0 {
		return SessionInfo{}, ErrNoSession
	}

	s.mu.Lock()
	sess := receivers[s.next%uint64(len(receivers))]
	s.next++
	s.mu.Unlock()

	return sess.Info(), sess.DeliverSM(ctx, m)
}

// SMPP 3.4 section 4.2
func (s *Server) Shutdown(ctx context.Context) error {
	sessions := s.stop()

	var wg sync.WaitGroup

	for _, sess := range sessions {
		wg.Go(func() {
			if sess.State().Bound() {
				_ = sess.Unbind(ctx)
			}

			_ = sess.Close()
		})
	}

	done := make(chan struct{})

	go func() {
		wg.Wait()
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		for _, sess := range sessions {
			_ = sess.Close()
		}

		return ctx.Err()
	}
}

func (s *Server) Close() error {
	for _, sess := range s.stop() {
		_ = sess.Close()
	}

	s.wg.Wait()

	return nil
}

func (s *Server) stop() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closing = true
	s.cancel()

	for l := range s.listeners {
		_ = l.Close()
	}

	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}

	return sessions
}
