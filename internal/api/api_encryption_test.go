package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/cluster"
	"github.com/ellanetworks/smsc/internal/db"
)

type fakeEncryption struct {
	err error
}

func (f *fakeEncryption) RotateKeys(context.Context) (cluster.EncryptionStatus, error) {
	if f.err != nil {
		return cluster.EncryptionStatus{}, f.err
	}

	return cluster.EncryptionStatus{Initialized: true, ActiveTerm: 2}, nil
}

func encryptionHandler(t *testing.T, err error) http.Handler {
	t.Helper()

	cfg := testConfig(t, openStore(t))
	cfg.Encryption = &fakeEncryption{err: err}

	return api.NewHandler(cfg)
}

func TestRotateEncryptionKeys(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"rotated":    {nil, http.StatusOK},
		"key change": {db.ErrKeyChange, http.StatusConflict},
		"raced":      {db.ErrKeysChanged, http.StatusConflict},
		"locked":     {cluster.ErrEncryptionLocked, http.StatusServiceUnavailable},
		"follower":   {&cluster.LeaderError{NodeID: "leader", APIAddress: "https://leader:5010"}, http.StatusMisdirectedRequest},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			h := encryptionHandler(t, tt.err)
			token := initialize(t, h)

			rec := serve(h, request(http.MethodPost, "/api/v1/encryption/rotate", ``, token))
			if rec.Code != tt.want {
				t.Fatalf("rotate = %d %s, want %d", rec.Code, rec.Body.String(), tt.want)
			}

			if tt.err == nil && !json.Valid(rec.Body.Bytes()) {
				t.Fatal("no JSON body")
			}
		})
	}
}
