//go:build pysim

package smpp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	pysimKIc = "F09C43EE1A0391665CC9F05AF4E0BD10"
	pysimKID = "01981F4A20999F62AF99988107BAF6CA"
	pysimPoR = "027100002c12b000107ddf58d1780f771638b3975759f4296cf5c31efc87a16a1b61921426baa16da1b5ba1a9951d59a39"
	pysimOut = "0000ffff7f2002000000000009b106350400838a838a 9000"
)

func TestPySimOTATool(t *testing.T) {
	dir := os.Getenv("PYSIM_DIR")
	if dir == "" {
		t.Skip("PYSIM_DIR is not set")
	}

	python := os.Getenv("PYSIM_PYTHON")
	if python == "" {
		python = "python3"
		if venv := filepath.Join(dir, "venv", "bin", "python"); fileExists(venv) {
			python = venv
		}
	}

	por, err := hex.DecodeString(pysimPoR)
	if err != nil {
		t.Fatal(err)
	}

	submitted := make(chan *SubmitSM, 1)
	unbound := make(chan error, 1)

	ts := newTestServer(t, func(cfg *ServerConfig) {
		cfg.Authenticate = func(_ context.Context, req BindRequest) BindOutcome {
			if req.Bind.ID != CmdBindTransceiver || req.Bind.SystemID != "test" || req.Bind.Password != "test" {
				return BindOutcome{Status: StatusBindFail}
			}

			return BindOutcome{Status: StatusOK}
		}
		cfg.Handler = HandlerFunc(func(_ context.Context, s *Session, req Request) Response {
			m := req.PDU.(*SubmitSM)
			submitted <- m

			return Response{PDU: &SubmitSMResp{MessageID: "1"}, Written: func(err error) {
				if err != nil {
					return
				}

				go func() {
					d := &DeliverSM{Message: Message{
						Source: m.Destination, Destination: m.Source, ESMClass: 0x50, ProtocolID: m.ProtocolID,
						DataCoding: m.DataCoding, ShortMessage: por,
					}}
					d.Options.ReceiptedMessageID = new("1")

					if err := s.DeliverSM(context.Background(), d); err != nil {
						t.Errorf("Deliver: %v", err)
					}
				}()
			}}
		})
		cfg.OnClosed = func(s *Session) { unbound <- s.Err() }
	})
	_, port, _ := net.SplitHostPort(ts.addr)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, filepath.Join(dir, "contrib", "smpp-ota-tool.py"),
		"--host", "127.0.0.1", "--port", port, "--kic", pysimKIc, "--kid", pysimKID, "--tar", "B00010",
		"--src-addr", "15550001000", "--dest-addr", "15551230002", "--timeout", "10",
		"--apdu", "A0A40000027F20", "--apdu", "A0C0000016")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PYTHONPATH="+dir)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("smpp-ota-tool.py: %v\n%s", err, stderr.String())
	}

	if got := strings.TrimSpace(string(out)); got != pysimOut {
		t.Fatalf("smpp-ota-tool.py printed %q, want %q\n%s", got, pysimOut, stderr.String())
	}

	m := <-submitted
	if m.Source != (Address{TON: 1, Addr: "15550001000"}) || m.Destination.TON != 1 || m.Destination.Addr != "15551230002" ||
		m.ESMClass != 0x40 || m.ProtocolID != 0x7f || m.DataCoding != 0x02 || m.RegisteredDelivery != 0 ||
		!bytes.HasPrefix(m.ShortMessage, []byte{2, 0x70, 0}) {
		t.Fatalf("submit_sm %+v", m)
	}

	select {
	case err := <-unbound:
		if !errors.Is(err, ErrUnbound) {
			t.Fatalf("session closed for %v, want ErrUnbound", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not close after the tool exited")
	}

	t.Logf("pySim log:\n%s", stderr.String())
}

func fileExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}
