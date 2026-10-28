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

// InterfaceVersion is the SMPP version of this package, 3.4 (§5.2.4).
const InterfaceVersion = 0x34

// The default timers and windows of SessionConfig. §7.2 leaves the timer values out of scope ("Definition of the
// various timer values is outside the scope"); §2.5.2 and §2.6.2 recommend at most 10 outstanding operations.
const (
	DefaultSessionInitTimeout  = 30 * time.Second
	DefaultEnquireLinkInterval = 30 * time.Second
	DefaultInactivityTimeout   = 90 * time.Second
	DefaultResponseTimeout     = 30 * time.Second
	DefaultWindow              = 10
	// unbindLinger is how long the receiver of an unbind waits, after its unbind_resp, for the peer to close the
	// connection (§2.2, CLOSED: the ESME "has closed the network connection").
	unbindLinger = 2 * time.Second
)

var (
	// ErrClosed is a request on a session that is closed, or that closed before its response arrived. The session's
	// Err tells why it closed.
	ErrClosed = errors.New("smpp: session closed")
	// ErrResponseTimeout is a request whose response did not arrive within the response_timer (§7.2). The request
	// may or may not have been processed (§2.8).
	ErrResponseTimeout = errors.New("smpp: response_timer expired")
	// ErrSessionInitTimeout closes a connection that did not bind within the session_init_timer (§7.2).
	ErrSessionInitTimeout = errors.New("smpp: session_init_timer expired")
	// ErrInactivityTimeout closes a session that received no PDU within the inactivity_timer (§7.2).
	ErrInactivityTimeout = errors.New("smpp: inactivity_timer expired")
	// ErrEnquireLinkTimeout closes a session whose peer did not answer an enquire_link within the response_timer.
	ErrEnquireLinkTimeout = errors.New("smpp: enquire_link unanswered")
	// ErrUnbound closes a session after an unbind and its unbind_resp (§4.2).
	ErrUnbound = errors.New("smpp: session unbound")
	// ErrNoOptionalParameters is a PDU with optional parameters toward a peer that does not support them: an ESME
	// whose interface_version is below 0x34, or an SMSC whose bind response has no sc_interface_version (§3.4).
	ErrNoOptionalParameters = errors.New("smpp: the peer does not support optional parameters")
)

// StatusError is a response with a command_status other than ESME_ROK, or a generic_nack, to a request.
type StatusError struct {
	// Command is the command_id of the response: the request's response, or generic_nack.
	Command CommandID
	Status  CommandStatus
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("smpp: %s %s", e.Command, e.Status)
}

// StateError is a request that the session's state, or the role of its sender, does not allow (§2.3, Table 2-1).
type StateError struct {
	Command CommandID
	State   State
}

func (e *StateError) Error() string {
	return fmt.Sprintf("smpp: %s not allowed in %s", e.Command, e.State)
}

// State is the state of a session (§2.2).
type State int32

// The session states of §2.2.
const (
	StateOpen State = iota
	StateBoundTX
	StateBoundRX
	StateBoundTRX
	StateClosed
)

var stateNames = [...]string{"OPEN", "BOUND_TX", "BOUND_RX", "BOUND_TRX", "CLOSED"}

// String returns the name of the state in §2.2.
func (s State) String() string {
	if s >= 0 && int(s) < len(stateNames) {
		return stateNames[s]
	}

	return fmt.Sprintf("state(%d)", int32(s))
}

// Bound reports whether s is BOUND_TX, BOUND_RX or BOUND_TRX.
func (s State) Bound() bool { return s == StateBoundTX || s == StateBoundRX || s == StateBoundTRX }

// Transmitter reports whether the ESME may submit in s: BOUND_TX or BOUND_TRX (Table 2-1, submit_sm).
func (s State) Transmitter() bool { return s == StateBoundTX || s == StateBoundTRX }

// Receiver reports whether the SMSC may deliver in s: BOUND_RX or BOUND_TRX (Table 2-1, deliver_sm).
func (s State) Receiver() bool { return s == StateBoundRX || s == StateBoundTRX }

// boundState is the state that a successful bind request id leads to (§2.2).
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

// role is the side of a session (§2.1).
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

// issues reports whether the role r issues the request id, from the "Issued by" columns of Table 2-1, for the
// requests that this package supports.
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

// allowedIn reports whether the request id may be sent in state s (Table 2-1, "Required SMPP Session State").
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

// SessionConfig sets the timers of §2.9 and §7.2 ("All timers should be configurable") and the windows of a session.
// A zero duration or window takes its default; a negative duration disables the timer.
type SessionConfig struct {
	// SessionInitTimeout is the session_init_timer: an SMSC closes a connection that has not bound within it.
	SessionInitTimeout time.Duration
	// EnquireLinkInterval is the enquire_link_timer: a bound session that received no PDU within it sends
	// enquire_link, and closes when the response_timer of that enquire_link expires.
	EnquireLinkInterval time.Duration
	// InactivityTimeout is the inactivity_timer: a bound session that received no PDU within it closes.
	InactivityTimeout time.Duration
	// ResponseTimeout is the response_timer: the time allowed for the response to a request, and for a write.
	ResponseTimeout time.Duration
	// InboundWindow is the most requests from the peer handled at once. One more is answered ESME_RTHROTTLED.
	InboundWindow int
	// OutboundWindow is the most requests sent and not yet answered. A request beyond it waits for a response.
	// enquire_link and unbind, which the session sends itself, are outside the window.
	OutboundWindow int
	Logger         *slog.Logger
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

// Request is a request from the peer: submit_sm to an SMSC, deliver_sm to an ESME.
type Request struct {
	Header Header
	PDU    PDU
}

// Response is a Handler's answer to a Request. PDU is the response body, of the request's response type; nil is an
// empty one. When Status is not ESME_ROK the body is left out where §4 says so (see Encode). Written, if set, is called
// once the response is written, or failed to be, so that the caller can act only after the peer has it.
type Response struct {
	Status  CommandStatus
	PDU     PDU
	Written func(error)
}

// Handler handles the requests that a session does not handle itself. The session answers bind, unbind,
// enquire_link, generic_nack, unsupported commands and requests that the state does not allow; Handler gets the
// rest: submit_sm on an SMSC, deliver_sm on an ESME. It is called on its own goroutine, up to the inbound window at
// once, and ctx ends when the session closes. A panic is answered ESME_RSYSERR.
type Handler interface {
	ServeSMPP(ctx context.Context, s *Session, req Request) Response
}

// HandlerFunc is a function used as a Handler.
type HandlerFunc func(ctx context.Context, s *Session, req Request) Response

// ServeSMPP calls f.
func (f HandlerFunc) ServeSMPP(ctx context.Context, s *Session, req Request) Response {
	return f(ctx, s, req)
}

// bindDecision is an SMSC's answer to a bind request.
type bindDecision struct {
	status  CommandStatus
	account string
	data    any
}

// hooks are what the server or client side adds to the engine.
type hooks struct {
	// systemID is the local system_id, in bind responses (SMSC).
	systemID string
	// bind decides a bind request (SMSC). It may reserve the session in a registry.
	bind func(ctx context.Context, s *Session, b *Bind) bindDecision
	// bound is called once the session is bound, after the bind response.
	bound func(s *Session)
	// closed is called once, when the session closes.
	closed func(s *Session)
	// decodeError is called for a request whose body does not decode, after its error response.
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

// Session is an SMPP session over a network connection, on either side (§2.2). It frames PDUs with the codec,
// allocates sequence numbers, matches responses to requests, keeps the windows and timers, answers enquire_link,
// unbind and generic_nack itself, and passes the other requests to its Handler. It is safe for concurrent use.
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

	mu       sync.Mutex
	closeErr error
	closed   bool
	pending  map[uint32]*pending
	seq      uint32
	// unbinding is set, under mu, once an unbind is sent or received: no new request is sent or handled after it.
	unbinding bool
	// inflight counts the requests being handled, and idle, when set, is closed once it drops to zero.
	inflight int
	idle     chan struct{}
	// Set at bind.
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

// start runs the reader and the timers.
func (s *Session) start() {
	go s.readLoop()
	go s.watch()
}

// ID is a number that identifies the session in this process.
func (s *Session) ID() uint64 { return s.id }

// State is the state of the session.
func (s *Session) State() State { return State(s.state.Load()) }

// RemoteAddr is the address of the peer.
func (s *Session) RemoteAddr() net.Addr { return s.conn.RemoteAddr() }

// Done is closed when the session is closed.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session closed, or nil while it is open.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closeErr
}

// SystemID is the peer's system_id: the ESME's from its bind request, or the SMSC's from its bind response.
func (s *Session) SystemID() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.systemID
}

// Account is the account that the server's Authenticate bound the session to. It is empty on a client.
func (s *Session) Account() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.account
}

// Data is the value that the server's Authenticate attached to the session.
func (s *Session) Data() any {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.data
}

// SessionInfo describes a session.
type SessionInfo struct {
	ID         uint64
	Account    string
	SystemID   string
	State      State
	RemoteAddr string
	BoundAt    time.Time
	// InterfaceVersion is the peer's: the ESME's interface_version, or the SMSC's sc_interface_version (0 when
	// absent).
	InterfaceVersion byte
}

// Info describes the session.
func (s *Session) Info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	return SessionInfo{
		ID: s.id, Account: s.account, SystemID: s.systemID, State: s.State(), RemoteAddr: s.conn.RemoteAddr().String(),
		BoundAt: s.boundAt, InterfaceVersion: s.peerVersion,
	}
}

// Close closes the session at once, without unbind.
func (s *Session) Close() error {
	s.closeWith(ErrClosed)

	return nil
}

// closeWith closes the session for the reason err: the connection, the pending requests, the handlers' context.
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

// write writes one encoded PDU, within the response_timer. A failed write closes the session.
func (s *Session) write(b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	return s.writeLocked(b)
}

// writeLocked is write with s.wmu held.
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

// emptyResponse is the response to the request id with an empty body. A bind response carries the local
// system_id, which bind_transceiver_resp keeps even on an error (Table 4-6).
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

// respond writes the response to the request h. A response that does not encode, or that is not of the request's
// response type, is replaced by an empty one with ESME_RSYSERR.
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

// nack writes a generic_nack (§4.3). sequence is the request's, or 0 when it cannot be decoded (Table 4-9).
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
				// The PDU boundaries are lost (§4.3): answer and close.
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

// handleResponse matches a response, or a generic_nack, to the request of the same sequence_number. Responses may
// arrive in any order (§2.5.2). One that matches no request, or whose command_id is not the request's response, is
// discarded.
func (s *Session) handleResponse(h Header, body []byte) {
	if !h.ID.Defined() {
		// An undefined command_id is answered generic_nack ESME_RINVCMDID (§3.3), response or not.
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

// bound records a successful bind response on an ESME, in the reader so that the next PDU is read in the new state.
// The SMSC supports optional parameters only if the response has sc_interface_version (§3.4).
func (s *Session) bound(id CommandID, r *BindResp) {
	s.mu.Lock()
	s.systemID, s.boundAt = r.SystemID, time.Now()

	if v := r.Options.SCInterfaceVersion; v != nil {
		s.peerVersion, s.peerTLV = *v, *v >= InterfaceVersion
	}
	s.mu.Unlock()

	if !s.state.CompareAndSwap(int32(StateOpen), int32(boundState(id))) {
		return // closed meanwhile
	}

	s.poke()

	if s.hooks.bound != nil {
		s.hooks.bound(s)
	}
}

// handleRequest answers a request, or passes it to the Handler. The checks are in the order of the header: a
// command that this entity does not support, or that the peer's role does not issue (Table 2-1), is answered
// generic_nack ESME_RINVCMDID (§3.3, §4.3); one that the state does not allow, its response with ESME_RALYBND for a
// bind or ESME_RINVBNDSTS otherwise (Table 5-2); one whose body does not decode, generic_nack ESME_RINVCMDLEN for a
// wrong length (§4.3) or its response with the error's status otherwise (§2.8).
func (s *Session) handleRequest(h Header, body []byte) {
	if h.Sequence == 0 || h.Sequence > MaxSequence {
		// A sequence_number outside §5.1.4 cannot be echoed: the PDU "cannot be decoded" (Table 4-9).
		s.nack(0, StatusUnknownErr)

		return
	}

	peer := roleESME
	if s.role == roleESME {
		peer = roleSMSC
	}

	if h.ID == CmdAlertNotification {
		// alert_notification has "no SMPP response" (§2.8, §4.12).
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

// handleBind answers a bind request on an SMSC (§4.1). A successful response carries sc_interface_version when the
// ESME supports optional parameters (§3.4, §5.3.2.25). The session is bound once the response is written, so that
// nothing is sent to the ESME before it.
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
		return // closed meanwhile
	}

	s.poke()
	s.log.Info("SMPP session bound", slog.String("system_id", b.SystemID), slog.String("state", s.State().String()))

	if s.hooks.bound != nil {
		s.hooks.bound(s)
	}
}

// serve passes a request to the Handler, within the inbound window, and writes its response.
// The in-flight count is checked and incremented under s.mu together with unbinding, so that an unbind waits for
// every request accepted before it.
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

// waitHandlers waits for the requests being handled to be answered, at most until ctx ends.
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

// peerUnbind answers the peer's unbind: no new request is handled, the ones in progress are answered, then
// unbind_resp, and the session closes once the peer has closed the connection or after unbindLinger.
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

// poke makes the timers re-evaluate, after a change of state.
func (s *Session) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// watch runs the session_init_timer, the enquire_link_timer and the inactivity_timer (§7.2).
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

// check applies the timers at now, and returns how long until the next one is due.
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

// keepAlive sends an enquire_link, and closes the session if it is not answered.
func (s *Session) keepAlive() {
	defer s.enquiring.Store(false)

	_, _, err := s.request(s.ctx, &EnquireLink{}, false)
	if errors.Is(err, ErrResponseTimeout) {
		s.closeWith(ErrEnquireLinkTimeout)
	}
}

// register allocates the next free sequence_number, from 1 to 0x7FFFFFFF and back to 1 (§5.1.4), for a request.
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

// send registers the request p and writes it. The writer's lock is held from the registration to the write, so that
// a request registered before an unbind is also written before it.
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

// refuse is the error for sending the request id now, or nil: the session is closed, the role does not issue it or
// the state does not allow it (Table 2-1), or an unbind has started. It runs again in register, under the lock that
// sets unbinding, so that nothing but the unbind itself is sent after the unbind (§4.2). s.mu is held.
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

// startUnbind sets unbinding, and reports whether it was not set already.
func (s *Session) startUnbind() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	first := !s.unbinding
	s.unbinding = true

	return first
}

// isUnbinding reports whether an unbind has been sent or received: the session takes no new request and closes soon.
func (s *Session) isUnbinding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.unbinding
}

// closedError is ErrClosed with the reason; s.mu is held.
func (s *Session) closedError() error {
	if s.closeErr == nil || errors.Is(s.closeErr, ErrClosed) {
		return ErrClosed
	}

	return fmt.Errorf("%w: %w", ErrClosed, s.closeErr)
}

// hasOptions reports whether p carries optional parameters.
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

// Send sends the request p and waits for its response, within the outbound window and the response_timer. A
// response with a command_status other than ESME_ROK, or a generic_nack, is returned with a *StatusError. A request
// that the state or the role does not allow returns a *StateError without being sent. bind and unbind change the
// session's state, so they are not sent through Send but by the Client and by Unbind: Send returns a *StateError
// for them.
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

// SubmitSM submits a short message (§4.4), on an ESME bound as a transmitter or transceiver, and returns its
// message_id.
func (s *Session) SubmitSM(ctx context.Context, m *SubmitSM) (string, error) {
	_, p, err := s.Send(ctx, m)
	if err != nil {
		return "", err
	}

	return p.(*SubmitSMResp).MessageID, nil
}

// DeliverSM delivers a short message (§4.6), on an SMSC bound to a receiver or transceiver.
func (s *Session) DeliverSM(ctx context.Context, m *DeliverSM) error {
	_, _, err := s.Send(ctx, m)

	return err
}

// EnquireLink checks that the peer answers (§4.11).
func (s *Session) EnquireLink(ctx context.Context) error {
	_, _, err := s.request(ctx, &EnquireLink{}, false)

	return err
}

// bind sends a bind request on an ESME (§4.1).
func (s *Session) bind(ctx context.Context, b *Bind) (*BindResp, error) {
	_, p, err := s.request(ctx, b, false)
	if err != nil {
		return nil, err
	}

	return p.(*BindResp), nil
}

// Unbind ends a bound session in order (§4.2): no new request is sent or handled, the requests being handled are
// answered, then unbind is sent, and the session closes once unbind_resp arrives or the response_timer expires.
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
		// The peer unbound at the same time.
		return nil
	}

	return err
}
