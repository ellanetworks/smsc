package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/intake"
	"github.com/ellanetworks/smsc/internal/oidc"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/internal/tracing"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

type Store interface {
	CreateMessages(ctx context.Context, messages []db.NewMessage) ([]int64, error)
	GetMessage(ctx context.Context, id int64) (db.Message, error)
	ListMessages(ctx context.Context, f db.MessageFilter, page, perPage int) ([]db.Message, int, error)
	CountMessages(ctx context.Context, f db.MessageFilter) (map[db.MessageStatus]int, error)
	ListDeliveryAttempts(ctx context.Context, messageID int64, page, perPage int) ([]db.DeliveryAttempt, int, error)
	ListDeliverReports(ctx context.Context, messageID int64) ([]db.DeliveryAttempt, error)
	StatusReportsFor(ctx context.Context, messageIDs []int64) (map[int64][]db.Message, error)
	SystemIDsFor(ctx context.Context, messageIDs []int64) (map[int64]string, error)
}

type ESMEStore interface {
	CountESMEs(ctx context.Context) (int, error)
	CreateESME(ctx context.Context, e db.ESME) (int64, error)
	UpdateESME(ctx context.Context, e db.ESME) error
	DeleteESME(ctx context.Context, id int64) error
	GetESMEBySystemID(ctx context.Context, systemID string) (db.ESME, error)
	ListESMEs(ctx context.Context) ([]db.ESME, error)
}

type SMPP interface {
	Sessions(esmeID int64) []SMPPSession
}

type SMPPSession struct {
	BindType string
	Remote   string
	BoundAt  time.Time
}

type noSMPP struct{}

func (noSMPP) Sessions(int64) []SMPPSession { return nil }

type Diameter interface {
	Identity() diameter.Identity
	Peers() []diameter.PeerStatus
	Peer(id string) (diameter.PeerStatus, bool)
	Routes() []Route
}

// RFC 6733 section 2.7
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
	Store          Store
	Diameter       Diameter
	Frontend       fs.FS
	Settings       Settings
	Submitter      *submission.Submitter
	Notify         func()
	Received       *intake.Received
	Ready          func(ctx context.Context) error
	Metrics        prometheus.Gatherer
	Requests       *RequestMetrics
	Auth           AuthStore
	ESMEs          ESMEStore
	OIDC           OIDC
	SMPP           SMPP
	Backups        Backups
	Cluster        Cluster
	JWTSecret      func() []byte
	OIDCCookieKey  func() []byte
	SecureCookie   bool
	BcryptCost     int
	TracerProvider trace.TracerProvider
	Now            func() time.Time
	Logger         *slog.Logger
}

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
		{"GET /api/v1/auth/oidc", public, GetOIDCLogin(cfg)},
		{"GET /api/v1/auth/oidc/login", public, StartOIDCLogin(a)},
		{"GET " + oidc.CallbackPath, public, OIDCCallback(a)},

		{"POST /api/v1/messages", PermCreateMessage, CreateMessage(cfg)},
		{"GET /api/v1/messages", PermReadMessage, ListMessages(cfg)},
		{"GET /api/v1/messages/counts", PermReadMessage, CountMessages(cfg)},
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
		{"GET /api/v1/esmes", PermReadESME, ListESMEs(cfg)},
		{"POST /api/v1/esmes", PermUpdateESME, a.needsLogin(CreateESME(a))},
		{"GET /api/v1/esmes/{system_id}", PermReadESME, GetESME(cfg)},
		{"PUT /api/v1/esmes/{system_id}", PermUpdateESME, a.needsLogin(UpdateESME(a))},
		{"DELETE /api/v1/esmes/{system_id}", PermUpdateESME, a.needsLogin(DeleteESME(a))},
		{"GET /api/v1/operator", PermReadOperator, GetOperator(cfg)},
		{"PUT /api/v1/operator", PermUpdateOperator, UpdateOperator(cfg)},
		{"GET /api/v1/delivery", PermReadDelivery, GetDelivery(cfg)},
		{"PUT /api/v1/delivery", PermUpdateDelivery, UpdateDelivery(cfg)},
		{"GET /api/v1/oidc", PermReadOIDC, a.needsLogin(GetOIDC(cfg))},
		{"PUT /api/v1/oidc", PermUpdateOIDC, a.needsLogin(UpdateOIDC(a))},
		{"POST /api/v1/oidc/discovery", PermUpdateOIDC, a.needsLogin(DiscoverOIDC(cfg))},

		{"GET /api/v1/cluster", PermReadCluster, GetCluster(cfg)},
		{"POST /api/v1/cluster/tokens", PermManageCluster, a.needsLogin(CreateJoinToken(a))},
		{"POST /api/v1/cluster/join", public, JoinCluster(a)},
		{"PUT /api/v1/cluster/members/{node_id}", PermManageCluster, a.needsLogin(UpdateMember(a))},
		{"DELETE /api/v1/cluster/members/{node_id}", PermManageCluster, a.needsLogin(RemoveMember(a))},
		{"POST /api/v1/cluster/members/{node_id}/drain", PermManageCluster, a.needsLogin(DrainMember(a, db.DrainDraining))},
		{"POST /api/v1/cluster/members/{node_id}/resume", PermManageCluster, a.needsLogin(DrainMember(a, db.DrainActive))},

		{"GET /api/v1/backup", PermBackup, Backup(a)},
		{"POST /api/v1/restore", PermBackup, a.needsLogin(Restore(a))},

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
		{"DELETE /api/v1/users/{email}/password", PermManageUsers, a.needsLogin(DeleteUserPassword(a))},
		{"DELETE /api/v1/users/{email}/oidc", PermManageUsers, a.needsLogin(DeleteUserOIDCLink(a))},
		{"GET /api/v1/users/{email}/api-tokens", PermManageUsers, a.needsLogin(ListAPITokens(a, userOfPath(cfg)))},
		{"POST /api/v1/users/{email}/api-tokens", PermManageUsers, a.needsLogin(CreateAPIToken(a, userOfPath(cfg)))},
		{"DELETE /api/v1/users/{email}/api-tokens/{id}", PermManageUsers, a.needsLogin(DeleteAPIToken(a, userOfPath(cfg)))},

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

		mux.Handle(rt.pattern, cfg.Requests.instrument(rt.pattern, h))
	}

	return traced(cfg, securityHeaders(cfg, http.NewCrossOriginProtection().Handler(mux)))
}

var untraced = map[string]bool{"/api/v1/livez": true, "/api/v1/readyz": true, "/api/v1/metrics": true}

func traced(cfg Config, next http.Handler) http.Handler {
	if cfg.TracerProvider == nil {
		return next
	}

	route := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		if i := strings.IndexByte(r.Pattern, '/'); i >= 0 {
			trace.SpanFromContext(r.Context()).SetAttributes(semconv.HTTPRoute(r.Pattern[i:]))
		}
	})

	return otelhttp.NewHandler(route, "",
		otelhttp.WithTracerProvider(cfg.TracerProvider),
		otelhttp.WithPropagators(tracing.Propagator),
		otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, "/api/") && !untraced[r.URL.Path] && !strings.HasPrefix(r.URL.Path, "/api/v1/pprof/")
		}),
	)
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
