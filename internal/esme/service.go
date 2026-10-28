// Package esme is the SMSC side of the SMPP 3.4 interface to External Short Message Entities, above the protocol layer
// of package smpp: it authenticates the binds of the ESME accounts, maps submit_sm to the messages it stores (SMPP 3.4
// section 4.4), and sends the ESMEs the delivery receipts and SIM answers on their messages as deliver_sm (SMPP 3.4
// sections 2.11 and 4.6).
package esme

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/ratelimit"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/smpp"
	"golang.org/x/crypto/bcrypt"
)

const (
	// interfaceVersion34 is the interface_version of SMPP 3.4. An ESME of an earlier version cannot take optional
	// parameters (SMPP 3.4 section 3.4), which receipts need, so it cannot bind.
	interfaceVersion34 = 0x34
	// DefaultWindow is the most deliver_sm outstanding on a session, the guideline of SMPP 3.4 section 2.6.2.
	DefaultWindow = 10
)

// BindType is the kind of session an ESME binds (SMPP 3.4 section 2.2).
type BindType string

const (
	Transmitter BindType = "transmitter"
	Receiver    BindType = "receiver"
	Transceiver BindType = "transceiver"
)

func bindTypeOf(id smpp.CommandID) (BindType, bool) {
	switch id {
	case smpp.CmdBindTransmitter:
		return Transmitter, true
	case smpp.CmdBindReceiver:
		return Receiver, true
	case smpp.CmdBindTransceiver:
		return Transceiver, true
	default:
		return "", false
	}
}

// receives is whether a session of this kind takes deliver_sm.
func (b BindType) receives() bool { return b == Receiver || b == Transceiver }

// transmits is whether a session of this kind sends submit_sm.
func (b BindType) transmits() bool { return b == Transmitter || b == Transceiver }

// permissions are what the role of an ESME's user must allow for each kind of bind: submitting creates messages, and
// receiving reads their outcome and the SIM answers, which are content.
var permissions = map[BindType][]api.Permission{
	Transmitter: {api.PermCreateMessage},
	Receiver:    {api.PermReadMessage, api.PermReadMessageContent},
	Transceiver: {api.PermCreateMessage, api.PermReadMessage, api.PermReadMessageContent},
}

// Conn is the protocol layer's side of a bound session: an smpp.Session.
type Conn interface {
	// Deliver sends a deliver_sm and returns the command_status of its deliver_sm_resp. It fails when the ESME answers
	// generic_nack, does not answer within the response timer, or the session ends.
	Deliver(ctx context.Context, d *smpp.DeliverSM) (smpp.CommandStatus, error)
	// Close ends the session.
	Close()
}

// Session is a bound session, which the server keeps as its smpp.Session's Data.
type Session struct {
	ID       uint64
	ESME     db.ESME
	UserID   int64
	BindType BindType
	Remote   netip.AddrPort
	BoundAt  time.Time

	// conn, inflight and closed are guarded by the service's mutex.
	conn     Conn
	inflight int
	closed   bool
}

// SessionInfo is a bound session, as the API shows it.
type SessionInfo struct {
	BindType BindType
	Remote   netip.AddrPort
	BoundAt  time.Time
}

// Store is what the service needs of the database.
type Store interface {
	GetESMEBySystemID(ctx context.Context, systemID string) (db.ESME, error)
	GetUserByID(ctx context.Context, id int64) (db.User, error)
	DueNotifications(ctx context.Context, esmeID int64, now time.Time, exclude []int64, limit int) ([]db.Notification, error)
	CloseNotification(ctx context.Context, id int64, result db.NotificationResult, at time.Time) error
	RetryNotification(ctx context.Context, id int64, next, at time.Time) error
	ExpireNotifications(ctx context.Context, now time.Time) (map[db.NotificationType]int, error)
	NextNotificationAt(ctx context.Context, esmeID int64) (time.Time, bool, error)
}

// Service is the SMSC side of the SMPP interface. Its zero values take defaults; it must not be copied once used.
type Service struct {
	Store     Store
	Submitter *submission.Submitter
	Settings  func() settings.Settings
	// Notify tells the delivery that a message is stored.
	Notify func()
	// Received counts the submit_sm that the SMSC refuses; the Submitter counts the others.
	Received *intake.Received
	Metrics  *Metrics
	// BcryptCost is the cost of the dummy hash that an unknown system_id is checked against: bcrypt's default when
	// zero. It should be the cost of the ESME password hashes.
	BcryptCost int
	// Window is the most deliver_sm outstanding on a session: DefaultWindow when zero.
	Window int
	// Accounts is the server's registry of sessions, which closes those of an account, binding ones included. It is
	// set once the server exists.
	Accounts interface{ CloseAccount(account string) int }
	Now      func() time.Time
	Logger   *slog.Logger

	initOnce  sync.Once
	dummyHash []byte
	binds     *ratelimit.Limiter
	failures  *ratelimit.Limiter
	bcrypt    chan struct{}
	wake      chan struct{}

	mu       sync.Mutex
	sessions map[uint64]*Session
	// users are the users of the accounts that bound, so that a change to a user closes their sessions.
	users map[string]int64
}

func (s *Service) init() {
	s.initOnce.Do(func() {
		password := make([]byte, 32)
		_, _ = rand.Read(password) // It never fails.

		dummy, err := bcrypt.GenerateFromPassword(password, s.BcryptCost)
		if err != nil {
			panic(fmt.Sprintf("esme: bcrypt cost %d: %v", s.BcryptCost, err))
		}

		if s.Now == nil {
			s.Now = time.Now
		}

		if s.Logger == nil {
			s.Logger = slog.Default()
		}

		if s.Window <= 0 {
			s.Window = DefaultWindow
		}

		s.dummyHash = dummy
		s.binds = ratelimit.New(api.LoginRateLimit, api.LoginRateWindow, s.Now)
		s.failures = ratelimit.New(api.AccountFailureLimit, api.AccountFailureWindow, s.Now)
		s.bcrypt = make(chan struct{}, runtime.GOMAXPROCS(0))
		s.wake = make(chan struct{}, 1)
		s.sessions = map[uint64]*Session{}
		s.users = map[string]int64{}
	})
}

// Authenticate decides a bind request (SMPP 3.4 section 4.1), as smpp.ServerConfig.Authenticate. Every refusal is
// ESME_RBINDFAIL, so that the response does not tell which check failed; the log does. The checks run in order: the
// binds per client address, the interface version, the password, which bcrypt checks against a dummy hash for an
// unknown system_id so that the time taken does not tell it exists, the wrong passwords per account, the client
// address against the account's networks, and the user's role against the kind of bind. The server then counts the
// account's sessions against max_sessions.
func (s *Service) Authenticate(ctx context.Context, req smpp.BindRequest) smpp.BindOutcome {
	s.init()

	remote := addrPortOf(req.RemoteAddr)

	sess, reason, err := s.bind(ctx, remote, req.Bind)

	log := s.Logger.With(slog.String("system_id", req.Bind.SystemID), slog.String("remote", remote.String()),
		slog.String("bind", req.Bind.ID.String()))

	switch {
	case err != nil:
		log.Error("failed to check an SMPP bind", slog.Any("error", err))
	case reason != "":
		log.Info("SMPP bind refused", slog.String("reason", reason))
	default:
		s.Metrics.bind(true)

		return smpp.BindOutcome{
			Status:      smpp.StatusOK,
			Account:     accountOf(sess.ESME.ID),
			Data:        sess,
			MaxSessions: sess.ESME.MaxSessions,
		}
	}

	s.Metrics.bind(false)

	return smpp.BindOutcome{Status: smpp.StatusBindFail}
}

// accountOf is the account of the server's registry that the sessions of an ESME are filed under: its ID, which a
// change of system_id would not change.
func accountOf(esmeID int64) string { return strconv.FormatInt(esmeID, 10) }

func addrPortOf(a net.Addr) netip.AddrPort {
	if a == nil {
		return netip.AddrPort{}
	}

	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.AddrPort{}
	}

	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// bind returns the session of a bind, or the reason it is refused, or an error when the SMSC cannot check it, which
// refuses it too.
func (s *Service) bind(ctx context.Context, remote netip.AddrPort, b *smpp.Bind) (*Session, string, error) {
	addr := remote.Addr().Unmap()

	if !s.binds.Allow(ratelimit.Key(addr)) {
		return nil, "too many binds from the client", nil
	}

	bindType, ok := bindTypeOf(b.ID)
	if !ok {
		return nil, "not a bind", nil
	}

	if b.InterfaceVersion < interfaceVersion34 {
		return nil, "interface_version 0x" + strconv.FormatUint(uint64(b.InterfaceVersion), 16) + " is earlier than 3.4", nil
	}

	e, err := s.Store.GetESMEBySystemID(ctx, b.SystemID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, "", fmt.Errorf("get ESME: %w", err)
	}

	known := err == nil

	hash := s.dummyHash
	if known {
		hash = []byte(e.PasswordHash)
	}

	if s.failures.Exceeded(b.SystemID) {
		return nil, "too many wrong passwords for the account", nil
	}

	match, err := s.checkPassword(ctx, hash, b.Password)
	if err != nil {
		return nil, "", err
	}

	// An unknown system_id counts as a wrong password, so that a locked account does not tell that it exists.
	switch {
	case !known:
		s.failures.Allow(b.SystemID)
		return nil, "unknown system_id", nil
	case !match:
		s.failures.Allow(b.SystemID)
		return nil, "wrong password", nil
	}

	s.failures.Reset(b.SystemID)

	// An empty list allows no address.
	if !slices.ContainsFunc(e.AllowedNetworks, func(p netip.Prefix) bool { return p.Contains(addr) }) {
		return nil, "client address not in the allowed networks", nil
	}

	u, err := s.Store.GetUserByID(ctx, e.UserID)
	if err != nil {
		return nil, "", fmt.Errorf("get user: %w", err)
	}

	for _, p := range permissions[bindType] {
		if !api.Allowed(u.Role, p) {
			return nil, "the user's role does not allow " + string(p), nil
		}
	}

	s.mu.Lock()
	s.users[accountOf(e.ID)] = u.ID
	s.mu.Unlock()

	return &Session{ESME: e, UserID: u.ID, BindType: bindType, Remote: remote}, "", nil
}

func (s *Service) checkPassword(ctx context.Context, hash []byte, password string) (bool, error) {
	select {
	case s.bcrypt <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	defer func() { <-s.bcrypt }()

	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil, nil
}

// OnBound registers a session once the ESME has its bind response, as smpp.ServerConfig.OnBound.
func (s *Service) OnBound(ls *smpp.Session) {
	sess, ok := ls.Data().(*Session)
	if !ok {
		return
	}

	sess.ID = ls.ID()
	sess.BoundAt = ls.Info().BoundAt
	s.attach(sess, libraryConn{ls})
}

// OnClosed unregisters a session, as smpp.ServerConfig.OnClosed.
func (s *Service) OnClosed(ls *smpp.Session) {
	if sess, ok := ls.Data().(*Session); ok {
		s.detach(sess)
	}
}

// OnDecodeError counts a submit_sm whose body does not decode, which the session refused, as
// smpp.ServerConfig.OnDecodeError.
func (s *Service) OnDecodeError(_ *smpp.Session, h smpp.Header, _ error) {
	if h.ID == smpp.CmdSubmitSM {
		s.Received.Add(intake.OriginSMPP, intake.Rejected, 1)
	}
}

// ServeSMPP answers a submit_sm, as smpp.Handler. The delivery learns of the message once the submit_sm_resp is
// written, so that the ESME has the message_id before any deliver_sm about it.
func (s *Service) ServeSMPP(ctx context.Context, ls *smpp.Session, req smpp.Request) smpp.Response {
	started := s.now()

	sm, ok := req.PDU.(*smpp.SubmitSM)
	sess, bound := ls.Data().(*Session)

	if !ok || !bound {
		return smpp.Response{Status: smpp.StatusInvCmdID}
	}

	id, status := s.SubmitSM(ctx, sess, sm)
	if status != smpp.StatusOK {
		return smpp.Response{Status: status, Written: func(error) { s.Metrics.submitted(s.now().Sub(started)) }}
	}

	return smpp.Response{
		Status: smpp.StatusOK,
		PDU:    &smpp.SubmitSMResp{MessageID: id},
		Written: func(error) {
			s.Metrics.submitted(s.now().Sub(started))
			// The message is stored whether or not the response reached the ESME.
			s.Submitted()
		},
	}
}

func (s *Service) now() time.Time {
	s.init()
	return s.Now()
}

// libraryConn is a session of the protocol layer.
type libraryConn struct{ s *smpp.Session }

func (c libraryConn) Deliver(ctx context.Context, d *smpp.DeliverSM) (smpp.CommandStatus, error) {
	err := c.s.DeliverSM(ctx, d)

	var se *smpp.StatusError
	if errors.As(err, &se) && se.Command == smpp.CmdDeliverSMResp {
		return se.Status, nil
	}

	if err != nil {
		return 0, err
	}

	return smpp.StatusOK, nil
}

func (c libraryConn) Close() { _ = c.s.Close() }

// attach registers a bound session and wakes the dispatcher.
func (s *Service) attach(sess *Session, conn Conn) {
	s.init()

	s.mu.Lock()
	sess.conn = conn
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	s.Metrics.session(sess.BindType, 1)
	s.wakeDispatcher()
}

// detach unregisters a session.
func (s *Service) detach(sess *Session) {
	s.init()

	s.mu.Lock()
	_, ok := s.sessions[sess.ID]
	delete(s.sessions, sess.ID)
	sess.closed = true
	s.mu.Unlock()

	if ok {
		s.Metrics.session(sess.BindType, -1)
	}
}

// CloseESME closes the sessions of an ESME, bound or binding, so that it binds again through the checks of
// Authenticate.
func (s *Service) CloseESME(esmeID int64) {
	s.init()

	if s.Accounts != nil {
		s.Accounts.CloseAccount(accountOf(esmeID))
	}

	s.closeWhere(func(sess *Session) bool { return sess.ESME.ID == esmeID })
}

// CloseUser closes the sessions of the ESMEs of a user, whose role changed or who was deleted.
func (s *Service) CloseUser(userID int64) {
	s.init()

	s.mu.Lock()

	var accounts []string

	for account, u := range s.users {
		if u == userID {
			accounts = append(accounts, account)
		}
	}
	s.mu.Unlock()

	if s.Accounts != nil {
		for _, a := range accounts {
			s.Accounts.CloseAccount(a)
		}
	}

	s.closeWhere(func(sess *Session) bool { return sess.UserID == userID })
}

func (s *Service) closeWhere(match func(*Session) bool) {
	var conns []Conn

	s.mu.Lock()

	for _, sess := range s.sessions {
		if match(sess) && sess.conn != nil {
			conns = append(conns, sess.conn)
		}
	}

	s.mu.Unlock()

	for _, c := range conns {
		c.Close()
	}
}

// Sessions returns the bound sessions of an ESME, oldest first.
func (s *Service) Sessions(esmeID int64) []SessionInfo {
	s.init()

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []SessionInfo

	for _, sess := range s.sessions {
		if sess.ESME.ID == esmeID && !sess.closed {
			out = append(out, SessionInfo{BindType: sess.BindType, Remote: sess.Remote, BoundAt: sess.BoundAt})
		}
	}

	slices.SortFunc(out, func(a, b SessionInfo) int { return a.BoundAt.Compare(b.BoundAt) })

	return out
}

// SubmitSM stores the message of a submit_sm and returns its message_id, the decimal ID of the stored message (SMPP
// 3.4 section 4.4.2), or the command_status that refuses it (SMPP 3.4 section 5.1.3). The caller calls Submitted once
// it has written the submit_sm_resp, so that the ESME has the message_id before any deliver_sm about the message.
func (s *Service) SubmitSM(ctx context.Context, sess *Session, sm *smpp.SubmitSM) (string, smpp.CommandStatus) {
	s.init()

	log := s.Logger.With(slog.String("system_id", sess.ESME.SystemID))

	if !sess.BindType.transmits() {
		return "", smpp.StatusInvBndSts
	}

	current := s.Settings()
	m := mapping{
		esme:   sess.ESME,
		userID: sess.UserID,
		plan:   current.Operator.Numbering,
		now:    s.Now(),
	}

	req, rej := m.request(&sm.Message)
	if rej != nil {
		s.Received.Add(intake.OriginSMPP, intake.Rejected, 1)
		log.Debug("submit_sm refused", slog.String("reason", rej.reason), slog.String("status", rej.status.String()))

		return "", rej.status
	}

	ids, err := s.Submitter.Submit(ctx, req)
	if err != nil {
		status := submissionStatus(err)
		if status != smpp.StatusSysErr {
			s.Received.Add(intake.OriginSMPP, intake.Rejected, 1)
			log.Debug("submit_sm refused", slog.Any("reason", err), slog.String("status", status.String()))
		} else {
			log.Error("failed to store a submit_sm", slog.Any("error", err))
		}

		return "", status
	}

	return strconv.FormatInt(ids[0], 10), smpp.StatusOK
}

// Submitted tells the delivery that a message is stored, once the ESME has its message_id.
func (s *Service) Submitted() {
	if s.Notify != nil {
		s.Notify()
	}
}

// ObserveSubmit times a submit_sm, from when it is read to when its submit_sm_resp is written.
func (s *Service) ObserveSubmit(d time.Duration) {
	s.Metrics.submitted(d)
}
