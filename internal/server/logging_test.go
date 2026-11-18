package server

import (
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/logging"
	"github.com/ellanetworks/smsc/internal/logging/logtest"
	"github.com/ellanetworks/smsc/smpp"
)

type listenerLog struct {
	logtest.Recorder
	written chan struct{}
}

func (l *listenerLog) Write(p []byte) (int, error) {
	n, err := l.Recorder.Write(p)
	select {
	case l.written <- struct{}{}:
	default:
	}
	return n, err
}

func TestListenerExitLogs(t *testing.T) {
	logs := &listenerLog{written: make(chan struct{}, 1)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := &Server{Logger: logging.New(logs, slog.LevelInfo), apiServer: &http.Server{}, apiListener: ln}
	s.reportServeExit("API", ln.Addr().String(), http.ErrServerClosed, http.ErrServerClosed)
	s.reportServeExit("SMPP", ln.Addr().String(), smpp.ErrServerClosed, smpp.ErrServerClosed)
	if len(logs.Records(t)) != 0 {
		t.Fatal("expected listener shutdown reported as a failure")
	}

	s.serveAPI()
	_ = ln.Close()
	select {
	case <-logs.written:
	case <-time.After(5 * time.Second):
		t.Fatal("listener exit was not reported")
	}
	if recs := logs.Messages(t, "listener stopped unexpectedly"); len(recs) != 1 || recs[0]["level"] != "error" ||
		recs[0]["listener"] != "API" {
		t.Errorf("unexpected listener exit logs = %v", recs)
	}
}
