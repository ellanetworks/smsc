package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/prometheus/client_golang/prometheus"
)

type Store interface {
	CreateMessages(ctx context.Context, messages []db.NewMessage) ([]int64, error)
	GetMessage(ctx context.Context, id int64) (db.Message, error)
	ListMessages(ctx context.Context, f db.MessageFilter, page, perPage int) ([]db.Message, int, error)
	ListDeliveryAttempts(ctx context.Context, messageID int64, page, perPage int) ([]db.DeliveryAttempt, int, error)
	ListDeliverReports(ctx context.Context, messageID int64) ([]db.DeliveryAttempt, error)
	StatusReportsFor(ctx context.Context, messageIDs []int64) (map[int64][]db.Message, error)
	SystemIDsFor(ctx context.Context, messageIDs []int64) (map[int64]string, error)
}

// ESMEStore stores the ESME accounts.
type ESMEStore interface {
	CountESMEs(ctx context.Context) (int, error)
	CreateESME(ctx context.Context, e db.ESME) (int64, error)
	UpdateESME(ctx context.Context, e db.ESME) error
	DeleteESME(ctx context.Context, id int64) error
	GetESMEBySystemID(ctx context.Context, systemID string) (db.ESME, error)
	ListESMEs(ctx context.Context) ([]db.ESME, error)
	ListESMEsOfUser(ctx context.Context, userID int64) ([]db.ESME, error)
}

// SMPP is the SMPP listener: the sessions of the ESMEs, which a change to an ESME or its user closes, so that it binds
// again under the checks of the change.
type SMPP interface {
	CloseESME(esmeID int64)
	// CloseUser closes the sessions of the ESMEs that the database has for a user.
	CloseUser(ctx context.Context, userID int64) error
	Sessions(esmeID int64) []SMPPSession
}

// SMPPSession is a bound SMPP session.
type SMPPSession struct {
	BindType string
	Remote   string
	BoundAt  time.Time
}

// noSMPP is the SMPP of an SMSC without an SMPP listener.
type noSMPP struct{}

func (noSMPP) CloseESME(int64)                        {}
func (noSMPP) CloseUser(context.Context, int64) error { return nil }
func (noSMPP) Sessions(int64) []SMPPSession           { return nil }

type Diameter interface {
	Identity() diameter.Identity
	Peers() []diameter.PeerStatus
	// Peer returns the status of the peer of a node ID, such as a configured peer's settings.Peer.NodeID.
	Peer(id string) (diameter.PeerStatus, bool)
	Routes() []Route
}

// Route is a realm-based routing table entry (RFC 6733, section 2.7) and the
// open peers that can currently serve it. A default route has no realm.
type Route struct {
	Realm         string
	ApplicationID uint32
	Default       bool
	Peers         []string
}

type Settings interface {
	Get() settings.Settings
	UpdateOperator(ctx context.Context, o settings.Operator) error
	UpdateDelivery(ctx context.Context, d settings.Delivery) error
	UpdateRetention(ctx context.Context, r settings.Retention) error
	CreatePeer(ctx context.Context, p settings.Peer) (settings.Peer, error)
	UpdatePeer(ctx context.Context, p settings.Peer) error
	DeletePeer(ctx context.Context, id int64) error
}

type Config struct {
	Store    Store
	Diameter Diameter
	Frontend fs.FS
	Settings Settings
	// Submitter stores the messages the API submits, and counts them.
	Submitter *submission.Submitter
	Notify    func()
	// Received counts the messages the API submits. It is the Submitter's when that is nil.
	Received *intake.Received
	// Ready reports why the SMSC cannot serve requests, or nil when it can. GET /api/v1/readyz answers 503 when
	// it fails. Nil is always ready.
	Ready func(ctx context.Context) error
	// Metrics are the metrics GET /api/v1/metrics serves.
	Metrics prometheus.Gatherer
	// Auth stores the users, their sessions and their API tokens.
	Auth AuthStore
	// ESMEs stores the ESME accounts, and SMPP is the listener they bind to, or nil for none.
	ESMEs ESMEStore
	SMPP  SMPP
	// JWTSecret signs the access tokens.
	JWTSecret []byte
	// SecureCookie marks the session cookie Secure, and turns on HSTS, for an API served over TLS.
	SecureCookie bool
	// BcryptCost is the cost of the password hashes: bcrypt.DefaultCost when zero.
	BcryptCost int
	Now        func() time.Time
	Logger     *slog.Logger
}

// route is a route of the API, and the permission that it needs: public for none.
type route struct {
	pattern string
	perm    Permission
	handler http.Handler
}

func routes(cfg Config, a *auth) []route {
	rs := []route{
		{"GET /api/v1/livez", public, Livez()},
		{"GET /api/v1/readyz", public, Readyz(cfg)},
		{"GET /api/v1/status", public, GetStatus(cfg)},
		{"GET /api/v1/metrics", public, GetMetrics(cfg)},
		{"GET /api/v1/openapi.yaml", public, OpenAPISpec()},
		{"POST /api/v1/init", public, Initialize(a)},
		{"POST /api/v1/auth/login", public, Login(a)},
		{"POST /api/v1/auth/refresh", public, Refresh(a)},
		{"POST /api/v1/auth/logout", public, Logout(a)},

		{"POST /api/v1/messages", PermCreateMessage, CreateMessage(cfg)},
		{"GET /api/v1/messages", PermReadMessage, ListMessages(cfg)},
		{"GET /api/v1/messages/retention", PermReadRetention, GetRetention(cfg)},
		{"PUT /api/v1/messages/retention", PermUpdateRetention, UpdateRetention(cfg)},
		{"GET /api/v1/messages/{id}", PermReadMessage, GetMessage(cfg)},
		{"GET /api/v1/messages/{id}/content", PermReadMessageContent, GetMessageContent(cfg, a)},
		{"GET /api/v1/messages/{id}/attempts", PermReadMessage, ListMessageAttempts(cfg)},
		{"GET /api/v1/diameter", PermReadDiameter, GetDiameterStatus(cfg)},
		{"GET /api/v1/diameter/peers", PermReadDiameter, ListPeers(cfg)},
		{"POST /api/v1/diameter/peers", PermUpdateDiameter, CreatePeer(a)},
		{"GET /api/v1/diameter/peers/{id}", PermReadDiameter, GetPeer(cfg)},
		{"PUT /api/v1/diameter/peers/{id}", PermUpdateDiameter, UpdatePeer(a)},
		{"DELETE /api/v1/diameter/peers/{id}", PermUpdateDiameter, DeletePeer(a)},
		// An ESME is a credential that acts as its user, so changing one needs a login, as an API token does.
		{"GET /api/v1/esmes", PermReadESME, ListESMEs(cfg)},
		{"POST /api/v1/esmes", PermUpdateESME, a.needsLogin(CreateESME(a))},
		{"GET /api/v1/esmes/{system_id}", PermReadESME, GetESME(cfg)},
		{"PUT /api/v1/esmes/{system_id}", PermUpdateESME, a.needsLogin(UpdateESME(a))},
		{"DELETE /api/v1/esmes/{system_id}", PermUpdateESME, a.needsLogin(DeleteESME(a))},
		{"GET /api/v1/operator", PermReadOperator, GetOperator(cfg)},
		{"PUT /api/v1/operator", PermUpdateOperator, UpdateOperator(cfg)},
		{"GET /api/v1/delivery", PermReadDelivery, GetDelivery(cfg)},
		{"PUT /api/v1/delivery", PermUpdateDelivery, UpdateDelivery(cfg)},

		// The routes that create credentials or manage users need a login rather than an API token, so that a leaked
		// API token cannot make itself others that outlive its revocation, or take an account.
		{"GET /api/v1/users/me", PermSelf, GetMyUser(a)},
		{"PUT /api/v1/users/me/password", PermSelf, a.needsLogin(UpdateMyPassword(a))},
		{"GET /api/v1/users/me/api-tokens", PermSelf, ListAPITokens(a, myself(cfg))},
		{"POST /api/v1/users/me/api-tokens", PermSelf, a.needsLogin(CreateAPIToken(a, myself(cfg)))},
		{"DELETE /api/v1/users/me/api-tokens/{id}", PermSelf, DeleteAPIToken(a, myself(cfg))},

		{"GET /api/v1/users", PermManageUsers, a.needsLogin(ListUsers(a))},
		{"POST /api/v1/users", PermManageUsers, a.needsLogin(CreateUser(a))},
		{"GET /api/v1/users/{email}", PermManageUsers, a.needsLogin(GetUser(a))},
		{"PUT /api/v1/users/{email}", PermManageUsers, a.needsLogin(UpdateUser(a))},
		{"DELETE /api/v1/users/{email}", PermManageUsers, a.needsLogin(DeleteUser(a))},
		{"PUT /api/v1/users/{email}/password", PermManageUsers, a.needsLogin(UpdateUserPassword(a))},
		{"GET /api/v1/users/{email}/api-tokens", PermManageUsers, a.needsLogin(ListAPITokens(a, userOfPath(cfg)))},
		{"POST /api/v1/users/{email}/api-tokens", PermManageUsers, a.needsLogin(CreateAPIToken(a, userOfPath(cfg)))},
		{"DELETE /api/v1/users/{email}/api-tokens/{id}", PermManageUsers, a.needsLogin(DeleteAPIToken(a, userOfPath(cfg)))},

		// The profiles take an API token, so that a profiler can scrape them.
		{"GET /api/v1/pprof/{$}", PermReadProfiles, http.HandlerFunc(pprof.Index)},
		{"GET /api/v1/pprof/allocs", PermReadProfiles, pprof.Handler("allocs")},
		{"GET /api/v1/pprof/goroutine", PermReadProfiles, pprof.Handler("goroutine")},
		{"GET /api/v1/pprof/goroutineleak", PermReadProfiles, pprof.Handler("goroutineleak")},
		{"GET /api/v1/pprof/heap", PermReadProfiles, pprof.Handler("heap")},
		{"GET /api/v1/pprof/threadcreate", PermReadProfiles, pprof.Handler("threadcreate")},
		{"GET /api/v1/pprof/cmdline", PermReadProfiles, http.HandlerFunc(pprof.Cmdline)},
		{"GET /api/v1/pprof/profile", PermReadProfiles, http.HandlerFunc(pprof.Profile)},
		{"GET /api/v1/pprof/symbol", PermReadProfiles, http.HandlerFunc(pprof.Symbol)},
		{"POST /api/v1/pprof/symbol", PermReadProfiles, http.HandlerFunc(pprof.Symbol)},
		{"GET /api/v1/pprof/trace", PermReadProfiles, http.HandlerFunc(pprof.Trace)},
	}

	// The UI is static files: all its data comes from the API, which authenticates it.
	if cfg.Frontend != nil {
		rs = append(rs, route{"GET /", public, Frontend(cfg.Frontend)})
	}

	return rs
}

func NewHandler(cfg Config) http.Handler {
	if cfg.SMPP == nil {
		cfg.SMPP = noSMPP{}
	}

	if cfg.Submitter == nil {
		cfg.Submitter = &submission.Submitter{
			Store:    cfg.Store,
			Settings: cfg.Settings.Get,
			Received: cfg.Received,
			Now:      cfg.Now,
		}
	}

	a := newAuth(cfg)
	mux := http.NewServeMux()

	for _, rt := range routes(cfg, a) {
		h := rt.handler
		if rt.perm != public {
			h = a.authenticate(a.authorize(rt.perm, h))
		}

		mux.Handle(rt.pattern, h)
	}

	// The session cookie authenticates the refresh and the logout, so a page of another origin must not be able to
	// send them. CrossOriginProtection rejects the unsafe requests that a browser tells are cross-origin, and lets
	// through those of other clients, which send none of the headers it checks.
	return securityHeaders(cfg, http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(cfg Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		if cfg.SecureCookie {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		next.ServeHTTP(w, r)
	})
}
