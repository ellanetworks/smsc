package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func durations(t *testing.T, m *Metrics) *dto.Histogram {
	t.Helper()

	var out dto.Metric
	if err := m.duration.Write(&out); err != nil {
		t.Fatal(err)
	}

	return out.GetHistogram()
}

func TestMetricsTimeDeliveredMessagesOnly(t *testing.T) {
	expiredMessage := pendingMessage(t)
	expiredMessage.ExpiresAt = testNow

	report := pendingMessage(t)
	report.Kind = db.KindStatusReport
	report.TPDU = mustHex(t, "06"+"07"+"0b91"+"5155210300f2"+"62907211950000"+"62907211950000"+"00")

	for _, tt := range []struct {
		name    string
		message db.Message
		answer  *diameter.Message
		want    uint64
	}{
		{"delivered", pendingMessage(t), success(), 1},
		{"status report delivered", report, success(), 0},
		{"expired", expiredMessage, success(), 0},
		{"failed", pendingMessage(t), experimental(tgpp.ResultErrorIllegalUser), 0},
		{"retried", pendingMessage(t), experimental(tgpp.ResultErrorAbsentUser), 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": tt.answer}}
			d := newDeliverer(newFakeStore(tt.message), &fakeRouter{routing: mmeRouting()}, sender)
			d.Metrics = NewMetrics()

			process(t, d)

			if got := durations(t, d.Metrics).GetSampleCount(); got != tt.want {
				t.Errorf("delivery durations observed = %d, want %d", got, tt.want)
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

func (s statusFailingStore) CompleteMessage(context.Context, int64, db.MessageStatus, time.Time, *db.NewStatusReport) error {
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

	if got := durations(t, d.Metrics).GetSampleCount(); got != 0 {
		t.Fatalf("delivery durations observed = %d, want 0: the message is still pending", got)
	}
}

func TestMessageReplacedDuringDeliveryKeepsItsStatus(t *testing.T) {
	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}
	d := newDeliverer(store, &fakeRouter{routing: mmeRouting()}, sender)
	d.Metrics = NewMetrics()

	m, _, _ := d.Store.NextDue(context.Background(), d.Now(), nil)
	store.messages[m.ID].Status = db.StatusReplaced

	if err := d.process(context.Background(), m); err != nil {
		t.Fatalf("process = %v", err)
	}

	if store.messages[m.ID].Status != db.StatusReplaced || len(sender.requests) != 1 {
		t.Fatalf("status = %s, TFRs = %d", store.messages[m.ID].Status, len(sender.requests))
	}

	if got := durations(t, d.Metrics).GetSampleCount(); got != 0 {
		t.Fatalf("delivery durations observed = %d, want 0: the message was replaced", got)
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
}
