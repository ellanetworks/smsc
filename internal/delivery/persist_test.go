package delivery

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
)

type busyStore struct {
	*db.DB
	mu            sync.Mutex
	completeFails int
	completeCalls int
	retryFails    int
	retryCalls    int
	onFail        func(call int)
}

func (s *busyStore) fail(calls *int, fails int) bool {
	s.mu.Lock()
	*calls++
	call, fail, onFail := *calls, *calls <= fails, s.onFail
	s.mu.Unlock()

	if fail && onFail != nil {
		onFail(call)
	}

	return fail
}

func (s *busyStore) CompleteMessage(ctx context.Context, id int64, status db.MessageStatus, at time.Time,
	report *db.NewStatusReport, tpStatus uint8, f db.Fence, attempts ...db.DeliveryAttempt,
) error {
	if s.fail(&s.completeCalls, s.completeFails) {
		return db.ErrBusy
	}

	return s.DB.CompleteMessage(ctx, id, status, at, report, tpStatus, f, attempts...)
}

func (s *busyStore) ScheduleRetry(ctx context.Context, id int64, at, now time.Time, hold bool, since time.Time, f db.Fence,
	attempts ...db.DeliveryAttempt,
) error {
	if s.fail(&s.retryCalls, s.retryFails) {
		return db.ErrBusy
	}

	return s.DB.ScheduleRetry(ctx, id, at, now, hold, since, f, attempts...)
}

func (s *busyStore) calls() (complete, retry int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.completeCalls, s.retryCalls
}

type busyEnv struct {
	store  *busyStore
	sender *fakeSender
	router *fakeRouter
	d      *Deliverer
	id     int64
}

func newBusyEnv(t *testing.T, answer *diameter.Message) *busyEnv {
	t.Helper()

	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = database.Close() })

	e := &busyEnv{
		store:  &busyStore{DB: database},
		sender: &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": answer}},
		router: &fakeRouter{routing: mmeRouting()},
	}

	e.d = newDeliverer(e.store, e.router, e.sender)
	e.d.Now = time.Now

	e.id, err = database.CreateMessage(t.Context(), db.NewMessage{
		Originator:  db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:   db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:      "15551230002",
		Origin:      db.OriginMobile,
		TPDU:        mustHex(t, submitTPDU),
		SubmittedAt: time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	return e
}

func (e *busyEnv) runUntil(t *testing.T, done func(db.Message) bool) db.Message {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})

	go func() {
		e.d.Run(ctx, 1)
		_ = e.d.Wait(context.Background())

		close(stopped)
	}()

	defer func() {
		cancel()
		<-stopped
	}()

	e.d.Notify()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m := e.message(t); done(m) {
			return m
		}
	}

	t.Fatal("the delivery outcome was never recorded")

	return db.Message{}
}

func (e *busyEnv) message(t *testing.T) db.Message {
	t.Helper()

	m, err := e.store.GetMessage(context.Background(), e.id)
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func (e *busyEnv) attempts(t *testing.T) map[db.AttemptStep]int {
	t.Helper()

	attempts, _, err := e.store.ListDeliveryAttempts(context.Background(), e.id, 1, 100)
	if err != nil {
		t.Fatal(err)
	}

	steps := map[db.AttemptStep]int{}
	for _, a := range attempts {
		steps[a.Step]++
	}

	return steps
}

func (e *busyEnv) sends() (sri, forwards int) {
	e.router.mu.Lock()
	sri = len(e.router.requests)
	e.router.mu.Unlock()

	e.sender.mu.Lock()
	defer e.sender.mu.Unlock()

	return sri, len(e.sender.requests)
}

func TestBusyCompletionIsRetriedWithoutResending(t *testing.T) {
	e := newBusyEnv(t, success())
	e.store.completeFails = 3

	m := e.runUntil(t, func(m db.Message) bool { return m.Status != db.StatusPending })

	if m.Status != db.StatusDelivered {
		t.Fatalf("status = %s, want delivered", m.Status)
	}

	if complete, _ := e.store.calls(); complete != 4 {
		t.Fatalf("CompleteMessage calls = %d, want 4", complete)
	}

	if sri, forwards := e.sends(); sri != 1 || forwards != 1 {
		t.Fatalf("SRIs = %d, MT-Forwards = %d, want 1 and 1", sri, forwards)
	}

	if steps := e.attempts(t); steps[db.StepRouting] != 1 || steps[db.StepDelivery] != 1 {
		t.Fatalf("delivery attempts = %v, want one routing and one delivery", steps)
	}
}

func TestBusyRetryScheduleIsRetriedWithoutResending(t *testing.T) {
	e := newBusyEnv(t, experimental(tgpp.ResultErrorUserBusyForMTSMS))
	e.store.retryFails = 3

	start := time.Now()
	m := e.runUntil(t, func(m db.Message) bool { return m.Retries > 0 })

	if m.Status != db.StatusPending || m.NextAttemptAt.Before(start.Add(time.Minute)) {
		t.Fatalf("status = %s, next attempt in %v, want pending and a minute out", m.Status, m.NextAttemptAt.Sub(start))
	}

	if _, retry := e.store.calls(); retry != 4 {
		t.Fatalf("ScheduleRetry calls = %d, want 4", retry)
	}

	if sri, forwards := e.sends(); sri != 1 || forwards != 1 {
		t.Fatalf("SRIs = %d, MT-Forwards = %d, want 1 and 1", sri, forwards)
	}

	if steps := e.attempts(t); steps[db.StepRouting] != 1 || steps[db.StepDelivery] != 1 {
		t.Fatalf("delivery attempts = %v, want one routing and one delivery", steps)
	}
}

func TestBusyRetryScheduleStopsOnStaleTerm(t *testing.T) {
	e := newBusyEnv(t, experimental(tgpp.ResultErrorUserBusyForMTSMS))
	e.store.retryFails = 2
	e.store.onFail = func(call int) {
		if call == 2 {
			if _, err := e.store.ClaimRecipient(context.Background(), "15551230002", 2); err != nil {
				t.Error(err)
			}
		}
	}

	m, ok, err := e.store.NextDue(t.Context(), "15551230002", time.Now())
	if err != nil || !ok {
		t.Fatalf("next due = %v, %v", ok, err)
	}

	if err := e.d.process(t.Context(), 1, m); err != nil {
		t.Fatalf("process = %v", err)
	}

	if _, retry := e.store.calls(); retry != 3 {
		t.Fatalf("ScheduleRetry calls = %d, want 3", retry)
	}

	if m := e.message(t); m.Status != db.StatusPending || m.Retries != 0 {
		t.Fatalf("status = %s, retries = %d, want pending and untouched for the newer term", m.Status, m.Retries)
	}

	if _, forwards := e.sends(); forwards != 1 {
		t.Fatalf("MT-Forwards = %d, want 1", forwards)
	}

	if steps := e.attempts(t); steps[db.StepRouting] != 1 || steps[db.StepDelivery] != 1 {
		t.Fatalf("delivery attempts = %v, want one routing and one delivery", steps)
	}
}

func TestBusyCompletionStopsWhenLeadershipEnds(t *testing.T) {
	e := newBusyEnv(t, success())
	e.store.completeFails = 1 << 30

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	e.store.onFail = func(call int) {
		if call == 3 {
			cancel()
		}
	}

	m, ok, err := e.store.NextDue(t.Context(), "15551230002", time.Now())
	if err != nil || !ok {
		t.Fatalf("next due = %v, %v", ok, err)
	}

	if err := e.d.process(ctx, 1, m); err == nil {
		t.Fatal("process succeeded although the outcome was never recorded")
	}

	if complete, _ := e.store.calls(); complete != 3 {
		t.Fatalf("CompleteMessage calls = %d, want 3", complete)
	}

	if _, forwards := e.sends(); forwards != 1 {
		t.Fatalf("MT-Forwards = %d, want 1", forwards)
	}
}
