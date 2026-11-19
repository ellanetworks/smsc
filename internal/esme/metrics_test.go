package esme

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/smpp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

type pendingCounterFunc func(ctx context.Context, now time.Time) (map[db.NotificationType]int, error)

func (f pendingCounterFunc) CountPendingNotifications(ctx context.Context, now time.Time) (map[db.NotificationType]int, error) {
	return f(ctx, now)
}

func TestPendingCollector(t *testing.T) {
	failure := errors.New("database is locked")

	m := NewMetrics(pendingCounterFunc(func(ctx context.Context, _ time.Time) (map[db.NotificationType]int, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the count has no deadline")
		}

		return nil, failure
	}), nil)

	reg := prometheus.NewRegistry()
	reg.MustRegister(m.pending)

	if _, err := reg.Gather(); !errors.Is(err, failure) {
		t.Fatalf("Gather = %v, want %v", err, failure)
	}
}

func TestDeliverOutcome(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status smpp.CommandStatus
		err    error
		want   deliverResult
	}{
		{"acknowledgment", smpp.StatusOK, nil, deliverAcknowledged},
		{"permanent application error", smpp.StatusXPAppn, nil, deliverRejected},
		{"application rejection", smpp.StatusXRAppn, nil, deliverRejected},
		{"temporary application error", smpp.StatusXTAppn, nil, deliverRefused},
		{"system error response", smpp.StatusSysErr, nil, deliverRefused},
		{"vendor response", 0x400, nil, deliverRefused},
		{"response timer", 0, fmt.Errorf("deliver_sm: %w", smpp.ErrResponseTimeout), deliverTimeout},
		{"deadline", 0, context.DeadlineExceeded, deliverTimeout},
		{"generic nack", 0, &smpp.StatusError{Command: smpp.CmdGenericNack, Status: smpp.StatusInvCmdID}, deliverNack},
		{"closed", 0, smpp.ErrClosed, deliverClosed},
		{"no longer bound", 0, &smpp.StateError{Command: smpp.CmdDeliverSM, State: smpp.StateOpen}, deliverClosed},
		{"canceled", 0, context.Canceled, deliverCanceled},
		{"other error", 0, io.ErrUnexpectedEOF, deliverError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := deliverOutcome(tt.status, tt.err); got != tt.want {
				t.Fatalf("deliverOutcome = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeliverMetricsRegistered(t *testing.T) {
	m := NewMetrics(nil, func() bool { return false })
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(m.Collectors()...)
	if n, err := testutil.GatherAndCount(reg, "ellasmsc_smpp_deliver_attempts_total"); err != nil || n != 24 {
		t.Errorf("deliver attempt series = %d, %v; want 24 series", n, err)
	}
	if n, err := testutil.GatherAndCount(reg, "ellasmsc_smpp_deliver_duration_seconds"); err != nil || n != 3 {
		t.Errorf("deliver duration series = %d, %v; want 3 series", n, err)
	}
}

func TestDeliverMetricsRetriesAndFinalOutcome(t *testing.T) {
	env := newTestEnv(t, nil)
	s := env.service
	c := &clock{now: testNow}
	s.Now = c.Now
	s.Metrics = NewMetrics(env.store, nil)
	m := s.Metrics
	ctx := t.Context()
	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(msg *smpp.Message) { msg.RegisteredDelivery = 0x01 }))
	env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())

	s.dispatch(ctx)
	checkDeliverDuration(t, m, db.NotificationReceipt, 0, 0)

	calls := 0
	env.bindAnswering(1, Receiver, env.esme, func(*smpp.DeliverSM) (smpp.CommandStatus, error) {
		calls++
		if calls < 3 {
			c.advance(30 * time.Second)
			return 0, smpp.ErrResponseTimeout
		}

		c.advance(500 * time.Millisecond)
		return smpp.StatusOK, nil
	})

	for i := range 3 {
		s.dispatch(ctx)
		s.sends.Wait()
		if calls != i+1 {
			t.Fatalf("deliver calls = %d, want %d", calls, i+1)
		}

		if i < 2 {
			if got := testutil.ToFloat64(m.sent.WithLabelValues("receipt", "acknowledged")); got != 0 {
				t.Fatalf("final acknowledgments after timeout = %v, want 0", got)
			}
			c.advance(retryDelay(i))
		}
	}

	checkDeliverAttempts(t, m, db.NotificationReceipt, deliverTimeout, 2)
	checkDeliverAttempts(t, m, db.NotificationReceipt, deliverAcknowledged, 1)
	checkDeliverDuration(t, m, db.NotificationReceipt, 3, 60.5)
	if got := testutil.ToFloat64(m.sent.WithLabelValues("receipt", "acknowledged")); got != 1 {
		t.Errorf("final acknowledgments = %v, want 1", got)
	}
}

type notificationCloseStore struct {
	Store
	err error
}

func (s notificationCloseStore) CloseNotification(context.Context, int64, db.NotificationResult) error {
	return s.err
}

func TestDeliverMetricsSeparateFromNotificationPersistence(t *testing.T) {
	m := NewMetrics(nil, func() bool { return false })
	s := &Service{
		Store: notificationCloseStore{err: errors.New("store unavailable")}, Metrics: m,
		Logger: slog.New(slog.DiscardHandler),
	}
	s.init()
	conn := newFakeConn()
	sess := &Session{conn: conn}

	s.send(t.Context(), sess, db.Notification{Type: db.NotificationSIMAnswer})
	if n := len(conn.got); n != 0 {
		t.Fatalf("unbuildable notification produced %d deliver_sm calls", n)
	}
	checkDeliverAttempts(t, m, db.NotificationSIMAnswer, deliverAcknowledged, 0)
	checkDeliverDuration(t, m, db.NotificationSIMAnswer, 0, 0)

	s.send(t.Context(), sess, db.Notification{Type: db.NotificationReceipt})
	checkDeliverAttempts(t, m, db.NotificationReceipt, deliverAcknowledged, 1)
	if got := testutil.ToFloat64(m.sent.WithLabelValues("receipt", "acknowledged")); got != 0 {
		t.Errorf("final acknowledgments after failed persistence = %v, want 0", got)
	}
}

func checkDeliverAttempts(t *testing.T, m *Metrics, typ db.NotificationType, result deliverResult, want float64) {
	t.Helper()

	if got := testutil.ToFloat64(m.deliverAttempts.WithLabelValues(string(typ), string(result))); got != want {
		t.Errorf("deliver attempts (%s, %s) = %v, want %v", typ, result, got, want)
	}
}

func checkDeliverDuration(t *testing.T, m *Metrics, typ db.NotificationType, count uint64, seconds float64) {
	t.Helper()

	var sample dto.Metric
	observer := m.deliverDuration.WithLabelValues(string(typ))
	if err := observer.(prometheus.Metric).Write(&sample); err != nil {
		t.Fatal(err)
	}
	if h := sample.GetHistogram(); h.GetSampleCount() != count || h.GetSampleSum() != seconds {
		t.Errorf("deliver duration (%s) has count %d and sum %v, want %d and %v", typ, h.GetSampleCount(), h.GetSampleSum(), count, seconds)
	}
}
