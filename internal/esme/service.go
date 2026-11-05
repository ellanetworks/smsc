package esme

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/ratelimit"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/smpp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/crypto/bcrypt"
)

const (
	interfaceVersion34 = 0x34
	MaxSessions        = 4
	recordTimeout      = 10 * time.Second
	syncInterval       = 10 * time.Second
)

// SMPP 3.4 section 2.2
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

func (b BindType) receives() bool { return b == Receiver || b == Transceiver }

func (b BindType) transmits() bool { return b == Transmitter || b == Transceiver }

var permissions = map[BindType][]api.Permission{
	Transmitter: {api.PermCreateMessage},
	Receiver:    {api.PermReadMessage, api.PermReadMessageContent},
	Transceiver: {api.PermCreateMessage, api.PermReadMessage, api.PermReadMessageContent},
}

type Conn interface {
	Deliver(ctx context.Context, d *smpp.DeliverSM) (smpp.CommandStatus, error)
	Close()
}

type Session struct {
	ID       uint64
	ESME     db.ESME
	UserID   int64
	BindType BindType
	Remote   netip.AddrPort
	BoundAt  time.Time

	conn     Conn
	inflight int
	closed   bool
}

type SessionInfo struct {
	BindType BindType
	Remote   netip.AddrPort
	BoundAt  time.Time
}

type Store interface {
	GetESMEBySystemID(ctx context.Context, systemID string) (db.ESME, error)
	GetUserByID(ctx context.Context, id int64) (db.User, error)
	ClaimNotifications(ctx context.Context, nodeID string, esmeID int64, now time.Time, lease time.Duration,
		limit int) ([]db.Notification, error)
	OpenSMPPSession(ctx context.Context, s db.SMPPSession, limit int) error
	CloseSMPPSession(ctx context.Context, nodeID string, sessionID uint64) error
	SyncSMPPSessions(ctx context.Context, nodeID string, live []uint64) error
	NodeSMPPSessions(ctx context.Context, nodeID string) ([]uint64, error)
	CloseNotification(ctx context.Context, id int64, result db.NotificationResult) error
	RetryNotification(ctx context.Context, id int64, next time.Time, nodeID string) error
	ReleaseNotification(ctx context.Context, id int64, nodeID string, now time.Time) error
	ExpireNotifications(ctx context.Context, now time.Time) (map[db.NotificationType]int, error)
	NextNotificationAt(ctx context.Context, esmeID int64, now time.Time) (time.Time, bool, error)
}

type Service struct {
	Store          Store
	Submitter      *submission.Submitter
	Settings       func() settings.Settings
	Notify         func()
	Received       *intake.Received
	Metrics        *Metrics
	BcryptCost     int
	Accounts       interface{ CloseAccount(account string) int }
	Node           string
	TracerProvider trace.TracerProvider
	Now            func() time.Time
	Logger         *slog.Logger

	initOnce  sync.Once
	dummyOnce sync.Once
	dummyHash []byte
	binds     *ratelimit.Limiter
	failures  *ratelimit.Limiter
	outsiders *ratelimit.Limiter
	bcrypt    chan struct{}
	wake      chan struct{}

	sends    sync.WaitGroup
	mu       sync.Mutex
	sessions map[uint64]*Session
	sending  map[int64]bool
	opened   map[uint64]bool
	synced   bool
	draining bool
	unbound  atomic.Uint64
	records  sync.WaitGroup
	stopping bool
}

func (s *Service) init() {
	s.initOnce.Do(func() {
		if s.Now == nil {
			s.Now = time.Now
		}

		if s.Logger == nil {
			s.Logger = slog.Default()
		}

		s.binds = ratelimit.New(api.LoginRateLimit, api.LoginRateWindow, s.Now)
		s.failures = ratelimit.New(api.AccountFailureLimit, api.AccountFailureWindow, s.Now)
		s.outsiders = ratelimit.New(api.AccountFailureLimit, api.AccountFailureWindow, s.Now)
		s.bcrypt = make(chan struct{}, runtime.GOMAXPROCS(0))
		s.wake = make(chan struct{}, 1)
		s.sessions = map[uint64]*Session{}
		s.sending = map[int64]bool{}
		s.opened = map[uint64]bool{}
	})
}

func (s *Service) SetDraining(draining bool) {
	s.init()

	s.mu.Lock()
	s.draining = draining
	s.mu.Unlock()
}

// SMPP 3.4 section 4.1
func (s *Service) Authenticate(ctx context.Context, req smpp.BindRequest) smpp.BindOutcome {
	s.init()

	remote := addrPortOf(req.RemoteAddr)

	sess, reason, err := s.bind(ctx, s.sessionID(req), remote, req.Bind)

	log := s.Logger.With(slog.String("system_id", req.Bind.SystemID), slog.String("remote", remote.String()),
		slog.String("bind", req.Bind.ID.String()))

	switch {
	case err != nil:
		log.ErrorContext(ctx, "failed to check an SMPP bind", slog.Any("error", err))
	case reason != "":
		log.InfoContext(ctx, "SMPP bind refused", slog.String("reason", reason))
	default:
		s.Metrics.bind(true)

		return smpp.BindOutcome{
			Status:      smpp.StatusOK,
			Account:     accountOf(sess.ESME.ID),
			Data:        sess,
			MaxSessions: MaxSessions,
		}
	}

	s.Metrics.bind(false)

	return smpp.BindOutcome{Status: smpp.StatusBindFail}
}

func (s *Service) sessionID(req smpp.BindRequest) uint64 {
	if req.Session != nil {
		return req.Session.ID()
	}

	return math.MaxInt64 - s.unbound.Add(1)
}

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

func (s *Service) bind(ctx context.Context, id uint64, remote netip.AddrPort, b *smpp.Bind) (*Session, string, error) {
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
	inside := known && slices.ContainsFunc(e.AllowedNetworks, func(p netip.Prefix) bool { return p.Contains(addr) })

	failures := s.outsiders
	if inside {
		failures = s.failures
	}

	if failures.Exceeded(b.SystemID) {
		return nil, "too many wrong passwords for the account", nil
	}

	hash := s.dummy()
	if known {
		hash = []byte(e.PasswordHash)
	}

	match, err := s.checkPassword(ctx, hash, b.Password)
	if err != nil {
		return nil, "", err
	}

	switch {
	case !known:
		failures.Allow(b.SystemID)
		return nil, "unknown system_id", nil
	case !match:
		failures.Allow(b.SystemID)
		return nil, "wrong password", nil
	case !inside:
		return nil, "client address not in the allowed networks", nil
	}

	s.failures.Reset(b.SystemID)

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
	draining, synced := s.draining, s.synced
	s.opened[id] = true
	s.mu.Unlock()

	switch {
	case draining:
		s.forget(id)
		return nil, "the node is draining", nil
	case !synced:
		s.forget(id)
		return nil, "the node is starting", nil
	}

	err = s.Store.OpenSMPPSession(ctx, db.SMPPSession{
		NodeID: s.Node, SessionID: id, ESMEID: e.ID, BindType: string(bindType), Remote: remote.String(),
		BoundAt: s.Now(),
	}, MaxSessions)

	switch {
	case errors.Is(err, db.ErrTooManySessions):
		s.forget(id)
		return nil, "too many sessions for the account", nil
	case err != nil:
		s.forget(id)
		return nil, "the session could not be recorded: " + err.Error(), nil
	}

	return &Session{ID: id, ESME: e, UserID: u.ID, BindType: bindType, Remote: remote}, "", nil
}

func (s *Service) forget(id uint64) {
	s.mu.Lock()
	delete(s.opened, id)
	s.mu.Unlock()
}

func (s *Service) dummy() []byte {
	s.dummyOnce.Do(func() {
		password := make([]byte, 32)
		_, _ = rand.Read(password)

		hash, err := bcrypt.GenerateFromPassword(password, s.BcryptCost)
		if err != nil {
			panic(fmt.Sprintf("esme: bcrypt cost %d: %v", s.BcryptCost, err))
		}

		s.dummyHash = hash
	})

	return s.dummyHash
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

func (s *Service) OnBound(ls *smpp.Session) {
	sess, ok := ls.Data().(*Session)
	if !ok {
		return
	}

	sess.BoundAt = ls.Info().BoundAt
	s.attach(sess, libraryConn{ls})
}

func (s *Service) OnClosed(ls *smpp.Session) {
	if sess, ok := ls.Data().(*Session); ok {
		s.detach(sess)
	}

	s.closeRecord(ls.ID())
}

func (s *Service) closeRecord(id uint64) {
	s.init()

	s.mu.Lock()
	opened := s.opened[id]
	delete(s.opened, id)

	async := opened && !s.stopping
	if async {
		s.records.Add(1)
	}
	s.mu.Unlock()

	if !opened {
		return
	}

	record := func() {
		ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
		defer cancel()

		if err := s.Store.CloseSMPPSession(ctx, s.Node, id); err != nil {
			s.Logger.WarnContext(ctx, "failed to record the end of an SMPP session; it is cleaned up later",
				slog.Uint64("session", id), slog.Any("error", err))
		}
	}

	if !async {
		record()
		return
	}

	go func() {
		defer s.records.Done()

		record()
	}()
}

// SMPP 3.4 section 2.2
func (s *Service) RunSessions(ctx context.Context) {
	s.init()

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		s.syncSessions(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) syncSessions(ctx context.Context) {
	s.mu.Lock()
	live := make([]uint64, 0, len(s.opened))

	for id := range s.opened {
		live = append(live, id)
	}
	s.mu.Unlock()

	slices.Sort(live)

	recorded, err := s.Store.NodeSMPPSessions(ctx, s.Node)
	if err != nil {
		return
	}

	stale := slices.ContainsFunc(recorded, func(id uint64) bool { _, ok := slices.BinarySearch(live, id); return !ok })

	if stale {
		if err := s.Store.SyncSMPPSessions(ctx, s.Node, live); err != nil {
			if ctx.Err() == nil {
				s.Logger.DebugContext(ctx, "failed to clean up the recorded SMPP sessions", slog.Any("error", err))
			}

			return
		}
	}

	s.mu.Lock()
	s.synced = true
	s.mu.Unlock()
}

func (s *Service) OnDecodeError(_ *smpp.Session, h smpp.Header, _ error) {
	if h.ID == smpp.CmdSubmitSM {
		s.Received.Add(intake.OriginSMPP, intake.Rejected, 1)
	}
}

func (s *Service) OnRefused(_ *smpp.Session, h smpp.Header, status smpp.CommandStatus) {
	if h.ID != smpp.CmdSubmitSM {
		return
	}

	result := intake.Rejected
	if status == smpp.StatusThrottled {
		result = intake.Throttled
	}

	s.Received.Add(intake.OriginSMPP, result, 1)
}

func (s *Service) ServeSMPP(ctx context.Context, ls *smpp.Session, req smpp.Request) smpp.Response {
	started := s.now()

	sm, ok := req.PDU.(*smpp.SubmitSM)
	sess, bound := ls.Data().(*Session)

	if !ok || !bound {
		return smpp.Response{Status: smpp.StatusInvCmdID}
	}

	return s.serveSubmit(ctx, sess, sm, started)
}

func (s *Service) serveSubmit(ctx context.Context, sess *Session, sm *smpp.SubmitSM, started time.Time) smpp.Response {
	ctx, span := s.startSpan(ctx, trace.SpanKindServer, smpp.CmdSubmitSM, sess)
	answered := false

	defer func() {
		if !answered {
			status := smpp.StatusSysErr
			endSpan(span, &status, status.String(), nil)
		}
	}()

	id, status := s.SubmitSM(ctx, sess, sm)
	answered = true

	if status != smpp.StatusOK {
		return smpp.Response{Status: status, Written: func(err error) {
			s.Metrics.submitted(s.now().Sub(started))
			endSubmitSpan(span, status, err)
		}}
	}

	return smpp.Response{
		Status: smpp.StatusOK,
		PDU:    &smpp.SubmitSMResp{MessageID: id},
		Written: func(err error) {
			s.Metrics.submitted(s.now().Sub(started))
			endSubmitSpan(span, status, err)
			s.Submitted()
		},
	}
}

func endSubmitSpan(span trace.Span, status smpp.CommandStatus, err error) {
	switch {
	case err != nil:
		endSpan(span, &status, semconv.ErrorTypeOther.Value.AsString(), err)
	case status == smpp.StatusSysErr:
		endSpan(span, &status, status.String(), nil)
	default:
		endSpan(span, &status, "", nil)
	}
}

func (s *Service) now() time.Time {
	s.init()
	return s.Now()
}

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

func (s *Service) attach(sess *Session, conn Conn) {
	s.init()

	s.mu.Lock()
	sess.conn = conn
	s.sessions[sess.ID] = sess
	s.mu.Unlock()

	s.Metrics.session(sess.BindType, 1)
	s.wakeDispatcher()
}

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

func (s *Service) CloseESME(esmeID int64) {
	s.init()

	if s.Accounts != nil {
		s.Accounts.CloseAccount(accountOf(esmeID))
	}

	s.closeWhere(func(sess *Session) bool { return sess.ESME.ID == esmeID })
}

func (s *Service) CloseAll() {
	s.init()
	s.closeWhere(func(*Session) bool { return true })
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

// SMPP 3.4 section 4.4.2
// SMPP 3.4 section 5.1.3
func (s *Service) SubmitSM(ctx context.Context, sess *Session, sm *smpp.SubmitSM) (string, smpp.CommandStatus) {
	s.init()

	log := s.Logger.With(slog.String("system_id", sess.ESME.SystemID))

	if !sess.BindType.transmits() {
		s.Received.Add(intake.OriginSMPP, intake.Rejected, 1)
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
		log.DebugContext(ctx, "submit_sm refused", slog.String("reason", rej.reason), slog.String("status", rej.status.String()))

		return "", rej.status
	}

	ids, err := s.Submitter.Submit(ctx, req)
	if err != nil {
		return "", s.submitFailed(ctx, log, err)
	}

	return strconv.FormatInt(ids[0], 10), smpp.StatusOK
}

func (s *Service) submitFailed(ctx context.Context, log *slog.Logger, err error) smpp.CommandStatus {
	status := submissionStatus(err)

	switch {
	case status != smpp.StatusSysErr:
		s.Received.Add(intake.OriginSMPP, intake.Rejected, 1)
		log.DebugContext(ctx, "submit_sm refused", slog.Any("reason", err), slog.String("status", status.String()))
	case errors.Is(err, submission.ErrStore):
		log.ErrorContext(ctx, "failed to store a submit_sm", slog.Any("error", err))
	default:
		s.Received.Add(intake.OriginSMPP, intake.Error, 1)
		log.ErrorContext(ctx, "failed to encode a submit_sm", slog.Any("error", err))
	}

	return status
}

func (s *Service) Submitted() {
	if s.Notify != nil {
		s.Notify()
	}
}
