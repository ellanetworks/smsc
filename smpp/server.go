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
	// ErrServerClosed is returned by Serve after Shutdown or Close.
	ErrServerClosed = errors.New("smpp: server closed")
	// ErrNoSession is a deliver_sm to an account without a session bound as a receiver or transceiver, or a
	// submit_sm on a client that is not bound.
	ErrNoSession = errors.New("smpp: no session bound")
)

// BindRequest is a bind request that an SMSC authenticates.
type BindRequest struct {
	Bind *Bind
	// Session is the session being bound, still OPEN: nothing may be sent on it before the bind response.
	Session    *Session
	RemoteAddr net.Addr
	// TLS reports whether the connection is over TLS.
	TLS bool
}

// BindOutcome is the answer to a BindRequest.
type BindOutcome struct {
	// Status is ESME_ROK to accept the bind, or the command_status of the bind response, such as ESME_RBINDFAIL,
	// ESME_RINVPASWD or ESME_RINVSYSID (Table 5-2).
	Status CommandStatus
	// Account is what the server's registry files the session under; empty is the system_id.
	Account string
	// Data is attached to the session, for the Handler (Session.Data).
	Data any
	// MaxSessions, when positive, is the most sessions bound for Account at once. One more is refused
	// ESME_RBINDFAIL.
	MaxSessions int
}

// ServerConfig configures an SMSC.
type ServerConfig struct {
	// SystemID identifies the SMSC in bind responses (§5.2.1), at most 15 characters.
	SystemID string
	// TLS, when set, wraps every connection in TLS from its first octet. SMPP 3.4 runs over TCP/IP or X.25 (§2.4);
	// TLS is a convention outside it.
	TLS *tls.Config
	// Session sets the timers and windows of every session.
	Session SessionConfig
	// Authenticate decides each bind request. It is required. It runs on the session's reader, so the ESME's
	// next PDU waits for it.
	Authenticate func(ctx context.Context, req BindRequest) BindOutcome
	// Handler handles submit_sm. It is required.
	Handler Handler
	// OnBound is called when a session is bound, after its bind response is written; OnClosed when a session
	// closes, bound or not.
	OnBound, OnClosed func(*Session)
	// OnDecodeError is called for a request whose body does not decode, after its error response.
	OnDecodeError func(s *Session, h Header, err error)
	Logger        *slog.Logger
}

// Server is an SMSC: it accepts ESME connections, binds them through Authenticate, and keeps a registry of the
// bound sessions by account.
type Server struct {
	cfg ServerConfig
	log *slog.Logger

	mu        sync.Mutex
	closing   bool
	listeners map[net.Listener]struct{}
	sessions  map[uint64]*Session
	accounts  map[string]map[uint64]*Session
	// reserved is the account that bind filed each session under, for closed to remove it.
	reserved map[uint64]string
	next     uint64
	wg       sync.WaitGroup
}

// NewServer checks cfg and returns a server.
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

	return &Server{
		cfg: cfg, log: cfg.Logger, listeners: map[net.Listener]struct{}{}, sessions: map[uint64]*Session{},
		accounts: map[string]map[uint64]*Session{}, reserved: map[uint64]string{},
	}, nil
}

// ListenAndServe listens on the TCP address addr and serves it.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}

	return s.Serve(l)
}

// Serve accepts connections on l until Shutdown or Close, and returns ErrServerClosed then. It closes l.
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

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := tc.HandshakeContext(ctx)

		cancel()

		if err != nil {
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

// bind authenticates a bind request and reserves the session in its account. The reservation counts toward
// MaxSessions at once, so that two binds at the same time cannot both pass.
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
		// The session closed during Authenticate, and closed has run or waits for s.mu: reserve nothing.
		return bindDecision{status: StatusBindFail}
	default:
	}

	if out.MaxSessions > 0 && len(s.accounts[account]) >= out.MaxSessions {
		s.log.Info("SMPP bind refused: too many sessions", slog.String("account", account),
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

// bound lists the bound sessions of an account, or of all accounts when account is empty, by ID. A session that is
// unbinding takes no new request, so it is left out.
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

// Sessions describes the bound sessions of an account, or of all accounts when account is empty, oldest first.
func (s *Server) Sessions(account string) []SessionInfo {
	sessions := s.bound(account)
	out := make([]SessionInfo, len(sessions))

	for i, sess := range sessions {
		out[i] = sess.Info()
	}

	return out
}

// CloseAccount closes every session of an account at once, bound or binding, for example after its credentials
// change, and returns how many it closed.
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

// Deliver sends a deliver_sm to one of an account's sessions bound as a receiver or transceiver, in turn, and
// returns that session and the outcome: nil for deliver_sm_resp ESME_ROK, a *StatusError for another status or a
// generic_nack, ErrResponseTimeout, ErrClosed, or ErrNoSession when none is bound. SMPP 3.4 does not say which
// session a deliver_sm goes to; routing by account is the server's convention.
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

// Shutdown stops accepting connections, unbinds every bound session (§4.2) and closes the others, and waits for
// them to close until ctx ends, when it closes the rest.
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

// Close stops accepting connections and closes every session at once.
func (s *Server) Close() error {
	for _, sess := range s.stop() {
		_ = sess.Close()
	}

	s.wg.Wait()

	return nil
}

// stop marks the server closing, closes its listeners and returns its sessions.
func (s *Server) stop() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closing = true

	for l := range s.listeners {
		_ = l.Close()
	}

	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}

	return sessions
}
