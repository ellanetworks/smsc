package delivery

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/s6c"
	"github.com/ellanetworks/smsc/internal/sgd"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

var testNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

const submitTPDU = "11" + "07" + "0b91" + "5155210300f2" + "00" + "00" + "aa" + "05" + "e8329bfd06"

type fakeStore struct {
	mu           sync.Mutex
	messages     map[int64]*db.Message
	attempts     []attempt
	pending      int
	holds        []hold
	alerts       []string
	alertMSISDNs map[string]string
	recipients   []string
}

type attempt struct {
	messageID int64
	node      string
	code      uint32
}

type hold struct {
	msisdn string
	until  time.Time
}

func newFakeStore(m db.Message) *fakeStore {
	return &fakeStore{messages: map[int64]*db.Message{m.ID: &m}, alertMSISDNs: map[string]string{}}
}

func (s *fakeStore) NextDue(_ context.Context, now time.Time, busy []string) (db.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range s.messages {
		if m.Status == db.StatusPending && !m.NextAttemptAt.After(now) && !slices.Contains(busy, m.MSISDN) {
			return *m, true, nil
		}
	}

	return db.Message{}, false, nil
}

func (s *fakeStore) NextWakeup(context.Context, []string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *fakeStore) SetMessageStatus(_ context.Context, id int64, status db.MessageStatus, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages[id].Status = status

	return nil
}

func (s *fakeStore) ScheduleRetry(_ context.Context, id int64, at, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages[id].NextAttemptAt = at
	s.messages[id].Retries++

	return nil
}

func (s *fakeStore) HoldRecipient(_ context.Context, msisdn string, until, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.holds = append(s.holds, hold{msisdn, until})

	return nil
}

func (s *fakeStore) AlertRecipient(_ context.Context, msisdn string, _ time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.alerts = append(s.alerts, msisdn)

	if s.recipients != nil {
		return s.recipients, nil
	}

	return []string{msisdn}, nil
}

func (s *fakeStore) SetAlertMSISDN(_ context.Context, msisdn, alertMSISDN string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.alertMSISDNs[msisdn] = alertMSISDN

	return nil
}

func (s *fakeStore) CountPendingFor(context.Context, string, int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.pending, nil
}

func (s *fakeStore) CreateDeliveryAttempt(_ context.Context, id int64, node string, code uint32, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.attempts = append(s.attempts, attempt{id, node, code})

	return int64(len(s.attempts)), nil
}

type fakeRouter struct {
	mu        sync.Mutex
	requests  []s6c.Request
	routing   s6c.Routing
	routes    map[string]s6c.Routing
	err       error
	reports   []s6c.DeliveryReport
	report    s6c.ReportResult
	reportErr error
}

func (r *fakeRouter) SendRoutingInfoForSM(_ context.Context, req s6c.Request) (s6c.Routing, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requests = append(r.requests, req)

	if routing, ok := r.routes[req.MSISDN]; ok {
		return routing, nil
	}

	return r.routing, r.err
}

func (r *fakeRouter) ReportSMDeliveryStatus(_ context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reports = append(r.reports, rep)

	return r.report, r.reportErr
}

type fakeSender struct {
	mu       sync.Mutex
	requests []*diameter.Message
	peers    []string
	answers  map[string]*diameter.Message
	errs     map[string]error
}

func (s *fakeSender) Do(_ context.Context, peer string, req *diameter.Message) (*diameter.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.peers = append(s.peers, peer)
	s.requests = append(s.requests, req)

	if err := s.errs[peer]; err != nil {
		return nil, err
	}

	return s.answers[peer], nil
}

func (s *fakeSender) NewSessionID() string {
	return "smsc.example.org;1;1"
}

func pendingMessage(t *testing.T) db.Message {
	t.Helper()

	return db.Message{
		ID:                 1,
		Originator:         db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:          db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:             "15551230002",
		MessageReference:   7,
		TPDU:               mustHex(t, submitTPDU),
		Status:             db.StatusPending,
		SubmittedAt:        testNow.Add(-time.Minute),
		ExpiresAt:          testNow.Add(24 * time.Hour),
		NextAttemptAt:      testNow,
		ProtocolIdentifier: 0,
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func success() *diameter.Message {
	return &diameter.Message{AVPs: []diameter.AVP{
		diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
	}}
}

func experimental(code uint32, extra ...diameter.AVP) *diameter.Message {
	return &diameter.Message{AVPs: append([]diameter.AVP{
		diameter.Grouped(diameter.AVPExperimentalResult, diameter.AVPFlagMandatory, 0,
			diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
			diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, code),
		),
	}, extra...)}
}

func smDeliveryFailure(cause uint32) *diameter.Message {
	return experimental(tgpp.ResultErrorSMDeliveryFailure, diameter.Grouped(sgd.AVPSMDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.Unsigned32(sgd.AVPSMEnumeratedDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID, cause)))
}

func mmeRouting() s6c.Routing {
	return s6c.Routing{
		IMSI: "001010000000002",
		ServingNodes: s6c.ServingNodes{
			Serving: &s6c.ServingNode{MME: &s6c.Node{Name: "mme.example.org", Realm: "epc.example.org", Number: "15550000010"}},
		},
	}
}

func newDeliverer(store Store, router Router, sender Sender) *Deliverer {
	return &Deliverer{
		Store:                store,
		Router:               router,
		Sender:               sender,
		Identity:             diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org"},
		ServiceCentreAddress: "15550000000",
		RetryIntervals:       []time.Duration{time.Minute, 5 * time.Minute},
		AttemptTimeout:       30 * time.Second,
		Now:                  func() time.Time { return testNow },
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func process(t *testing.T, d *Deliverer) {
	t.Helper()

	m, ok, err := d.Store.NextDue(context.Background(), d.Now(), nil)
	if err != nil || !ok {
		t.Fatalf("NextDue = %v, %v", ok, err)
	}

	if err := d.process(context.Background(), m); err != nil {
		t.Fatalf("process = %v", err)
	}
}

func avpData(t *testing.T, m *diameter.Message, code, vendor uint32) []byte {
	t.Helper()

	a, ok := m.Find(code, vendor)
	if !ok {
		t.Fatalf("AVP %d missing from %+v", code, m)
	}

	return a.Data
}

func TestDeliverViaMME(t *testing.T) {
	store := newFakeStore(pendingMessage(t))
	router := &fakeRouter{routing: mmeRouting()}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}

	process(t, newDeliverer(store, router, sender))

	if store.messages[1].Status != db.StatusDelivered {
		t.Fatalf("status = %s", store.messages[1].Status)
	}

	if len(router.requests) != 1 || router.requests[0] != (s6c.Request{MSISDN: "15551230002"}) {
		t.Fatalf("routing requests = %+v", router.requests)
	}

	if len(store.attempts) != 1 || store.attempts[0] != (attempt{1, "mme.example.org", diameter.ResultSuccess}) {
		t.Fatalf("attempts = %+v", store.attempts)
	}

	tfr := sender.requests[0]
	if tfr.CommandCode != sgd.CommandMTForwardShortMessage || tfr.ApplicationID != sgd.ApplicationID ||
		tfr.Flags != diameter.FlagRequest|diameter.FlagProxiable || tfr.AVPs[0].Code != diameter.AVPSessionID {
		t.Fatalf("TFR header = %+v", tfr)
	}

	checks := map[string]struct {
		code, vendor uint32
		want         []byte
	}{
		"Destination-Host":       {diameter.AVPDestinationHost, 0, []byte("mme.example.org")},
		"Destination-Realm":      {diameter.AVPDestinationRealm, 0, []byte("epc.example.org")},
		"User-Name":              {diameter.AVPUserName, 0, []byte("001010000000002")},
		"SC-Address":             {tgpp.AVPSCAddress, tgpp.VendorID, mustHex(t, "5155000000f0")},
		"MME-Number-for-MT-SMS":  {tgpp.AVPMMENumberForMTSMS, tgpp.VendorID, mustHex(t, "5155000010f0")},
		"SM-Delivery-Timer":      {sgd.AVPSMDeliveryTimer, tgpp.VendorID, mustHex(t, "0000001e")},
		"SM-Delivery-Start-Time": {sgd.AVPSMDeliveryStartTime, tgpp.VendorID, diameter.Time(0, 0, 0, testNow).Data},
		"Auth-Session-State":     {diameter.AVPAuthSessionState, 0, mustHex(t, "00000001")},
	}

	for name, c := range checks {
		if got := avpData(t, tfr, c.code, c.vendor); !bytes.Equal(got, c.want) {
			t.Errorf("%s = %x, want %x", name, got, c.want)
		}
	}

	if _, ok := tfr.Find(sgd.AVPTFRFlags, tgpp.VendorID); ok {
		t.Error("TFR-Flags present with no further messages pending")
	}

	want := mustHex(t, "04"+"0b91"+"5155210300f1"+"00"+"00"+"62907211950000"+"05"+"e8329bfd06")
	if got := avpData(t, tfr, sgd.AVPSMRPUI, tgpp.VendorID); !bytes.Equal(got, want) {
		t.Fatalf("SMS-DELIVER = %x, want %x", got, want)
	}
}

func TestDeliverSignalsMoreMessages(t *testing.T) {
	store := newFakeStore(pendingMessage(t))
	store.pending = 2
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}

	process(t, newDeliverer(store, &fakeRouter{routing: mmeRouting()}, sender))

	tfr := sender.requests[0]
	if flags := avpData(t, tfr, sgd.AVPTFRFlags, tgpp.VendorID); !bytes.Equal(flags, mustHex(t, "00000001")) {
		t.Fatalf("TFR-Flags = %x", flags)
	}

	if first := avpData(t, tfr, sgd.AVPSMRPUI, tgpp.VendorID)[0]; first&0x04 != 0 {
		t.Fatalf("TP-MMS says no more messages (first octet %02x)", first)
	}
}

func TestDeliverFallsBackToAdditionalNode(t *testing.T) {
	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.Node{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{answers: map[string]*diameter.Message{
		"mme.example.org":  experimental(tgpp.ResultErrorAbsentUser),
		"sgsn.example.org": success(),
	}}

	process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

	if store.messages[1].Status != db.StatusDelivered || len(sender.peers) != 2 || sender.peers[1] != "sgsn.example.org" {
		t.Fatalf("status %s via %v", store.messages[1].Status, sender.peers)
	}

	if got := avpData(t, sender.requests[1], tgpp.AVPSGSNNumber, tgpp.VendorID); !bytes.Equal(got, mustHex(t, "5155000020f0")) {
		t.Fatalf("SGSN-Number = %x", got)
	}

	for _, req := range sender.requests {
		for _, code := range []uint32{tgpp.AVPSGSNNumber, tgpp.AVPMMENumberForMTSMS} {
			if a, ok := req.Find(code, tgpp.VendorID); ok && a.Flags&diameter.AVPFlagMandatory != 0 {
				t.Fatalf("AVP %d sent with the M bit; TS 29.338 Table 6.3.3.1/2 forbids it", code)
			}
		}
	}

	if store.attempts[0].code != tgpp.ResultErrorAbsentUser || store.attempts[1].code != diameter.ResultSuccess {
		t.Fatalf("attempts = %+v", store.attempts)
	}
}

func TestDeliverToSMSF(t *testing.T) {
	routing := s6c.Routing{IMSI: "001010000000002", ServingNodes: s6c.ServingNodes{
		SMSF3GPP: &s6c.Node{Name: "smsf.example.org", Realm: "5gc.example.org", Number: "15550000030"},
	}}
	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{answers: map[string]*diameter.Message{"smsf.example.org": success()}}

	process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

	if store.messages[1].Status != db.StatusDelivered || sender.peers[0] != "smsf.example.org" {
		t.Fatalf("status %s via %v", store.messages[1].Status, sender.peers)
	}
}

func TestDeliverOutcomes(t *testing.T) {
	tests := map[string]struct {
		answer     *diameter.Message
		sendErr    error
		singleShot bool
		retries    int
		wantStatus db.MessageStatus
		wantNext   time.Time
	}{
		"absent user is retried":              {answer: experimental(tgpp.ResultErrorAbsentUser), wantStatus: db.StatusPending, wantNext: testNow.Add(time.Minute)},
		"retry interval grows":                {answer: experimental(tgpp.ResultErrorAbsentUser), retries: 1, wantStatus: db.StatusPending, wantNext: testNow.Add(5 * time.Minute)},
		"retry interval stays at the last":    {answer: experimental(tgpp.ResultErrorAbsentUser), retries: 9, wantStatus: db.StatusPending, wantNext: testNow.Add(5 * time.Minute)},
		"busy is retried":                     {answer: experimental(tgpp.ResultErrorUserBusyForMTSMS), wantStatus: db.StatusPending, wantNext: testNow.Add(time.Minute)},
		"memory exceeded is retried":          {answer: smDeliveryFailure(sgd.CauseMemoryCapacityExceeded), wantStatus: db.StatusPending, wantNext: testNow.Add(time.Minute)},
		"transport error is retried":          {sendErr: diameter.ErrNotConnected, wantStatus: db.StatusPending, wantNext: testNow.Add(time.Minute)},
		"base protocol error is retried":      {answer: &diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, 3002)}}, wantStatus: db.StatusPending, wantNext: testNow.Add(time.Minute)},
		"single-shot is not retried":          {answer: experimental(tgpp.ResultErrorAbsentUser), singleShot: true, wantStatus: db.StatusFailed},
		"unknown user at the only node waits": {answer: experimental(tgpp.ResultErrorUserUnknown), wantStatus: db.StatusPending, wantNext: testNow.Add(time.Minute)},
		"base permanent error fails":          {answer: &diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, 5004)}}, wantStatus: db.StatusFailed},
		"illegal user fails":                  {answer: experimental(tgpp.ResultErrorIllegalUser), wantStatus: db.StatusFailed},
		"illegal equipment fails":             {answer: experimental(tgpp.ResultErrorIllegalEquipment), wantStatus: db.StatusFailed},
		"equipment protocol error fails":      {answer: smDeliveryFailure(sgd.CauseEquipmentProtocolError), wantStatus: db.StatusFailed},
		"not SM equipped fails":               {answer: smDeliveryFailure(sgd.CauseEquipmentNotSMEquipped), wantStatus: db.StatusFailed},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m := pendingMessage(t)
			m.SingleShot = tt.singleShot
			m.Retries = tt.retries

			store := newFakeStore(m)
			sender := &fakeSender{
				answers: map[string]*diameter.Message{"mme.example.org": tt.answer},
				errs:    map[string]error{"mme.example.org": tt.sendErr},
			}

			process(t, newDeliverer(store, &fakeRouter{routing: mmeRouting()}, sender))

			got := store.messages[1]
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s", got.Status, tt.wantStatus)
			}

			if tt.wantStatus == db.StatusPending && !got.NextAttemptAt.Equal(tt.wantNext) {
				t.Fatalf("next attempt = %v, want %v", got.NextAttemptAt, tt.wantNext)
			}
		})
	}
}

func TestDeliverRoutingOutcomes(t *testing.T) {
	tests := map[string]struct {
		err  error
		want db.MessageStatus
	}{
		"unknown user":     {&s6c.ResultError{ResultCode: tgpp.ResultErrorUserUnknown, Experimental: true, VendorID: tgpp.VendorID}, db.StatusFailed},
		"barred":           {&s6c.ResultError{ResultCode: tgpp.ResultErrorServiceBarred, Experimental: true, VendorID: tgpp.VendorID}, db.StatusFailed},
		"not subscribed":   {&s6c.ResultError{ResultCode: tgpp.ResultErrorServiceNotSubscribed, Experimental: true, VendorID: tgpp.VendorID}, db.StatusFailed},
		"absent user":      {&s6c.ResultError{ResultCode: tgpp.ResultErrorAbsentUser, Experimental: true, VendorID: tgpp.VendorID}, db.StatusPending},
		"HSS unreachable":  {diameter.ErrNotConnected, db.StatusPending},
		"malformed answer": {s6c.ErrMalformedAnswer, db.StatusPending},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore(pendingMessage(t))
			sender := &fakeSender{}

			process(t, newDeliverer(store, &fakeRouter{err: tt.err}, sender))

			if store.messages[1].Status != tt.want || len(sender.requests) != 0 {
				t.Fatalf("status = %s, TFRs sent = %d", store.messages[1].Status, len(sender.requests))
			}
		})
	}
}

func TestDeliverExpired(t *testing.T) {
	m := pendingMessage(t)
	m.ExpiresAt = testNow

	store := newFakeStore(m)
	router := &fakeRouter{routing: mmeRouting()}

	process(t, newDeliverer(store, router, &fakeSender{}))

	if store.messages[1].Status != db.StatusExpired || len(router.requests) != 0 {
		t.Fatalf("status = %s, routing requests = %d", store.messages[1].Status, len(router.requests))
	}
}

func TestDeliverNoSGdTarget(t *testing.T) {
	routing := s6c.Routing{IMSI: "001010000000002", ServingNodes: s6c.ServingNodes{Serving: &s6c.ServingNode{MSCNumber: "15550000040"}}}
	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{}

	process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

	if store.messages[1].Status != db.StatusFailed || len(sender.requests) != 0 {
		t.Fatalf("status = %s, TFRs = %d", store.messages[1].Status, len(sender.requests))
	}
}

func TestDeliverSingleShotRequestsSingleAttempt(t *testing.T) {
	m := pendingMessage(t)
	m.SingleShot = true

	router := &fakeRouter{routing: mmeRouting()}

	process(t, newDeliverer(newFakeStore(m), router, &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}))

	if !router.requests[0].SingleAttempt {
		t.Fatal("single-shot message must request single-attempt delivery from the HSS")
	}
}

func TestRunDeliversStoredMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()

	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}
	d := newDeliverer(database, &fakeRouter{routing: mmeRouting()}, sender)
	d.Now = time.Now

	done := make(chan struct{})

	go func() {
		d.Run(ctx)
		close(done)
	}()

	id, err := database.CreateMessage(ctx, db.NewMessage{
		Originator:  db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:   db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:      "15551230002",
		TPDU:        mustHex(t, submitTPDU),
		SubmittedAt: time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	d.Notify()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, err := database.GetMessage(ctx, id)
		if err == nil && m.Status == db.StatusDelivered {
			cancel()
			<-done

			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("message was not delivered")
}

func TestDeliverUnknownUserTriesNextNode(t *testing.T) {
	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.Node{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{answers: map[string]*diameter.Message{
		"mme.example.org":  experimental(tgpp.ResultErrorUserUnknown),
		"sgsn.example.org": success(),
	}}

	process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

	if store.messages[1].Status != db.StatusDelivered || len(sender.peers) != 2 {
		t.Fatalf("status %s via %v", store.messages[1].Status, sender.peers)
	}
}

func TestDeliverMixedFailuresAreRetried(t *testing.T) {
	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.Node{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{answers: map[string]*diameter.Message{
		"mme.example.org":  experimental(tgpp.ResultErrorUserUnknown),
		"sgsn.example.org": experimental(tgpp.ResultErrorAbsentUser),
	}}

	process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

	if store.messages[1].Status != db.StatusPending {
		t.Fatalf("status = %s; one node was only temporarily unavailable", store.messages[1].Status)
	}
}

type cancellingSender struct {
	*fakeSender
	cancel context.CancelFunc
}

func (s *cancellingSender) Do(ctx context.Context, peer string, req *diameter.Message) (*diameter.Message, error) {
	s.cancel()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return s.fakeSender.Do(ctx, peer, req)
}

func TestShutdownLetsInFlightDeliveryFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore(pendingMessage(t))
	sender := &cancellingSender{
		fakeSender: &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}},
		cancel:     cancel,
	}

	newDeliverer(store, &fakeRouter{routing: mmeRouting()}, sender).Run(ctx)

	if store.messages[1].Status != db.StatusDelivered {
		t.Fatalf("status = %s; a delivery in flight at shutdown must complete and be recorded", store.messages[1].Status)
	}
}

func TestNotifyBeforeRun(t *testing.T) {
	d := newDeliverer(newFakeStore(pendingMessage(t)), &fakeRouter{}, &fakeSender{})

	done := make(chan struct{})

	go func() {
		d.Notify()
		close(done)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	d.Run(ctx)
	<-done
}

func absentWithDiagnostic(diagnostic uint32) *diameter.Message {
	return experimental(tgpp.ResultErrorAbsentUser,
		diameter.Unsigned32(sgd.AVPAbsentUserDiagnosticSM, diameter.AVPFlagMandatory, tgpp.VendorID, diagnostic))
}

func u32(v uint32) *uint32 {
	return &v
}

func TestAbsentUserIsReportedAndHoldsRecipient(t *testing.T) {
	store := newFakeStore(pendingMessage(t))
	router := &fakeRouter{routing: mmeRouting()}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": absentWithDiagnostic(2)}}

	process(t, newDeliverer(store, router, sender))

	want := s6c.DeliveryReport{
		MSISDN: "15551230002",
		MME:    &s6c.DeliveryOutcome{Cause: s6c.DeliveryCauseAbsentUser, AbsentDiagnostic: u32(2)},
		Failed: s6c.ServingNodes{Serving: mmeRouting().Serving},
	}

	if len(router.reports) != 1 || !reflect.DeepEqual(router.reports[0], want) {
		t.Fatalf("reports = %+v", router.reports)
	}

	if len(store.holds) != 1 || store.holds[0] != (hold{"15551230002", testNow.Add(time.Minute)}) {
		t.Fatalf("holds = %+v", store.holds)
	}
}

func TestReportConditions(t *testing.T) {
	tests := map[string]struct {
		answer     *diameter.Message
		mwdStatus  uint32
		absent     s6c.AbsentUserDiagnostics
		wantReport bool
	}{
		"MNRF already set":                    {experimental(tgpp.ResultErrorAbsentUser), s6c.MWDStatusMNRF, s6c.AbsentUserDiagnostics{}, false},
		"SC address not in the MWD":           {experimental(tgpp.ResultErrorAbsentUser), s6c.MWDStatusMNRF | s6c.MWDStatusSCAddressNotIncluded, s6c.AbsentUserDiagnostics{}, true},
		"only MNRG set":                       {experimental(tgpp.ResultErrorAbsentUser), s6c.MWDStatusMNRG, s6c.AbsentUserDiagnostics{}, true},
		"same absent diagnostic":              {absentWithDiagnostic(1), s6c.MWDStatusMNRF, s6c.AbsentUserDiagnostics{MME: u32(1)}, false},
		"different absent diagnostic":         {absentWithDiagnostic(2), s6c.MWDStatusMNRF, s6c.AbsentUserDiagnostics{MME: u32(1)}, true},
		"memory exceeded without MCEF":        {smDeliveryFailure(sgd.CauseMemoryCapacityExceeded), s6c.MWDStatusMNRF, s6c.AbsentUserDiagnostics{}, true},
		"memory exceeded with MCEF":           {smDeliveryFailure(sgd.CauseMemoryCapacityExceeded), s6c.MWDStatusMCEF, s6c.AbsentUserDiagnostics{}, false},
		"unidentified user":                   {experimental(tgpp.ResultErrorUserUnknown), 0, s6c.AbsentUserDiagnostics{}, true},
		"busy is not reported":                {experimental(tgpp.ResultErrorUserBusyForMTSMS), 0, s6c.AbsentUserDiagnostics{}, false},
		"success with no flags":               {success(), 0, s6c.AbsentUserDiagnostics{}, false},
		"success clears MNRF":                 {success(), s6c.MWDStatusMNRF, s6c.AbsentUserDiagnostics{}, true},
		"success with only SC not in the MWD": {success(), s6c.MWDStatusSCAddressNotIncluded, s6c.AbsentUserDiagnostics{}, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			routing := mmeRouting()
			routing.MWDStatus = tt.mwdStatus
			routing.Absent = tt.absent

			router := &fakeRouter{routing: routing}
			sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": tt.answer}}

			process(t, newDeliverer(newFakeStore(pendingMessage(t)), router, sender))

			if got := len(router.reports) == 1; got != tt.wantReport {
				t.Fatalf("reported = %v, want %v (%+v)", got, tt.wantReport, router.reports)
			}
		})
	}
}

func TestMemoryExceededReport(t *testing.T) {
	router := &fakeRouter{routing: mmeRouting()}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": smDeliveryFailure(sgd.CauseMemoryCapacityExceeded)}}
	store := newFakeStore(pendingMessage(t))

	process(t, newDeliverer(store, router, sender))

	if len(router.reports) != 1 || router.reports[0].MME == nil || router.reports[0].MME.Cause != s6c.DeliveryCauseMemoryCapacityExceeded {
		t.Fatalf("reports = %+v", router.reports)
	}

	if len(store.holds) != 1 {
		t.Fatalf("holds = %+v", store.holds)
	}
}

func TestSecondPathSuccessIsReported(t *testing.T) {
	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.Node{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	router := &fakeRouter{routing: routing}
	sender := &fakeSender{answers: map[string]*diameter.Message{
		"mme.example.org":  absentWithDiagnostic(1),
		"sgsn.example.org": success(),
	}}

	process(t, newDeliverer(newFakeStore(pendingMessage(t)), router, sender))

	want := s6c.DeliveryReport{
		MSISDN: "15551230002",
		MME:    &s6c.DeliveryOutcome{Cause: s6c.DeliveryCauseAbsentUser, AbsentDiagnostic: u32(1)},
		SGSN:   &s6c.DeliveryOutcome{Cause: s6c.DeliveryCauseSuccessfulTransfer},
	}

	if len(router.reports) != 1 || !reflect.DeepEqual(router.reports[0], want) {
		t.Fatalf("reports = %+v", router.reports)
	}
}

func TestSingleShotReportRequestsSingleAttempt(t *testing.T) {
	m := pendingMessage(t)
	m.SingleShot = true

	router := &fakeRouter{routing: mmeRouting()}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": experimental(tgpp.ResultErrorAbsentUser)}}
	store := newFakeStore(m)

	process(t, newDeliverer(store, router, sender))

	if len(router.reports) != 1 || !router.reports[0].SingleAttempt {
		t.Fatalf("reports = %+v", router.reports)
	}

	if store.messages[1].Status != db.StatusFailed || len(store.holds) != 0 {
		t.Fatalf("status = %s, holds = %+v", store.messages[1].Status, store.holds)
	}
}

func TestReportAnswerNodesAreRetriedImmediately(t *testing.T) {
	router := &fakeRouter{
		routing: mmeRouting(),
		report: s6c.ReportResult{
			ServingNodes: s6c.ServingNodes{
				Serving: &s6c.ServingNode{MME: &s6c.Node{Name: "mme2.example.org", Realm: "epc.example.org", Number: "15550000011"}},
			},
			AlertMSISDN: "15559990000",
		},
	}
	sender := &fakeSender{answers: map[string]*diameter.Message{
		"mme.example.org":  experimental(tgpp.ResultErrorAbsentUser),
		"mme2.example.org": success(),
	}}
	store := newFakeStore(pendingMessage(t))

	process(t, newDeliverer(store, router, sender))

	if store.messages[1].Status != db.StatusDelivered || !slices.Equal(sender.peers, []string{"mme.example.org", "mme2.example.org"}) {
		t.Fatalf("status %s via %v", store.messages[1].Status, sender.peers)
	}

	if len(router.reports) != 2 || router.reports[1].MME == nil || router.reports[1].MME.Cause != s6c.DeliveryCauseSuccessfulTransfer {
		t.Fatalf("reports = %+v", router.reports)
	}

	if store.alertMSISDNs["15551230002"] != "15559990000" {
		t.Fatalf("Alert MSISDNs = %+v", store.alertMSISDNs)
	}
}

func TestMemoryExceededDoesNotReportFailedNode(t *testing.T) {
	router := &fakeRouter{routing: mmeRouting()}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": smDeliveryFailure(sgd.CauseMemoryCapacityExceeded)}}

	process(t, newDeliverer(newFakeStore(pendingMessage(t)), router, sender))

	if len(router.reports) != 1 || router.reports[0].Failed != (s6c.ServingNodes{}) {
		t.Fatalf("reports = %+v", router.reports)
	}
}

func TestAdditionalNodeFailureIsReported(t *testing.T) {
	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.Node{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	router := &fakeRouter{routing: routing}
	sender := &fakeSender{answers: map[string]*diameter.Message{
		"mme.example.org":  experimental(tgpp.ResultErrorUserBusyForMTSMS),
		"sgsn.example.org": experimental(tgpp.ResultErrorAbsentUser),
	}}

	process(t, newDeliverer(newFakeStore(pendingMessage(t)), router, sender))

	if len(router.reports) != 1 || router.reports[0].Failed != (s6c.ServingNodes{Additional: routing.Additional}) {
		t.Fatalf("reports = %+v", router.reports)
	}
}

func TestShutdownStopsBetweenSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.Node{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	store := newFakeStore(pendingMessage(t))
	router := &fakeRouter{routing: routing}
	sender := &cancellingSender{
		fakeSender: &fakeSender{answers: map[string]*diameter.Message{
			"mme.example.org":  experimental(tgpp.ResultErrorAbsentUser),
			"sgsn.example.org": success(),
		}},
		cancel: cancel,
	}

	newDeliverer(store, router, sender).Run(ctx)

	if len(sender.peers) != 1 || len(router.reports) != 0 {
		t.Fatalf("TFRs to %v, reports = %+v; nothing new may start after shutdown", sender.peers, router.reports)
	}

	if m := store.messages[1]; m.Status != db.StatusPending || !m.NextAttemptAt.Equal(testNow.Add(time.Minute)) {
		t.Fatalf("message = %+v", m)
	}
}

func TestReportFailureStillHolds(t *testing.T) {
	router := &fakeRouter{routing: mmeRouting(), reportErr: &s6c.ResultError{ResultCode: tgpp.ResultErrorMWDListFull, Experimental: true, VendorID: tgpp.VendorID}}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": experimental(tgpp.ResultErrorAbsentUser)}}
	store := newFakeStore(pendingMessage(t))

	process(t, newDeliverer(store, router, sender))

	if store.messages[1].Status != db.StatusPending || len(store.holds) != 1 {
		t.Fatalf("status = %s, holds = %+v", store.messages[1].Status, store.holds)
	}
}

func TestRoutingAbsentUserHoldsWithoutReport(t *testing.T) {
	router := &fakeRouter{err: &s6c.ResultError{
		ResultCode: tgpp.ResultErrorAbsentUser, Experimental: true, VendorID: tgpp.VendorID, AlertMSISDN: "15559990000",
	}}
	store := newFakeStore(pendingMessage(t))

	process(t, newDeliverer(store, router, &fakeSender{}))

	if len(router.reports) != 0 || len(store.holds) != 1 || store.alertMSISDNs["15551230002"] != "15559990000" {
		t.Fatalf("reports = %+v, holds = %+v, Alert MSISDNs = %+v", router.reports, store.holds, store.alertMSISDNs)
	}
}

func TestRoutingRecordsAlertMSISDN(t *testing.T) {
	routing := mmeRouting()
	routing.AlertMSISDN = "15559990000"

	store := newFakeStore(pendingMessage(t))
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}

	process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

	if store.alertMSISDNs["15551230002"] != "15559990000" {
		t.Fatalf("Alert MSISDNs = %+v", store.alertMSISDNs)
	}
}

func TestAlertOfBusyRecipientIsReapplied(t *testing.T) {
	store := newFakeStore(pendingMessage(t))
	store.recipients = []string{"15551230002", "15551230009"}

	d := newDeliverer(store, &fakeRouter{}, &fakeSender{})
	d.busy = map[string]bool{"15551230002": true}
	d.realert = map[string]bool{}

	if err := d.Alert(context.Background(), "15559990000"); err != nil {
		t.Fatal(err)
	}

	if !d.realert["15551230002"] || d.realert["15551230009"] {
		t.Fatalf("realert = %+v", d.realert)
	}

	d.finish(context.Background(), "15551230002")

	if !slices.Equal(store.alerts, []string{"15559990000", "15551230002"}) || len(d.realert) != 0 || d.busy["15551230002"] {
		t.Fatalf("alerts = %v, realert = %+v, busy = %+v", store.alerts, d.realert, d.busy)
	}
}

type blockingSender struct {
	*fakeSender
	blockPeer string
	release   chan struct{}
}

func (s *blockingSender) Do(ctx context.Context, peer string, req *diameter.Message) (*diameter.Message, error) {
	if peer == s.blockPeer {
		<-s.release
	}

	return s.fakeSender.Do(ctx, peer, req)
}

func TestRunDeliversOtherRecipientsWhileOneIsStuck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()

	slow := mmeRouting()
	fast := mmeRouting()
	fast.Serving = &s6c.ServingNode{MME: &s6c.Node{Name: "mme2.example.org", Realm: "epc.example.org", Number: "15550000011"}}

	sender := &blockingSender{
		fakeSender: &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success(), "mme2.example.org": success()}},
		blockPeer:  "mme.example.org",
		release:    make(chan struct{}),
	}
	router := &fakeRouter{routes: map[string]s6c.Routing{"15551230002": slow, "15551230003": fast}}

	d := newDeliverer(database, router, sender)
	d.Now = time.Now
	d.Concurrency = 2

	var ids []int64

	for _, msisdn := range []string{"15551230002", "15551230003"} {
		id, err := database.CreateMessage(ctx, db.NewMessage{
			Originator:  db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
			Recipient:   db.Address{Digits: msisdn, TypeOfNumber: 1, NumberingPlan: 1},
			MSISDN:      msisdn,
			TPDU:        mustHex(t, submitTPDU),
			SubmittedAt: time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	done := make(chan struct{})

	go func() {
		d.Run(ctx)
		close(done)
	}()

	waitForStatus(t, database, ids[1], db.StatusDelivered)

	if m, err := database.GetMessage(ctx, ids[0]); err != nil || m.Status != db.StatusPending {
		t.Fatalf("stuck message = %+v, %v", m, err)
	}

	close(sender.release)
	waitForStatus(t, database, ids[0], db.StatusDelivered)

	cancel()
	<-done
}

func waitForStatus(t *testing.T, database *db.DB, id int64, want db.MessageStatus) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m, err := database.GetMessage(context.Background(), id); err == nil && m.Status == want {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("message %d never reached %s", id, want)
}

type cancellingRouter struct {
	*fakeRouter
	cancel context.CancelFunc
}

func (r *cancellingRouter) SendRoutingInfoForSM(ctx context.Context, req s6c.Request) (s6c.Routing, error) {
	r.cancel()

	return r.fakeRouter.SendRoutingInfoForSM(ctx, req)
}

func TestShutdownAfterRoutingSendsNoTFR(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newFakeStore(pendingMessage(t))
	router := &cancellingRouter{fakeRouter: &fakeRouter{routing: mmeRouting()}, cancel: cancel}
	sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}

	newDeliverer(store, router, sender).Run(ctx)

	if len(sender.requests) != 0 || len(store.attempts) != 0 {
		t.Fatalf("TFRs = %d, attempts = %+v; no TFR may start after shutdown", len(sender.requests), store.attempts)
	}

	if m := store.messages[1]; m.Status != db.StatusPending || m.Retries != 0 || !m.NextAttemptAt.Equal(testNow) {
		t.Fatalf("message = %+v; an interrupted delivery must leave the message untouched", m)
	}
}
