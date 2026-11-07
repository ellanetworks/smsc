package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/smpp"
)

type secrets struct {
	current atomic.Pointer[db.Secrets]
}

func newSecrets(ctx context.Context, d *db.DB) (*secrets, error) {
	v, err := d.Secrets(ctx)
	if err != nil {
		return nil, err
	}

	s := &secrets{}
	s.current.Store(&v)

	return s, nil
}

func (s *secrets) reload(ctx context.Context, d *db.DB) error {
	v, err := d.ReadSecrets(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return err
	}

	s.current.Store(&v)

	return nil
}

func (s *secrets) jwt() []byte { return s.current.Load().JWT }

func (s *secrets) cookieKey() []byte { return s.current.Load().OIDCCookieKey }

func (s *Server) fenceOnRemoval(ctx context.Context, node *cluster.Node, nodes *nodeManager, smppServer *smpp.Server) {
	select {
	case <-ctx.Done():
		return
	case <-node.Removed():
	}

	s.Logger.WarnContext(ctx, "removed from the cluster: closing the Diameter and SMPP edges")

	if smppServer != nil {
		if err := smppServer.Shutdown(ctx); err != nil {
			s.Logger.WarnContext(ctx, "SMPP sessions closed without unbind", slog.Any("error", err))
		}
	}

	nodes.close(ctx)
}
