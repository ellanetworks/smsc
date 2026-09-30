package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/db"
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
	HSSAvailable() bool
}

type Config struct {
	Store           Store
	Diameter        Diameter
	Frontend        fs.FS
	Notify          func()
	DefaultValidity time.Duration
	Now             func() time.Time
	Logger          *slog.Logger
}

func NewHandler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("POST /api/v1/messages", CreateMessage(cfg))
	mux.Handle("GET /api/v1/messages", ListMessages(cfg))
	mux.Handle("GET /api/v1/messages/{id}", GetMessage(cfg))
	mux.Handle("GET /api/v1/messages/{id}/attempts", ListMessageAttempts(cfg))
	mux.Handle("GET /api/v1/diameter", GetDiameterStatus(cfg))
	mux.Handle("GET /api/v1/openapi.yaml", OpenAPISpec())

	if cfg.Frontend != nil {
		mux.Handle("GET /", Frontend(cfg.Frontend))
	}

	return mux
}
