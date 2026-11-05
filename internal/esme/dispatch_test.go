package esme

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/smpp"
)

type fakeConn struct {
	got    chan *smpp.DeliverSM
	answer func(*smpp.DeliverSM) (smpp.CommandStatus, error)
	closed atomic.Bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{got: make(chan *smpp.DeliverSM, 32)}
}

func (c *fakeConn) Deliver(_ context.Context, d *smpp.DeliverSM) (smpp.CommandStatus, error) {
	c.got <- d

	if c.answer != nil {
		return c.answer(d)
	}

	return smpp.StatusOK, nil
}

func (c *fakeConn) Close() { c.closed.Store(true) }

func (c *fakeConn) next(t *testing.T) *smpp.DeliverSM {
	t.Helper()

	select {
	case d := <-c.got:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no deliver_sm")
		return nil
	}
}

func (c *fakeConn) none(t *testing.T) {
	t.Helper()

	select {
	case d := <-c.got:
		t.Fatalf("unexpected deliver_sm %q", d.ShortMessage)
	case <-time.After(100 * time.Millisecond):
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (e *testEnv) run(t *testing.T) *clock {
	t.Helper()

	c := &clock{now: testNow}
	e.service.Now = c.Now
	e.service.Submitter.Now = c.Now

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		e.service.Run(ctx)
		close(done)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	return c
}

func (e *testEnv) bind(id uint64, kind BindType, esme db.ESME) *fakeConn {
	return e.bindAnswering(id, kind, esme, nil)
}

func (e *testEnv) bindAnswering(
	id uint64, kind BindType, esme db.ESME, answer func(*smpp.DeliverSM) (smpp.CommandStatus, error),
) *fakeConn {
	conn := newFakeConn()
	conn.answer = answer
	e.service.attach(&Session{ID: id, ESME: esme, UserID: esme.UserID, BindType: kind}, conn)

	return conn
}

func (e *testEnv) stored(t *testing.T, sm smpp.Message) int64 {
	t.Helper()

	id, status := e.service.SubmitSM(context.Background(), e.session(Transceiver), &smpp.SubmitSM{Message: sm})
	if status != smpp.StatusOK {
		t.Fatalf("submit_sm = %s", status)
	}

	n, _ := strconv.ParseInt(id, 10, 64)

	return n
}

func (e *testEnv) complete(t *testing.T, id int64, status db.MessageStatus, tpStatus uint8, outcome, report string, at time.Time) {
	t.Helper()

	ctx := context.Background()

	if outcome != "" {
		if _, err := e.store.CreateDeliveryAttempt(ctx, db.DeliveryAttempt{
			MessageID: id, StartedAt: at, CompletedAt: at, Step: db.StepDelivery, Node: "mme.example.org",
			NodeType: db.NodeTypeMME, Outcome: outcome, DeliverReport: unhex(report),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := e.store.CompleteMessage(ctx, id, status, at, nil, tpStatus, db.Fence{}); err != nil {
		t.Fatal(err)
	}

	e.service.NotificationsQueued()
}

func encodeDeliverSM(t *testing.T, d *smpp.DeliverSM) string {
	t.Helper()

	b, err := smpp.Encode(d, smpp.StatusOK, 1)
	if err != nil {
		t.Fatal(err)
	}

	return hex.EncodeToString(b[smpp.HeaderLength:])
}

func cstring(s string) string { return hex.EncodeToString(append([]byte(s), 0)) }

func TestReceipt(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
		m.RegisteredDelivery = 0x01
		m.Options.UserMessageReference = ptr[uint16](0x0102)
	}))

	c.advance(90 * time.Second)
	env.complete(t, id, db.StatusDelivered, uint8(tpdu.StatusReceived), "success", "", c.Now())

	d := conn.next(t)

	text := "id:" + strconv.FormatInt(id, 10) + " sub:001 dlvrd:001 submit date:2610281200 done date:2610281201 " +
		"stat:DELIVRD err:000 text:"

	want := "00" + "0101" + cstring("15551230002") + "0101" + cstring("15550001000") + "04" + "00" + "00" + "00" + "00" +
		"00" + "00" + "01" + "00" + hex.EncodeToString([]byte{byte(len(text))}) + hex.EncodeToString([]byte(text)) +
		"001e0002" + cstring(strconv.FormatInt(id, 10)) + "020400020102" + "0427000102"

	if got := encodeDeliverSM(t, d); got != want {
		t.Fatalf("deliver_sm = %s\nwant         %s", got, want)
	}

	waitResult(t, env, id, db.NotificationReceipt, db.NotificationAcknowledged)
}

// SMPP 3.4 section 2.11
func TestReceiptEchoesTheSourceNPI(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) {
		m.Source.NPI = 0
		m.RegisteredDelivery = 0x01
	}))

	if m, err := env.store.GetMessage(context.Background(), id); err != nil || m.Originator.NumberingPlan != 1 {
		t.Fatalf("stored originator = %+v, %v; want NPI 1", m.Originator, err)
	}

	env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())

	if d := conn.next(t); d.Destination != (smpp.Address{TON: 1, NPI: 0, Addr: "15550001000"}) {
		t.Fatalf("receipt destination = %+v, want TON 1, NPI 0", d.Destination)
	}
}

func waitResult(t *testing.T, env *testEnv, messageID int64, typ db.NotificationType, want db.NotificationResult) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if got := notificationResult(t, env, messageID, typ); got == want {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("%s on message %d: result %q, want %q", typ, messageID, notificationResult(t, env, messageID, typ), want)
}

func TestReceiptStates(t *testing.T) {
	tests := []struct {
		receipt  byte
		status   db.MessageStatus
		tpStatus uint8
		want     string
	}{
		{0x01, db.StatusFailed, 0x42, "dlvrd:000 submit date:2610281200 done date:2610281200 stat:UNDELIV err:066 text:"},
		{0x01, db.StatusExpired, 0x46, "dlvrd:000 submit date:2610281200 done date:2610281200 stat:EXPIRED err:070 text:"},
		{0x02, db.StatusFailed, 0x67, "dlvrd:000 submit date:2610281200 done date:2610281200 stat:UNDELIV err:103 text:"},
		{0x02, db.StatusDelivered, 0x00, ""},
		{0x03, db.StatusFailed, 0x42, ""},
		{0x00, db.StatusFailed, 0x42, ""},
	}

	for _, tc := range tests {
		t.Run(string(tc.status)+strconv.Itoa(int(tc.receipt)), func(t *testing.T) {
			env := newTestEnv(t, nil)
			c := env.run(t)
			conn := env.bind(1, Transceiver, env.esme)

			id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = tc.receipt }))
			env.complete(t, id, tc.status, tc.tpStatus, "", "", c.Now())

			if tc.want == "" {
				conn.none(t)
				return
			}

			d := conn.next(t)
			if got := string(d.ShortMessage); !strings.HasSuffix(got, tc.want) {
				t.Fatalf("receipt %q, want …%q", got, tc.want)
			}
		})
	}
}

func TestReplacedReceipt(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	sm := with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.ProtocolID, m.RegisteredDelivery = 0x41, 0x01 })
	first := env.stored(t, sm)

	conn.none(t)

	c.advance(5 * time.Minute)

	if _, status := env.service.SubmitSM(context.Background(), env.session(Transceiver),
		&smpp.SubmitSM{Message: with(sm, func(m *smpp.Message) { m.RegisteredDelivery = 0 })}); status != smpp.StatusOK {
		t.Fatalf("submit_sm = %s", status)
	}

	want := "id:" + strconv.FormatInt(first, 10) +
		" sub:001 dlvrd:000 submit date:2610281200 done date:2610281205 stat:DELETED err:002 text:"
	if d := conn.next(t); string(d.ShortMessage) != want || *d.Options.MessageState != smpp.StateDeleted {
		t.Fatalf("receipt %q, state %v; want %q", d.ShortMessage, d.Options.MessageState, want)
	}
}

func TestSIMAnswer(t *testing.T) {
	const ack = "00 07 7f f6 07 02710003aabbcc"

	tests := []struct {
		name     string
		esme     func(*db.ESME)
		sm       smpp.Message
		outcome  string
		report   string
		status   db.MessageStatus
		esmClass byte
		dc       byte
		pid      byte
		ud       string
	}{
		{
			name: "pySim, asking for a user acknowledgement",
			sm: with(baseSubmit(0x02, "027000 0011aabb"), func(m *smpp.Message) {
				m.ESMClass, m.ProtocolID, m.RegisteredDelivery = 0x40, 0x7f, 0x08
			}),
			outcome: "success", report: ack, status: db.StatusDelivered,
			esmClass: 0x50, dc: 0x02, pid: 0x7f, ud: "02710003aabbcc",
		},
		{
			name: "registered_delivery asks for a user acknowledgement",
			sm: with(baseSubmit(0x04, "027000 0011aabb"), func(m *smpp.Message) {
				m.ESMClass, m.ProtocolID, m.RegisteredDelivery = 0x40, 0x7f, 0x08
			}),
			outcome: "success", report: ack, status: db.StatusDelivered,
			esmClass: 0x50, dc: 0x04, pid: 0x7f, ud: "02710003aabbcc",
		},
		{
			name: "RP-ERROR with TP-FCS D5",
			sm: with(baseSubmit(0xf6, "027000 0011aabb"), func(m *smpp.Message) {
				m.ESMClass, m.ProtocolID, m.RegisteredDelivery = 0x40, 0x7f, 0x0c
			}),
			outcome: "absent_user", report: "00 d5 07 7f f6 06 0271000102ff", status: db.StatusFailed,
			esmClass: 0x50, dc: 0xf6, pid: 0x7f, ud: "0271000102ff",
		},
		{
			name: "PID 0x7F user data without a UDH",
			sm: with(baseSubmit(0xf6, "027000 0011aabb"), func(m *smpp.Message) {
				m.ESMClass, m.ProtocolID, m.RegisteredDelivery = 0x40, 0x7f, 0x08
			}),
			outcome: "absent_user", report: "00 d5 07 7f f6 02 cafe", status: db.StatusFailed,
			esmClass: 0x10, dc: 0xf6, pid: 0x7f, ud: "cafe",
		},
		{
			name:    "a report with TP-UDHI and UCS2 for another PID",
			sm:      with(baseSubmit(0x08, "0041"), func(m *smpp.Message) { m.RegisteredDelivery = 0x08 }),
			outcome: "success", report: "40 06 08 06 0270000041 00", status: db.StatusDelivered,
			esmClass: 0x50, dc: 0x08, pid: 0x00, ud: "027000004100",
		},
		{
			name:    "a report in GSM 7 bit",
			sm:      with(baseSubmit(0x00, "68"), func(m *smpp.Message) { m.RegisteredDelivery = 0x08 }),
			outcome: "success", report: "00 04 02 e834", status: db.StatusDelivered,
			esmClass: 0x10, dc: 0x00, pid: 0x00, ud: "6869",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, tc.esme)
			c := env.run(t)
			conn := env.bind(1, Transceiver, env.esme)

			id := env.stored(t, tc.sm)
			env.complete(t, id, tc.status, 0, tc.outcome, tc.report, c.Now())

			d := conn.next(t)

			if d.ESMClass != tc.esmClass || d.DataCoding != tc.dc || d.ProtocolID != tc.pid ||
				hex.EncodeToString(d.ShortMessage) != tc.ud {
				t.Fatalf("answer esm_class %#x data_coding %#x protocol_id %#x short_message %x; want %#x %#x %#x %s",
					d.ESMClass, d.DataCoding, d.ProtocolID, d.ShortMessage, tc.esmClass, tc.dc, tc.pid, tc.ud)
			}

			o := d.Options
			if o.ReceiptedMessageID == nil || *o.ReceiptedMessageID != strconv.FormatInt(id, 10) || o.MessageState != nil ||
				d.Source.Addr != "15551230002" || d.Destination.Addr != "15550001000" {
				t.Fatalf("answer options %+v, from %s to %s", o, d.Source.Addr, d.Destination.Addr)
			}

			waitResult(t, env, id, db.NotificationSIMAnswer, db.NotificationAcknowledged)

			if tc.sm.RegisteredDelivery&0x03 != 0 {
				if r := conn.next(t); r.ESMClass != esmTypeReceipt {
					t.Fatalf("after the answer: esm_class %#x", r.ESMClass)
				}
			}
		})
	}
}

// SMPP 3.4 section 2.11
// SMPP 3.4 section 5.2.17
func TestNoSIMAnswerUnasked(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)
	conn := env.bind(1, Transceiver, env.esme)

	id := env.stored(t, with(baseSubmit(0x02, "027000 0011aabb"), func(m *smpp.Message) {
		m.ESMClass, m.ProtocolID, m.RegisteredDelivery = 0x40, 0x7f, 0x01
	}))
	env.complete(t, id, db.StatusDelivered, 0, "success", "00 07 7f f6 07 02710003aabbcc", c.Now())

	if d := conn.next(t); d.ESMClass != esmTypeReceipt {
		t.Fatalf("esm_class %#x, want only the receipt", d.ESMClass)
	}

	conn.none(t)

	if r := notificationResult(t, env, id, db.NotificationSIMAnswer); r != "missing" {
		t.Fatalf("SIM answer %q, want none", r)
	}
}

func TestSIMAnswerWithoutUserData(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x09 }))

	env.complete(t, id, db.StatusDelivered, 0, "success", "0000", c.Now())

	if d := conn.next(t); d.ESMClass != esmTypeReceipt {
		t.Fatalf("esm_class %#x, want the receipt only", d.ESMClass)
	}

	waitResult(t, env, id, db.NotificationSIMAnswer, db.NotificationNoUserData)
}

func TestAnswerBeforeReceipt(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)
	conn := env.bind(1, Transceiver, env.esme)

	var calls atomic.Int32

	conn.answer = func(*smpp.DeliverSM) (smpp.CommandStatus, error) {
		if calls.Add(1) == 1 {
			return smpp.StatusXTAppn, nil
		}

		return smpp.StatusOK, nil
	}

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x09 }))
	env.complete(t, id, db.StatusDelivered, 0, "success", "00 04 01 aa", c.Now())

	if d := conn.next(t); d.ESMClass != esmTypeSIMAnswer {
		t.Fatalf("first esm_class %#x", d.ESMClass)
	}

	conn.none(t)

	c.advance(time.Minute)
	env.service.NotificationsQueued()

	if d := conn.next(t); d.ESMClass != esmTypeSIMAnswer {
		t.Fatalf("second esm_class %#x", d.ESMClass)
	}

	if d := conn.next(t); d.ESMClass != esmTypeReceipt {
		t.Fatalf("third esm_class %#x", d.ESMClass)
	}
}

func TestOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		status smpp.CommandStatus
		err    error
		want   db.NotificationResult
	}{
		{"ESME_ROK", smpp.StatusOK, nil, db.NotificationAcknowledged},
		{"ESME_RX_P_APPN", smpp.StatusXPAppn, nil, db.NotificationRejected},
		{"ESME_RX_R_APPN", smpp.StatusXRAppn, nil, db.NotificationRejected},
		{"ESME_RX_T_APPN", smpp.StatusXTAppn, nil, ""},
		{"ESME_RSYSERR", smpp.StatusSysErr, nil, ""},
		{"no response", 0, smpp.ErrResponseTimeout, ""},
		{"generic_nack", 0, &smpp.StatusError{Command: smpp.CmdGenericNack, Status: smpp.StatusInvCmdID}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, nil)
			c := env.run(t)
			conn := env.bind(1, Receiver, env.esme)
			conn.answer = func(*smpp.DeliverSM) (smpp.CommandStatus, error) { return tc.status, tc.err }

			id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }))
			env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())
			conn.next(t)

			if tc.want != "" {
				waitResult(t, env, id, db.NotificationReceipt, tc.want)
				return
			}

			for _, wait := range []time.Duration{time.Minute, 2 * time.Minute} {
				conn.none(t)
				c.advance(wait)
				env.service.NotificationsQueued()
				conn.next(t)
			}
		})
	}
}

func TestExpiry(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }))
	env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())

	c.advance(env.settings.Delivery.DefaultValidity)
	env.service.Expire(context.Background())
	waitResult(t, env, id, db.NotificationReceipt, db.NotificationExpired)

	conn := env.bind(1, Receiver, env.esme)
	conn.none(t)
}

func TestRouting(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)

	otherID, err := env.store.CreateESME(context.Background(), db.ESME{
		SystemID: "other", UserID: env.user.ID, PasswordHash: env.esme.PasswordHash, CreatedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}

	transmitter := env.bind(1, Transmitter, env.esme)
	other := env.bind(2, Receiver, db.ESME{ID: otherID, SystemID: "other", UserID: env.user.ID})

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }))
	env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())

	transmitter.none(t)
	other.none(t)

	receiver := env.bind(3, Receiver, env.esme)
	receiver.next(t)
	waitResult(t, env, id, db.NotificationReceipt, db.NotificationAcknowledged)
}

func TestLostSession(t *testing.T) {
	env := newTestEnv(t, nil)
	c := env.run(t)

	id := env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }))

	for i := range 4 {
		conn := env.bindAnswering(uint64(i+1), Receiver, env.esme, func(*smpp.DeliverSM) (smpp.CommandStatus, error) {
			return 0, smpp.ErrClosed
		})

		if i == 0 {
			env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())
		} else {
			env.service.NotificationsQueued()
		}

		conn.next(t)
		env.service.NotificationsQueued()
		conn.none(t)
		env.service.detach(&Session{ID: uint64(i + 1), BindType: Receiver})
	}

	again := env.bind(9, Receiver, env.esme)
	again.next(t)
	waitResult(t, env, id, db.NotificationReceipt, db.NotificationAcknowledged)
}

func TestRetryDelay(t *testing.T) {
	for attempts, want := range []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour,
	} {
		if got := retryDelay(attempts); got != want {
			t.Errorf("retryDelay(%d) = %s, want %s", attempts, got, want)
		}
	}
}

func notificationResult(t *testing.T, env *testEnv, messageID int64, typ db.NotificationType) db.NotificationResult {
	t.Helper()

	conn, err := sql.Open("sqlite3", "file:"+env.path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = conn.Close() }()

	var r sql.Null[db.NotificationResult]

	err = conn.QueryRowContext(context.Background(), `SELECT result FROM smpp_notifications WHERE message_id = ? AND type = ?`,
		messageID, typ).Scan(&r)
	if errors.Is(err, sql.ErrNoRows) {
		return "missing"
	}

	if err != nil {
		t.Fatal(err)
	}

	return r.V
}

type claimRecorder struct {
	Store

	mu      sync.Mutex
	largest int
}

func (r *claimRecorder) ClaimNotifications(ctx context.Context, nodeID string, esmeID int64, now time.Time,
	lease time.Duration, limit int,
) ([]db.Notification, error) {
	r.mu.Lock()
	r.largest = max(r.largest, limit)
	r.mu.Unlock()

	return r.Store.ClaimNotifications(ctx, nodeID, esmeID, now, lease, limit)
}

func TestNotificationsInFlightStayWithinTheWindow(t *testing.T) {
	env := newTestEnv(t, nil)
	recorder := &claimRecorder{Store: env.service.Store}
	env.service.Store = recorder
	c := env.run(t)
	conn := env.bind(1, Receiver, env.esme)

	ids := make([]int64, 200)
	for i := range ids {
		ids[i] = env.stored(t, with(baseSubmit(0x04, "aabb"), func(m *smpp.Message) { m.RegisteredDelivery = 0x01 }))
	}

	for _, id := range ids {
		env.complete(t, id, db.StatusDelivered, 0, "", "", c.Now())
	}

	for range ids {
		conn.next(t)
	}

	for _, id := range ids {
		waitResult(t, env, id, db.NotificationReceipt, db.NotificationAcknowledged)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	if recorder.largest > window {
		t.Fatalf("%d notifications claimed at once, want at most the window of %d", recorder.largest, window)
	}
}
