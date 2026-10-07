package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
)

type Store interface {
	CreateMessages(ctx context.Context, messages []db.NewMessage) ([]int64, error)
	GetMessage(ctx context.Context, id int64) (db.Message, error)
	ListMessages(ctx context.Context, f db.MessageFilter, page, perPage int) ([]db.Message, int, error)
	ListDeliveryAttempts(ctx context.Context, messageID int64, page, perPage int) ([]db.DeliveryAttempt, int, error)
}

type Diameter interface {
	Identity() diameter.Identity
	Peers() []diameter.PeerStatus
	Routes() []Route
}

// Route is a realm-based routing table entry (RFC 6733, section 2.7) and the
// open peers that can currently serve it.
type Route struct {
	Realm         string
	ApplicationID uint32
	Peers         []string
}

type Settings interface {
	Get() settings.Settings
	UpdateOperator(ctx context.Context, o settings.Operator) error
	UpdateDelivery(ctx context.Context, d settings.Delivery) error
}

type Config struct {
	Store    Store
	Diameter Diameter
	Frontend fs.FS
	Settings Settings
	Notify   func()
	Now      func() time.Time
	Logger   *slog.Logger
}

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("POST /api/v1/messages", CreateMessage(cfg))
	mux.Handle("GET /api/v1/messages", ListMessages(cfg))
	mux.Handle("GET /api/v1/messages/{id}", GetMessage(cfg))
	mux.Handle("GET /api/v1/messages/{id}/attempts", ListMessageAttempts(cfg))
	mux.Handle("GET /api/v1/diameter", GetDiameterStatus(cfg))
	mux.Handle("GET /api/v1/operator", GetOperator(cfg))
	mux.Handle("PUT /api/v1/operator", UpdateOperator(cfg))
	mux.Handle("GET /api/v1/delivery", GetDelivery(cfg))
	mux.Handle("PUT /api/v1/delivery", UpdateDelivery(cfg))
	mux.Handle("GET /api/v1/status", GetStatus(cfg))
	mux.Handle("GET /api/v1/openapi.yaml", OpenAPISpec())

	if cfg.Frontend != nil {
		mux.Handle("GET /", Frontend(cfg.Frontend))
	}

	return mux
}
