package smpp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

// SMPP 3.4 section 5.2.4
const InterfaceVersion = 0x34

const (
	DefaultSessionInitTimeout  = 30 * time.Second
	DefaultEnquireLinkInterval = 30 * time.Second
	DefaultInactivityTimeout   = 90 * time.Second
	DefaultResponseTimeout     = 30 * time.Second
	DefaultWindow              = 10
	unbindLinger               = 2 * time.Second
)

var (
	ErrClosed               = errors.New("smpp: session closed")
	ErrResponseTimeout      = errors.New("smpp: response_timer expired")
	ErrSessionInitTimeout   = errors.New("smpp: session_init_timer expired")
	ErrInactivityTimeout    = errors.New("smpp: inactivity_timer expired")
	ErrEnquireLinkTimeout   = errors.New("smpp: enquire_link unanswered")
	ErrUnbound              = errors.New("smpp: session unbound")
	ErrNoOptionalParameters = errors.New("smpp: the peer does not support optional parameters")
)

type StatusError struct {
	Command CommandID
	Status  CommandStatus
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("smpp: %s %s", e.Command, e.Status)
}

type StateError struct {
	Command CommandID
	State   State
}

func (e *StateError) Error() string {
	return fmt.Sprintf("smpp: %s not allowed in %s", e.Command, e.State)
}

// SMPP 3.4 section 2.2
type State int32

// SMPP 3.4 section 2.2
const (
	StateOpen State = iota
	StateBoundTX
	StateBoundRX
	StateBoundTRX
	StateClosed
)

var stateNames = [...]string{"OPEN", "BOUND_TX", "BOUND_RX", "BOUND_TRX", "CLOSED"}

func (s State) String() string {
	if s >= 0 && int(s) < len(stateNames) {
		return stateNames[s]
	}

	return fmt.Sprintf("state(%d)", int32(s))
}

func (s State) Bound() bool { return s == StateBoundTX || s == StateBoundRX || s == StateBoundTRX }

func (s State) Transmitter() bool { return s == StateBoundTX || s == StateBoundTRX }

func (s State) Receiver() bool { return s == StateBoundRX || s == StateBoundTRX }

// SMPP 3.4 section 2.2
func boundState(id CommandID) State {
	switch id {
	case CmdBindTransmitter:
		return StateBoundTX
	case CmdBindReceiver:
		return StateBoundRX
	case CmdBindTransceiver:
		return StateBoundTRX
	default:
		return StateClosed
	}
}

// SMPP 3.4 section 2.1
type role int

const (
	roleSMSC role = iota + 1
	roleESME
)

func (r role) String() string {
	if r == roleSMSC {
		return "SMSC"
	}

	return "ESME"
}

// SMPP 3.4 section 2.3 table 2-1
func (r role) issues(id CommandID) bool {
	switch id {
	case CmdBindTransmitter, CmdBindReceiver, CmdBindTransceiver, CmdSubmitSM:
		return r == roleESME
	case CmdDeliverSM:
		return r == roleSMSC
	case CmdUnbind, CmdEnquireLink:
		return true
	default:
		return false
	}
}

// SMPP 3.4 section 2.3 table 2-1
func allowedIn(id CommandID, s State) bool {
	switch id {
	case CmdBindTransmitter, CmdBindReceiver, CmdBindTransceiver:
		return s == StateOpen
	case CmdSubmitSM:
		return s.Transmitter()
	case CmdDeliverSM:
		return s.Receiver()
	case CmdUnbind, CmdEnquireLink:
		return s.Bound()
	default:
		return false
	}
}

// SMPP 3.4 section 2.9
// SMPP 3.4 section 7.2
type SessionConfig struct {
	SessionInitTimeout  time.Duration
	EnquireLinkInterval time.Duration
	InactivityTimeout   time.Duration
	ResponseTimeout     time.Duration
	InboundWindow       int
	OutboundWindow      int
	Logger              *slog.Logger
}

func (c SessionConfig) withDefaults() SessionConfig {
	def := func(d *time.Duration, v time.Duration) {
		switch {
		case *d == 0:
			*d = v
		case *d < 0:
			*d = 0
		}
	}

	def(&c.SessionInitTimeout, DefaultSessionInitTimeout)
	def(&c.EnquireLinkInterval, DefaultEnquireLinkInterval)
	def(&c.InactivityTimeout, DefaultInactivityTimeout)
	def(&c.ResponseTimeout, DefaultResponseTimeout)

	if c.InboundWindow <= 0 {
		c.InboundWindow = DefaultWindow
	}

	if c.OutboundWindow <= 0 {
		c.OutboundWindow = DefaultWindow
	}

	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}

	return c
}

type Request struct {
	Header Header
	PDU    PDU
}

type Response struct {
	Status  CommandStatus
	PDU     PDU
	Written func(error)
}

type Handler interface {
	ServeSMPP(ctx context.Context, s *Session, req Request) Response
}

type HandlerFunc func(ctx context.Context, s *Session, req Request) Response

func (f HandlerFunc) ServeSMPP(ctx context.Context, s *Session, req Request) Response {
	return f(ctx, s, req)
}

type bindDecision struct {
	status  CommandStatus
	account string
	data    any
}

type hooks struct {
	systemID    string
	bind        func(ctx context.Context, s *Session, b *Bind) bindDecision
	bound       func(s *Session)
	closed      func(s *Session)
	decodeError func(s *Session, h Header, err error)
}

type result struct {
	h   Header
	p   PDU
	err error
}

type pending struct {
	id CommandID
	ch chan result
}

var sessionIDs atomic.Uint64

// SMPP 3.4 section 2.2
type Session struct {
	id      uint64
	role    role
	conn    net.Conn
	cfg     SessionConfig
	handler Handler
	hooks   hooks
	log     *slog.Logger
	created time.Time

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	kick   chan struct{}

	state     atomic.Int32
	enquiring atomic.Bool
	lastRecv  atomic.Int64
	outbound  chan struct{}
	wmu       sync.Mutex

	mu          sync.Mutex
	closeErr    error
	closed      bool
	pending     map[uint32]*pending
	seq         uint32
	unbinding   bool
	inflight    int
	idle        chan struct{}
	systemID    string
	account     string
	data        any
	boundAt     time.Time
	peerVersion byte
	peerTLV     bool
}

func newSession(conn net.Conn, r role, cfg SessionConfig, h Handler, hk hooks) *Session {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithCancel(context.Background())
	id := sessionIDs.Add(1)

	s := &Session{
		id: id, role: r, conn: conn, cfg: cfg, handler: h, hooks: hk, created: time.Now(), ctx: ctx, cancel: cancel,
		done: make(chan struct{}), kick: make(chan struct{}, 1), outbound: make(chan struct{}, cfg.OutboundWindow),
		pending: map[uint32]*pending{},
		log: cfg.Logger.With(slog.Uint64("session", id), slog.String("remote", conn.RemoteAddr().String()),
			slog.String("role", r.String())),
	}
	s.lastRecv.Store(s.created.UnixNano())

	return s
}

func (s *Session) start() {
	go s.readLoop()
	go s.watch()
}

func (s *Session) ID() uint64 { return s.id }

func (s *Session) State() State { return State(s.state.Load()) }

func (s *Session) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }

func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closeErr
}

func (s *Session) SystemID() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.systemID
}

func (s *Session) Account() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.account
}

func (s *Session) Data() any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.data
}

type SessionInfo struct {
	ID               uint64
	Account          string
	SystemID         string
	State            State
	RemoteAddr       string
	BoundAt          time.Time
	InterfaceVersion byte
}

func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	return SessionInfo{
		ID: s.id, Account: s.account, SystemID: s.systemID, State: s.State(), RemoteAddr: s.conn.RemoteAddr().String(),
		BoundAt: s.boundAt, InterfaceVersion: s.peerVersion,
	}
}

func (s *Session) Close() error {
	s.closeWith(ErrClosed)

	return nil
}

func (s *Session) closeWith(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return
	}

	s.closed, s.closeErr = true, err
	pend := s.pending
	s.pending = nil
	s.mu.Unlock()

	s.state.Store(int32(StateClosed))
	s.cancel()
	_ = s.conn.Close()

	failed := ErrClosed
	if !errors.Is(err, ErrClosed) {
		failed = fmt.Errorf("%w: %w", ErrClosed, err)
	}

	for _, p := range pend {
		p.ch <- result{err: failed}
	}

	close(s.done)

	switch {
	case errors.Is(err, ErrClosed), errors.Is(err, ErrUnbound):
		s.log.Debug("SMPP session closed", slog.String("reason", err.Error()))
	default:
		s.log.Info("SMPP session closed", slog.String("reason", err.Error()))
	}

	if s.hooks.closed != nil {
		s.hooks.closed(s)
	}
}

func (s *Session) write(b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	return s.writeLocked(b)
}

func (s *Session) writeLocked(b []byte) error {
	if t := s.cfg.ResponseTimeout; t > 0 {
		_ = s.conn.SetWriteDeadline(time.Now().Add(t))
	}

	if _, err := s.conn.Write(b); err != nil {
		s.closeWith(fmt.Errorf("smpp: write: %w", err))

		return err
	}

	return nil
}

func (s *Session) emptyResponse(id CommandID) PDU {
	switch id {
	case CmdBindTransmitter, CmdBindReceiver, CmdBindTransceiver:
		return &BindResp{ID: id.Response(), SystemID: s.hooks.systemID}
	case CmdSubmitSM:
		return &SubmitSMResp{}
	case CmdDeliverSM:
		return &DeliverSMResp{}
	case CmdUnbind:
		return &UnbindResp{}
	default:
		return &EnquireLinkResp{}
	}
}

func (s *Session) respond(h Header, p PDU, status CommandStatus) error {
	if p == nil {
		p = s.emptyResponse(h.ID)
	}

	b, err := Encode(p, status, h.Sequence)
	if err == nil && p.CommandID() != h.ID.Response() {
		err = fmt.Errorf("%w: %s in response to %s", ErrInvalidField, p.CommandID(), h.ID)
	}

	if err != nil {
		s.log.Error("failed to encode SMPP response", slog.String("command", h.ID.String()), slog.Any("error", err))

		if b, err = Encode(s.emptyResponse(h.ID), StatusSysErr, h.Sequence); err != nil {
			return err
		}
	}

	return s.write(b)
}

// SMPP 3.4 section 4.3
func (s *Session) nack(sequence uint32, status CommandStatus) {
	if sequence > MaxSequence {
		sequence = 0
	}

	b, err := Encode(&GenericNack{}, status, sequence)
	if err != nil {
		return
	}

	_ = s.write(b)
}

func (s *Session) readLoop() {
	r := bufio.NewReader(s.conn)

	for {
		h, body, err := ReadPDU(r)
		if err != nil {
			var he *HeaderError
			if errors.As(err, &he) {
				s.log.Warn("invalid SMPP command_length", slog.Uint64("command_length", uint64(he.Length)))
				s.nack(h.Sequence, StatusInvCmdLen)
			}

			s.closeWith(fmt.Errorf("smpp: read: %w", err))

			return
		}

		s.lastRecv.Store(time.Now().UnixNano())

		if h.ID.IsResponse() {
			s.handleResponse(h, body)
		} else {
			s.handleRequest(h, body)
		}

		if s.State() == StateClosed {
			return
		}
	}
}

// SMPP 3.4 section 2.5.2
func (s *Session) handleResponse(h Header, body []byte) {
	if !h.ID.Defined() {
		s.log.Debug("undefined SMPP command", slog.String("command", h.ID.String()))
		s.nack(h.Sequence, StatusInvCmdID)

		return
	}

	s.mu.Lock()
	p := s.pending[h.Sequence]

	matched := p != nil && (h.ID == p.id.Response() || h.ID == CmdGenericNack)
	if matched {
		delete(s.pending, h.Sequence)
	}
	s.mu.Unlock()

	if !matched {
		s.log.Debug("discarding SMPP response to no request", slog.String("command", h.ID.String()),
			slog.Uint64("sequence", uint64(h.Sequence)))

		return
	}

	if h.ID == CmdGenericNack {
		p.ch <- result{h: h, err: &StatusError{Command: h.ID, Status: h.Status}}

		return
	}

	resp, err := DecodeBody(h, body)

	switch {
	case err != nil:
		err = fmt.Errorf("smpp: %s: %w", h.ID, err)
	case h.Status != StatusOK:
		err = &StatusError{Command: h.ID, Status: h.Status}
	case p.id.IsBind():
		s.bound(p.id, resp.(*BindResp))
	case p.id == CmdUnbind:
		s.state.Store(int32(StateClosed))
	}

	p.ch <- result{h: h, p: resp, err: err}
}

// SMPP 3.4 section 3.4
func (s *Session) bound(id CommandID, r *BindResp) {
	s.mu.Lock()
	s.systemID, s.boundAt = r.SystemID, time.Now()

	if v := r.Options.SCInterfaceVersion; v != nil {
		s.peerVersion, s.peerTLV = *v, *v >= InterfaceVersion
	}
	s.mu.Unlock()

	if !s.state.CompareAndSwap(int32(StateOpen), int32(boundState(id))) {
		return
	}

	s.poke()

	if s.hooks.bound != nil {
		s.hooks.bound(s)
	}
}

// SMPP 3.4 section 3.3
// SMPP 3.4 section 4.3
// SMPP 3.4 section 2.8
func (s *Session) handleRequest(h Header, body []byte) {
	if h.Sequence == 0 || h.Sequence > MaxSequence {
		s.nack(0, StatusUnknownErr)

		return
	}

	peer := roleESME
	if s.role == roleESME {
		peer = roleSMSC
	}

	if h.ID == CmdAlertNotification {
		s.log.Debug("discarding SMPP alert_notification")

		return
	}

	if !h.ID.Supported() || !peer.issues(h.ID) {
		s.log.Debug("unsupported SMPP command", slog.String("command", h.ID.String()))
		s.nack(h.Sequence, StatusInvCmdID)

		return
	}

	if st := s.State(); !allowedIn(h.ID, st) {
		status := StatusInvBndSts
		if h.ID.IsBind() {
			status = StatusAlyBnd
		}

		_ = s.respond(h, nil, status)

		return
	}

	p, err := DecodeBody(h, body)
	if err != nil {
		var be *BodyError
		if !errors.As(err, &be) {
			be = &BodyError{Status: StatusSysErr}
		}

		s.log.Debug("invalid SMPP PDU", slog.String("command", h.ID.String()), slog.Any("error", err))

		if be.Status == StatusInvCmdLen {
			s.nack(h.Sequence, be.Status)
		} else {
			_ = s.respond(h, nil, be.Status)
		}

		if s.hooks.decodeError != nil {
			s.hooks.decodeError(s, h, err)
		}

		return
	}

	switch h.ID {
	case CmdEnquireLink:
		_ = s.respond(h, &EnquireLinkResp{}, StatusOK)
	case CmdUnbind:
		s.peerUnbind(h)
	case CmdBindTransmitter, CmdBindReceiver, CmdBindTransceiver:
		s.handleBind(h, p.(*Bind))
	default:
		s.serve(h, p)
	}
}

// SMPP 3.4 section 4.1
// SMPP 3.4 section 3.4
// SMPP 3.4 section 5.3.2.25
func (s *Session) handleBind(h Header, b *Bind) {
	d := bindDecision{status: StatusBindFail}
	if s.hooks.bind != nil {
		d = s.hooks.bind(s.ctx, s, b)
	}

	resp := &BindResp{ID: h.ID.Response(), SystemID: s.hooks.systemID}
	if d.status == StatusOK && b.InterfaceVersion >= InterfaceVersion {
		resp.Options.SCInterfaceVersion = new(byte(InterfaceVersion))
	}

	if d.status != StatusOK {
		s.log.Info("SMPP bind refused", slog.String("system_id", b.SystemID), slog.String("status", d.status.String()))
		_ = s.respond(h, resp, d.status)

		return
	}

	s.mu.Lock()
	s.systemID, s.account, s.data, s.peerVersion = b.SystemID, d.account, d.data, b.InterfaceVersion
	s.peerTLV = b.InterfaceVersion >= InterfaceVersion
	s.mu.Unlock()

	if err := s.respond(h, resp, StatusOK); err != nil {
		return
	}

	s.mu.Lock()
	s.boundAt = time.Now()
	s.mu.Unlock()

	if !s.state.CompareAndSwap(int32(StateOpen), int32(boundState(h.ID))) {
		return
	}

	s.poke()
	s.log.Info("SMPP session bound", slog.String("system_id", b.SystemID), slog.String("state", s.State().String()))

	if s.hooks.bound != nil {
		s.hooks.bound(s)
	}
}

func (s *Session) serve(h Header, p PDU) {
	s.mu.Lock()
	switch {
	case s.unbinding:
		s.mu.Unlock()

		_ = s.respond(h, nil, StatusInvBndSts)

		return
	case s.inflight >= s.cfg.InboundWindow:
		s.mu.Unlock()

		_ = s.respond(h, nil, StatusThrottled)

		return
	}

	s.inflight++
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.inflight--

			if s.inflight == 0 && s.idle != nil {
				close(s.idle)
				s.idle = nil
			}
			s.mu.Unlock()
		}()

		r := s.call(h, p)
		err := s.respond(h, r.PDU, r.Status)

		if r.Written != nil {
			r.Written(err)
		}
	}()
}

func (s *Session) call(h Header, p PDU) (r Response) {
	if s.handler == nil {
		return Response{Status: StatusSysErr}
	}

	defer func() {
		if v := recover(); v != nil {
			s.log.Error("SMPP handler panicked", slog.Any("panic", v), slog.String("command", h.ID.String()),
				slog.String("stack", string(debug.Stack())))

			r = Response{Status: StatusSysErr}
		}
	}()

	return s.handler.ServeSMPP(s.ctx, s, Request{Header: h, PDU: p})
}

func (s *Session) waitHandlers(ctx context.Context) {
	s.mu.Lock()
	if s.inflight == 0 {
		s.mu.Unlock()

		return
	}

	if s.idle == nil {
		s.idle = make(chan struct{})
	}

	idle := s.idle
	s.mu.Unlock()

	select {
	case <-idle:
	case <-ctx.Done():
	}
}

func (s *Session) peerUnbind(h Header) {
	s.startUnbind()

	ctx, cancel := context.WithTimeout(s.ctx, s.responseTimeout())
	s.waitHandlers(ctx)
	cancel()

	if err := s.respond(h, &UnbindResp{}, StatusOK); err != nil {
		return
	}

	s.state.Store(int32(StateClosed))
	_ = s.conn.SetReadDeadline(time.Now().Add(unbindLinger))

	var b [256]byte
	for {
		if _, err := s.conn.Read(b[:]); err != nil {
			break
		}
	}

	s.closeWith(ErrUnbound)
}

func (s *Session) responseTimeout() time.Duration {
	if s.cfg.ResponseTimeout > 0 {
		return s.cfg.ResponseTimeout
	}

	return DefaultResponseTimeout
}

func (s *Session) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// SMPP 3.4 section 7.2
func (s *Session) watch() {
	t := time.NewTimer(time.Hour)
	defer t.Stop()

	for {
		wait, ok := s.check(time.Now())
		if !ok {
			return
		}

		t.Reset(wait)

		select {
		case <-s.done:
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

func (s *Session) check(now time.Time) (time.Duration, bool) {
	next := time.Duration(math.MaxInt64)
	st := s.State()

	switch st {
	case StateClosed:
		return 0, false
	case StateOpen:
		if t := s.cfg.SessionInitTimeout; t > 0 && s.role == roleSMSC {
			d := s.created.Add(t).Sub(now)
			if d <= 0 {
				s.closeWith(ErrSessionInitTimeout)

				return 0, false
			}

			next = d
		}
	default:
		idle := now.Sub(time.Unix(0, s.lastRecv.Load()))

		if t := s.cfg.InactivityTimeout; t > 0 {
			if idle >= t {
				s.closeWith(ErrInactivityTimeout)

				return 0, false
			}

			next = min(next, t-idle)
		}

		if t := s.cfg.EnquireLinkInterval; t > 0 {
			d := t - idle
			if d <= 0 {
				if !s.isUnbinding() && s.enquiring.CompareAndSwap(false, true) {
					go s.keepAlive()
				}

				d = t
			}

			next = min(next, d)
		}
	}

	if next == time.Duration(math.MaxInt64) {
		next = time.Hour
	}

	return next, true
}

func (s *Session) keepAlive() {
	defer s.enquiring.Store(false)

	_, _, err := s.request(s.ctx, &EnquireLink{}, false)
	if errors.Is(err, ErrResponseTimeout) {
		s.closeWith(ErrEnquireLinkTimeout)
	}
}

// SMPP 3.4 section 5.1.4
func (s *Session) register(id CommandID) (uint32, *pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.refuse(id); err != nil {
		return 0, nil, err
	}

	for {
		s.seq++
		if s.seq > MaxSequence {
			s.seq = 1
		}

		if _, busy := s.pending[s.seq]; !busy {
			break
		}
	}

	p := &pending{id: id, ch: make(chan result, 1)}
	s.pending[s.seq] = p

	return s.seq, p, nil
}

func (s *Session) send(id CommandID, p PDU) (uint32, *pending, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	seq, pd, err := s.register(id)
	if err != nil {
		return 0, nil, err
	}

	b, err := Encode(p, StatusOK, seq)
	if err != nil {
		s.unregister(seq)

		return 0, nil, err
	}

	if err := s.writeLocked(b); err != nil {
		s.unregister(seq)

		return 0, nil, fmt.Errorf("%w: %w", ErrClosed, err)
	}

	return seq, pd, nil
}

func (s *Session) unregister(seq uint32) {
	s.mu.Lock()
	delete(s.pending, seq)
	s.mu.Unlock()
}

// SMPP 3.4 section 4.2
func (s *Session) refuse(id CommandID) error {
	st := s.State()

	switch {
	case s.closed || st == StateClosed:
		return s.closedError()
	case !s.role.issues(id) || !allowedIn(id, st) || s.unbinding && id != CmdUnbind:
		return &StateError{Command: id, State: st}
	}

	return nil
}

func (s *Session) startUnbind() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	first := !s.unbinding
	s.unbinding = true

	return first
}

func (s *Session) isUnbinding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.unbinding
}

func (s *Session) closedError() error {
	if s.closeErr == nil || errors.Is(s.closeErr, ErrClosed) {
		return ErrClosed
	}

	return fmt.Errorf("%w: %w", ErrClosed, s.closeErr)
}

func hasOptions(p PDU) bool {
	var o *Options

	switch p := p.(type) {
	case *SubmitSM:
		o = &p.Options
	case *DeliverSM:
		o = &p.Options
	default:
		return false
	}

	return o.DestAddrSubunit != nil || o.ReceiptedMessageID != nil || o.UserMessageReference != nil ||
		o.SourcePort != nil || o.DestinationPort != nil || o.SarMsgRefNum != nil || o.SarTotalSegments != nil ||
		o.SarSegmentSeqnum != nil || o.SCInterfaceVersion != nil || o.NetworkErrorCode != nil ||
		o.MessagePayload != nil || o.MessageState != nil || len(o.Other) > 0
}

func (s *Session) Send(ctx context.Context, p PDU) (Header, PDU, error) {
	if id := p.CommandID(); id.IsBind() || id == CmdUnbind {
		return Header{}, nil, &StateError{Command: id, State: s.State()}
	}

	return s.request(ctx, p, true)
}

func (s *Session) request(ctx context.Context, p PDU, windowed bool) (Header, PDU, error) {
	id := p.CommandID()

	s.mu.Lock()
	err := s.refuse(id)
	tlv := s.peerTLV
	s.mu.Unlock()

	if err != nil {
		return Header{}, nil, err
	}

	if !tlv && hasOptions(p) {
		return Header{}, nil, ErrNoOptionalParameters
	}

	if windowed {
		select {
		case s.outbound <- struct{}{}:
			defer func() { <-s.outbound }()
		case <-ctx.Done():
			return Header{}, nil, ctx.Err()
		case <-s.done:
			s.mu.Lock()
			defer s.mu.Unlock()

			return Header{}, nil, s.closedError()
		}
	}

	seq, pd, err := s.send(id, p)
	if err != nil {
		return Header{}, nil, err
	}

	var timeout <-chan time.Time

	if t := s.cfg.ResponseTimeout; t > 0 {
		timer := time.NewTimer(t)
		defer timer.Stop()

		timeout = timer.C
	}

	select {
	case r := <-pd.ch:
		return r.h, r.p, r.err
	case <-ctx.Done():
		s.unregister(seq)

		return Header{}, nil, ctx.Err()
	case <-timeout:
		s.unregister(seq)

		return Header{}, nil, fmt.Errorf("%w: %s %d", ErrResponseTimeout, id, seq)
	}
}

// SMPP 3.4 section 4.4
func (s *Session) SubmitSM(ctx context.Context, m *SubmitSM) (string, error) {
	_, p, err := s.Send(ctx, m)
	if err != nil {
		return "", err
	}

	return p.(*SubmitSMResp).MessageID, nil
}

// SMPP 3.4 section 4.6
func (s *Session) DeliverSM(ctx context.Context, m *DeliverSM) error {
	_, _, err := s.Send(ctx, m)

	return err
}

// SMPP 3.4 section 4.11
func (s *Session) EnquireLink(ctx context.Context) error {
	_, _, err := s.request(ctx, &EnquireLink{}, false)

	return err
}

// SMPP 3.4 section 4.1
func (s *Session) bind(ctx context.Context, b *Bind) (*BindResp, error) {
	_, p, err := s.request(ctx, b, false)
	if err != nil {
		return nil, err
	}

	return p.(*BindResp), nil
}

// SMPP 3.4 section 4.2
func (s *Session) Unbind(ctx context.Context) error {
	if st := s.State(); !st.Bound() {
		if st == StateClosed {
			return s.Err()
		}

		return &StateError{Command: CmdUnbind, State: st}
	}

	if !s.startUnbind() {
		select {
		case <-s.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	s.waitHandlers(ctx)

	_, _, err := s.request(ctx, &Unbind{}, false)
	s.closeWith(ErrUnbound)

	if errors.Is(err, ErrClosed) && errors.Is(s.Err(), ErrUnbound) {
		return nil
	}

	return err
}
