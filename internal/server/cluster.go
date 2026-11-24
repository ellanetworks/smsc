package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"

	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/encryption"
	"github.com/ellanetworks/smsc/internal/oidc"
	"github.com/ellanetworks/smsc/smpp"
)

type secrets struct {
	d       *db.DB
	current atomic.Pointer[db.Secrets]
}

func newSecrets(ctx context.Context, d *db.DB, logger *slog.Logger) (*secrets, error) {
	s := &secrets{d: d}

	v, err := d.Secrets(ctx)

	switch {
	case errors.Is(err, encryption.ErrLocked):
		logger.WarnContext(ctx, "the secrets stay unavailable until this node unlocks its encryption keys")
	case errors.Is(err, encryption.ErrAuthentication), errors.Is(err, encryption.ErrMalformed),
		errors.Is(err, encryption.ErrNotEncrypted):
		logger.ErrorContext(ctx, "a cluster secret cannot be decrypted: logins fail until the encryption keys are "+
			"rotated, which replaces it", slog.Any("error", err))
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	default:
		s.current.Store(&v)
	}

	return s, nil
}

func (s *secrets) reload(ctx context.Context, replaced bool) error {
	v, err := s.d.Secrets(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		if replaced || errors.Is(err, encryption.ErrLocked) {
			s.current.Store(nil)
		}

		return err
	}

	s.current.Store(&v)

	return nil
}

func (s *secrets) get() *db.Secrets {
	if v := s.current.Load(); v != nil {
		return v
	}

	if err := s.reload(context.Background(), true); err != nil {
		return nil
	}

	return s.current.Load()
}

func (s *secrets) jwt() []byte {
	if v := s.get(); v != nil {
		return v.JWT
	}

	return nil
}

func (s *secrets) cookieKey() []byte {
	if v := s.get(); v != nil {
		return v.OIDCCookieKey
	}

	return nil
}

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

func apiAddress(cfg config.Config, listening net.Addr) string {
	if cfg.Cluster == nil {
		return ""
	}

	scheme := "http"
	if cfg.API.TLS != nil {
		scheme = "https"
	}

	host := cfg.API.Address.Unmap().String()

	if !cfg.API.Address.IsValid() || cfg.API.Address.IsUnspecified() {
		h, _, err := net.SplitHostPort(cfg.Cluster.AdvertiseAddress())
		if err != nil {
			return ""
		}

		host = h
	}

	_, port, err := net.SplitHostPort(listening.String())
	if err != nil {
		return ""
	}

	return scheme + "://" + net.JoinHostPort(host, port)
}

func encryptionConfig(e config.Encryption) cluster.EncryptionConfig {
	out := cluster.EncryptionConfig{Key: keySource(e.Key)}

	if p := e.PreviousKey; p != nil {
		previous := keySource(*p)
		out.Previous = &previous
	}

	return out
}

func keySource(k config.EncryptionKey) cluster.KeySource {
	out := cluster.KeySource{Type: k.Type, Path: k.File.Path}

	if p := k.PKCS11; p != nil {
		out.PKCS11 = &encryption.PKCS11Config{
			Library: p.Library, TokenLabel: p.TokenLabel, Slot: p.Slot, PINFile: p.PINFile, KeyLabel: p.KeyLabel,
			Mechanism: p.Mechanism,
		}
	}

	return out
}

type reloader struct {
	d          *db.DB
	secrets    *secrets
	oidc       *oidc.Live
	logger     *slog.Logger
	keys       <-chan struct{}
	wake       chan struct{}
	secretsDue atomic.Bool
	oidcDue    atomic.Bool
	replaced   atomic.Bool
}

func newReloader(d *db.DB, s *secrets, o *oidc.Live, keys <-chan struct{}, logger *slog.Logger) *reloader {
	return &reloader{d: d, secrets: s, oidc: o, keys: keys, logger: logger, wake: make(chan struct{}, 1)}
}

func (r *reloader) request(secrets, oidc, replaced bool) {
	if !secrets && !oidc {
		return
	}

	if secrets {
		r.secretsDue.Store(true)
	}

	if oidc {
		r.oidcDue.Store(true)
	}

	if replaced {
		r.replaced.Store(true)
	}

	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *reloader) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.keys:
			r.request(true, true, false)
			continue
		case <-r.wake:
		}

		replaced := r.replaced.Swap(false)

		if r.secretsDue.Swap(false) {
			if err := r.secrets.reload(ctx, replaced); err != nil && !errors.Is(err, encryption.ErrLocked) {
				r.logger.ErrorContext(ctx, "failed to reload the secrets", slog.Any("error", err))
			}
		}

		if r.oidcDue.Swap(false) {
			current, err := r.d.GetOIDC(ctx)
			if err != nil {
				r.logger.ErrorContext(ctx, "failed to reload the OIDC settings", slog.Any("error", err))
			} else {
				r.oidc.Reload(current)
			}
		}
	}
}
