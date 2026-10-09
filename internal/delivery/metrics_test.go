package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func completed(t *testing.T, m *Metrics, status db.MessageStatus) float64 {
	t.Helper()

	return testutil.ToFloat64(m.completed.WithLabelValues(string(status)))
}

func durations(t *testing.T, m *Metrics) *dto.Histogram {
	t.Helper()

	var out dto.Metric
	if err := m.duration.Write(&out); err != nil {
		t.Fatal(err)
	}

	return out.GetHistogram()
}

func TestMetricsCountFinalStatuses(t *testing.T) {
	expiredMessage := pendingMessage(t)
	expiredMessage.ExpiresAt = testNow

	for _, tt := range []struct {
		name    string
		message db.Message
		routing s6c.Routing
		answer  *diameter.Message
		want    db.MessageStatus
	}{
		{"delivered", pendingMessage(t), mmeRouting(), success(), db.StatusDelivered},
		{"expired", expiredMessage, mmeRouting(), success(), db.StatusExpired},
		{"failed", pendingMessage(t), mmeRouting(), experimental(tgpp.ResultErrorIllegalUser), db.StatusFailed},
		{"retried", pendingMessage(t), mmeRouting(), experimental(tgpp.ResultErrorAbsentUser), db.StatusPending},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": tt.answer}}
			d := newDeliverer(newFakeStore(tt.message), &fakeRouter{routing: tt.routing}, sender)
			d.Metrics = NewMetrics()

			process(t, d)

			for _, status := range finalStatuses {
				want := 0.0
				if status == tt.want {
					want = 1
				}

				if got := completed(t, d.Metrics, status); got != want {
					t.Errorf("completed{status=%q} = %v, want %v", status, got, want)
				}
			}

			wantDelivered := uint64(0)
			if tt.want == db.StatusDelivered {
				wantDelivered = 1
			}

			if got := durations(t, d.Metrics).GetSampleCount(); got != wantDelivered {
				t.Errorf("delivery durations observed = %d, want %d", got, wantDelivered)
			}
		})
	}
}

func TestMetricsTimeDeliveryFromSubmission(t *testing.T) {
	m := pendingMessage(t)
	m.SubmittedAt = testNow.Add(-90 * time.Second)

	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}
	d := newDeliverer(newFakeStore(m), &fakeRouter{routing: mmeRouting()}, sender)
	d.Metrics = NewMetrics()

	process(t, d)

	if got := durations(t, d.Metrics).GetSampleSum(); got != 90 {
		t.Fatalf("delivery duration = %vs, want 90s", got)
	}
}

// statusFailingStore fails to store a final status, which leaves the message pending.
type statusFailingStore struct {
	*fakeStore
}

func (s statusFailingStore) SetMessageStatus(context.Context, int64, db.MessageStatus, time.Time) error {
	return errors.New("disk I/O error")
}

func TestMetricsDoNotCountUnstoredStatus(t *testing.T) {
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}
	d := newDeliverer(statusFailingStore{newFakeStore(pendingMessage(t))}, &fakeRouter{routing: mmeRouting()}, sender)
	d.Metrics = NewMetrics()

	m, _, _ := d.Store.NextDue(context.Background(), d.Now(), nil)
	if err := d.process(context.Background(), m); err == nil {
		t.Fatal("process succeeded although the status was not stored")
	}

	if got := completed(t, d.Metrics, db.StatusDelivered); got != 0 {
		t.Fatalf("completed{status=delivered} = %v, want 0: the message is still pending", got)
	}

	if got := durations(t, d.Metrics).GetSampleCount(); got != 0 {
		t.Fatalf("delivery durations observed = %d, want 0", got)
	}
}

func TestMetricsLint(t *testing.T) {
	m := NewMetrics()

	for _, c := range m.Collectors() {
		problems, err := testutil.CollectAndLint(c)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range problems {
			t.Errorf("lint: %s: %s", p.Metric, p.Text)
		}
	}

	if n := testutil.CollectAndCount(m.completed); n != len(finalStatuses) {
		t.Errorf("completed has %d series, want %d", n, len(finalStatuses))
	}
}
