package esme

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/logging"
	"github.com/ellanetworks/smsc/internal/logging/logtest"
	"github.com/ellanetworks/smsc/smpp"
)

func TestBindFailuresAtInfo(t *testing.T) {
	env := newTestEnv(t, nil)

	var logs logtest.Recorder

	env.service.Logger = logging.New(&logs, slog.LevelInfo)
	closed := make(chan struct{})

	srv, err := smpp.NewServer(smpp.ServerConfig{
		SystemID: "smsc", Handler: env.service, Authenticate: env.service.Authenticate, OnBound: env.service.OnBound,
		OnDecodeError: env.service.OnDecodeError, OnRefused: env.service.OnRefused, Logger: env.service.Logger,
		OnClosed: func(s *smpp.Session) {
			env.service.OnClosed(s)
			close(closed)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var listener net.ListenConfig

	ln, err := listener.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	t.Cleanup(func() {
		_ = srv.Close()

		if err := <-done; !errors.Is(err, smpp.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := env.service.Wait(ctx); err != nil {
			t.Error(err)
		}
	})

	dialer := net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	encode := func(p smpp.PDU, seq uint32) []byte {
		t.Helper()

		buf, err := smpp.Encode(p, smpp.StatusOK, seq)
		if err != nil {
			t.Fatal(err)
		}

		return buf
	}
	send := func(buf []byte, command smpp.CommandID, status smpp.CommandStatus) {
		t.Helper()

		if _, err := conn.Write(buf); err != nil {
			t.Fatal(err)
		}

		h, _, err := smpp.ReadPDU(conn)
		if err != nil || h.ID != command || h.Status != status {
			t.Fatalf("response = %+v, %v; want %s %s", h, err, command, status)
		}
	}

	bind := &smpp.Bind{ID: smpp.CmdBindTransceiver, SystemID: env.esme.SystemID, Password: "badpass", InterfaceVersion: 0x34}
	send(encode(bind, 1), smpp.CmdBindTransceiverResp, smpp.StatusBindFail)

	for seq := uint32(2); seq <= 3; seq++ {
		buf := encode(bind, seq)[:smpp.HeaderLength+len(bind.SystemID)+1+len(bind.Password)]
		binary.BigEndian.PutUint32(buf, uint32(len(buf)))
		send(buf, smpp.CmdGenericNack, smpp.StatusInvCmdLen)
	}

	bind.Password = "secret"
	buf := encode(bind, 4)
	binary.BigEndian.PutUint32(buf[12:], 0)
	send(buf, smpp.CmdGenericNack, smpp.StatusUnknownErr)
	send(encode(bind, 5), smpp.CmdBindTransceiverResp, smpp.StatusOK)
	send(encode(bind, 6), smpp.CmdBindTransceiverResp, smpp.StatusAlyBnd)
	send(encode(&smpp.Unbind{}, 7), smpp.CmdUnbindResp, smpp.StatusOK)

	_ = conn.Close()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("SMPP session did not close")
	}

	want := []struct{ reason, status string }{
		{"wrong password", smpp.StatusBindFail.String()},
		{"malformed bind", smpp.StatusInvCmdLen.String()},
		{"invalid sequence number", smpp.StatusUnknownErr.String()},
		{"session already bound", smpp.StatusAlyBnd.String()},
	}

	records := logs.Messages(t, "SMPP bind refused")
	if len(records) != len(want) {
		t.Fatalf("bind rejection logs = %v; want %d records", records, len(want))
	}

	for i, rec := range records {
		if rec["level"] != "info" || rec["reason"] != want[i].reason || rec["status"] != want[i].status ||
			rec["bind"] != "bind_transceiver" || rec["remote"] != conn.LocalAddr().String() {
			t.Errorf("bind rejection %d = %v", i, rec)
		}

		for _, v := range rec {
			if text, ok := v.(string); ok && (strings.Contains(text, "badpass") || strings.Contains(text, "secret")) {
				t.Fatal("bind rejection log contains a password")
			}
		}
	}

	if records[0]["system_id"] != env.esme.SystemID || records[1]["field"] != "password" {
		t.Errorf("missing bind diagnostic fields: %v", records)
	}

	if recs := logs.Messages(t, "SMPP session closed"); len(recs) != 1 || recs[0]["level"] != "info" ||
		recs[0]["system_id"] != env.esme.SystemID || recs[0]["reason"] != smpp.ErrUnbound.Error() {
		t.Errorf("clean unbind log = %v", recs)
	}
}

type sessionSyncStore struct {
	Store
	err error
}

func (s *sessionSyncStore) NodeSMPPSessions(context.Context, string) ([]uint64, error) {
	return nil, s.err
}

func TestSessionReconciliationLogsAtInfo(t *testing.T) {
	var logs logtest.Recorder

	store := &sessionSyncStore{err: errors.New("store unavailable")}
	s := &Service{Store: store, Logger: logging.New(&logs, slog.LevelInfo)}
	s.init()

	ctx := t.Context()

	s.syncSessions(ctx)
	s.syncSessions(ctx)

	if s.synced {
		t.Fatal("session reconciliation marked as synced after failure")
	}

	store.err = nil

	s.syncSessions(ctx)

	if !s.synced {
		t.Fatal("successful session reconciliation did not unblock binds")
	}

	store.err = errors.New("store unavailable")

	for range 4 {
		s.syncSessions(ctx)
	}

	store.err = nil

	s.syncSessions(ctx)

	warnings := logs.Messages(t, "SMPP session reconciliation failed")
	if len(warnings) != 1 || warnings[0]["level"] != "warn" || warnings[0]["blocks_binds"] != true ||
		warnings[0]["operation"] != "read" {
		t.Errorf("reconciliation warnings = %v", warnings)
	}

	if recs := logs.Messages(t, "SMPP session reconciliation recovered"); len(recs) != 1 || recs[0]["level"] != "info" {
		t.Errorf("reconciliation recoveries = %v", recs)
	}
}
