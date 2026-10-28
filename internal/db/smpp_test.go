package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// notificationRow is a row of smpp_notifications, as stored, with the ESME of its message.
type notificationRow struct {
	ID, MessageID, ESMEID int64
	Type                  NotificationType
	TPStatus              sql.Null[uint8]
	AttemptID             sql.Null[int64]
	ExpiresAt, NextAt     int64
	Attempts              int
	Result                sql.Null[NotificationResult]
}

func notificationRows(t *testing.T, d *DB, messageID int64) []notificationRow {
	t.Helper()

	rows, err := d.conn.QueryContext(context.Background(), `SELECT n.id, n.message_id, s.esme_id, n.type, n.tp_status,
		n.attempt_id, n.expires_at, n.next_attempt_at, n.attempts, n.result
		FROM smpp_notifications n JOIN smpp_messages s ON s.message_id = n.message_id
		WHERE n.message_id = ? ORDER BY n.id`, messageID)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var out []notificationRow

	for rows.Next() {
		var n notificationRow
		if err := rows.Scan(&n.ID, &n.MessageID, &n.ESMEID, &n.Type, &n.TPStatus, &n.AttemptID, &n.ExpiresAt, &n.NextAt,
			&n.Attempts, &n.Result); err != nil {
			t.Fatal(err)
		}

		out = append(out, n)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func notificationByID(t *testing.T, d *DB, id int64) notificationRow {
	t.Helper()

	var messageID int64
	if err := d.conn.QueryRowContext(context.Background(), `SELECT message_id FROM smpp_notifications WHERE id = ?`,
		id).Scan(&messageID); err != nil {
		t.Fatal(err)
	}

	for _, n := range notificationRows(t, d, messageID) {
		if n.ID == id {
			return n
		}
	}

	t.Fatalf("no notification %d", id)

	return notificationRow{}
}

// smppFixture is a database with an ESME, owned by a Submitter.
type smppFixture struct {
	d      *DB
	user   int64
	esme   int64
	origin time.Time
}

func newSMPPFixture(t *testing.T) smppFixture {
	t.Helper()

	d := openTestDB(t)
	user := createTestUser(t, d, "ota@example.com", RoleSubmitter)

	return smppFixture{
		d: d, user: user, esme: createTestESME(t, d, testESME("ota", user)),
		origin: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
	}
}

// submit stores a message from the fixture's ESME, submitted at the fixture's origin.
func (f smppFixture) submit(t *testing.T, esme int64, receipt uint8, simAnswer bool) int64 {
	t.Helper()

	id, err := f.d.CreateMessage(context.Background(), smppMessage(esme, f.user, receipt, simAnswer, f.origin))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	return id
}

// attempt stores a delivery attempt on a message, with an SMS-DELIVER-REPORT when report is not nil.
func (f smppFixture) attempt(t *testing.T, id int64, outcome string, report []byte) int64 {
	t.Helper()

	attemptID, err := f.d.CreateDeliveryAttempt(context.Background(), DeliveryAttempt{
		MessageID: id, StartedAt: f.origin, CompletedAt: f.origin, Step: StepDelivery, Node: "mme.example.org",
		NodeType: NodeTypeMME, Outcome: outcome, DeliverReport: report,
	})
	if err != nil {
		t.Fatalf("CreateDeliveryAttempt: %v", err)
	}

	return attemptID
}

const defaultValidity = 604800 * time.Second

func TestCompleteMessageQueuesNotifications(t *testing.T) {
	report := []byte{0x00, 0x07, 0x7f, 0xf6, 0x03, 0x02, 0x71, 0x00}

	// attempts stores the delivery attempts of a message, and returns the ID of the one whose report a SIM answer
	// forwards, or 0 when there is none to forward.
	attemptCases := []struct {
		name     string
		attempts func(t *testing.T, f smppFixture, id int64) int64
	}{
		{"no attempt", func(*testing.T, smppFixture, int64) int64 { return 0 }},
		{"a report on the last attempt", func(t *testing.T, f smppFixture, id int64) int64 {
			f.attempt(t, id, "absent_subscriber", nil)
			return f.attempt(t, id, "success", report)
		}},
		{"a report on an earlier attempt only", func(t *testing.T, f smppFixture, id int64) int64 {
			f.attempt(t, id, "error", report)
			f.attempt(t, id, "absent_subscriber", nil)

			return 0
		}},
	}

	statuses := []struct {
		status MessageStatus
		state  uint8
	}{
		{StatusDelivered, 2},
		{StatusFailed, 5},
		{StatusExpired, 3},
	}

	for _, receipt := range []uint8{ReceiptNone, ReceiptFinal, ReceiptFailure} {
		for _, s := range statuses {
			for _, simAnswer := range []bool{false, true} {
				for _, a := range attemptCases {
					name := fmt.Sprintf("receipt %d/%s/sim answer %t/%s", receipt, s.status, simAnswer, a.name)

					t.Run(name, func(t *testing.T) {
						ctx := context.Background()
						f := newSMPPFixture(t)

						id := f.submit(t, f.esme, receipt, simAnswer)
						answered := a.attempts(t, f, id)

						at := f.origin.Add(time.Minute)
						if err := f.d.CompleteMessage(ctx, id, s.status, at, nil, 0x42); err != nil {
							t.Fatalf("CompleteMessage: %v", err)
						}

						wantAnswer := simAnswer && answered != 0
						wantReceipt := receipt == ReceiptFinal || receipt == ReceiptFailure && s.status != StatusDelivered

						rows := notificationRows(t, f.d, id)

						var want []NotificationType
						if wantAnswer {
							want = append(want, NotificationSIMAnswer)
						}

						if wantReceipt {
							want = append(want, NotificationReceipt)
						}

						var got []NotificationType
						for _, n := range rows {
							got = append(got, n.Type)
						}

						// The rows are in id order, so the SIM answer comes first.
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("notifications = %v, want %v", got, want)
						}

						for _, n := range rows {
							if n.ESMEID != f.esme || n.NextAt != at.UnixNano() ||
								n.ExpiresAt != at.Add(defaultValidity).UnixNano() || n.Attempts != 0 || n.Result.Valid {
								t.Errorf("notification = %+v", n)
							}

							switch n.Type {
							case NotificationSIMAnswer:
								if n.AttemptID != (sql.Null[int64]{V: answered, Valid: true}) || n.TPStatus.Valid {
									t.Errorf("SIM answer = %+v, want attempt %d", n, answered)
								}
							case NotificationReceipt:
								if n.TPStatus != (sql.Null[uint8]{V: 0x42, Valid: true}) || n.AttemptID.Valid {
									t.Errorf("receipt = %+v, want tp_status 0x42", n)
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestCompleteMessageNotificationsExpireAfterTheDefaultValidity(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)

	if _, err := f.d.conn.ExecContext(ctx, `UPDATE delivery SET default_validity = ?`, int64(time.Hour)); err != nil {
		t.Fatal(err)
	}

	id := f.submit(t, f.esme, ReceiptFinal, false)
	at := f.origin.Add(time.Minute)

	if err := f.d.CompleteMessage(ctx, id, StatusDelivered, at, nil, 0); err != nil {
		t.Fatal(err)
	}

	if rows := notificationRows(t, f.d, id); len(rows) != 1 || rows[0].ExpiresAt != at.Add(time.Hour).UnixNano() {
		t.Fatalf("notifications = %+v, want one expiring an hour after completion", rows)
	}
}

func TestCompleteMessageQueuesNothingForOtherOrigins(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)

	m := testMessage(1, false, []byte{0x01}, f.origin)
	m.Origin, m.SubmittedBy = OriginAPI, f.user

	id, err := f.d.CreateMessage(ctx, m)
	if err != nil {
		t.Fatal(err)
	}

	f.attempt(t, id, "success", []byte{0x00, 0x00})

	if err := f.d.CompleteMessage(ctx, id, StatusFailed, f.origin, nil, 0x42); err != nil {
		t.Fatal(err)
	}

	if n := count(t, f.d, `smpp_notifications`); n != 0 {
		t.Fatalf("notifications = %d, want 0", n)
	}
}

// TestCompleteMessageQueuesInItsTransaction checks that a failure to queue a notification leaves the message
// pending, with no status report and no other notification.
func TestCompleteMessageQueuesInItsTransaction(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)

	id := f.submit(t, f.esme, ReceiptFinal, true)
	f.attempt(t, id, "success", []byte{0x00, 0x07})

	// The SIM answer goes in first; the receipt after it fails.
	if _, err := f.d.conn.ExecContext(ctx, `CREATE TRIGGER fail_receipt BEFORE INSERT ON smpp_notifications
		WHEN NEW.type = 'receipt' BEGIN SELECT RAISE(ABORT, 'forced failure'); END`); err != nil {
		t.Fatal(err)
	}

	at := f.origin.Add(time.Minute)

	if err := f.d.CompleteMessage(ctx, id, StatusDelivered, at, nil, 0); err == nil {
		t.Fatal("CompleteMessage succeeded")
	}

	if m, err := f.d.GetMessage(ctx, id); err != nil || m.Status != StatusPending || !m.UpdatedAt.Equal(f.origin) {
		t.Fatalf("message = %+v, %v; want it left pending", m, err)
	}

	if n := count(t, f.d, `smpp_notifications`); n != 0 {
		t.Fatalf("notifications = %d, want 0: the SIM answer must roll back with the receipt", n)
	}

	if _, err := f.d.conn.ExecContext(ctx, `DROP TRIGGER fail_receipt`); err != nil {
		t.Fatal(err)
	}

	if err := f.d.CompleteMessage(ctx, id, StatusDelivered, at, nil, 0); err != nil {
		t.Fatalf("CompleteMessage once the trigger is gone: %v", err)
	}

	if rows := notificationRows(t, f.d, id); len(rows) != 2 {
		t.Fatalf("notifications = %+v, want 2", rows)
	}

	// A second completion is refused and queues nothing more.
	if err := f.d.CompleteMessage(ctx, id, StatusFailed, at, nil, 0); !errors.Is(err, ErrNotPending) {
		t.Fatalf("CompleteMessage twice = %v, want ErrNotPending", err)
	}

	if rows := notificationRows(t, f.d, id); len(rows) != 2 {
		t.Fatalf("notifications after a refused completion = %+v, want 2", rows)
	}
}

func TestReplacedSMPPMessageGetsADeletedReceipt(t *testing.T) {
	for _, tt := range []struct {
		receipt     uint8
		wantReceipt bool
	}{
		{ReceiptNone, false},
		{ReceiptFinal, true},
		{ReceiptFailure, true},
	} {
		t.Run(fmt.Sprintf("receipt %d", tt.receipt), func(t *testing.T) {
			ctx := context.Background()
			f := newSMPPFixture(t)

			original := replaceType(smppMessage(f.esme, f.user, tt.receipt, true, f.origin))

			id, err := f.d.CreateMessage(ctx, original)
			if err != nil {
				t.Fatal(err)
			}

			// A report on the attempt would be a SIM answer, but a replaced message gets none.
			f.attempt(t, id, "error", []byte{0x00, 0x07})

			replacedAt := f.origin.Add(time.Minute)

			replacementID, err := f.d.CreateMessage(ctx, replaceType(smppMessage(f.esme, f.user, tt.receipt, true,
				replacedAt)))
			if err != nil {
				t.Fatal(err)
			}

			if m, err := f.d.GetMessage(ctx, id); err != nil || m.Status != StatusReplaced {
				t.Fatalf("original = %+v, %v; want it replaced", m, err)
			}

			rows := notificationRows(t, f.d, id)
			if !tt.wantReceipt {
				if len(rows) != 0 {
					t.Fatalf("notifications = %+v, want none", rows)
				}

				return
			}

			if len(rows) != 1 {
				t.Fatalf("notifications = %+v, want one receipt", rows)
			}

			n := rows[0]
			if n.Type != NotificationReceipt || n.TPStatus != (sql.Null[uint8]{V: 2, Valid: true}) ||
				n.ExpiresAt != replacedAt.Add(defaultValidity).UnixNano() || n.ESMEID != f.esme {
				t.Fatalf("receipt = %+v, want TP-Status 2", n)
			}

			// Its message_state is that of the message's status, replaced: DELETED.
			if due, err := f.d.DueNotifications(ctx, f.esme, replacedAt, nil, 10); err != nil || len(due) != 1 ||
				due[0].MessageState != 4 {
				t.Fatalf("DueNotifications = %+v, %v; want DELETED (4)", due, err)
			}

			if rows := notificationRows(t, f.d, replacementID); len(rows) != 0 {
				t.Fatalf("notifications on the replacement = %+v, want none", rows)
			}
		})
	}
}

func TestDueNotifications(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)
	d := f.d

	otherESME := createTestESME(t, d, testESME("other", f.user))
	report := []byte{0x00, 0x07, 0x7f, 0xf6, 0x03, 0x02, 0x71, 0x00}
	reference := uint16(0x1234)

	// answered: a SIM answer, then a receipt.
	m := smppMessage(f.esme, f.user, ReceiptFinal, true, f.origin)
	m.SMPP.UserMessageReference = &reference

	answered, err := d.CreateMessage(ctx, m)
	if err != nil {
		t.Fatal(err)
	}

	f.attempt(t, answered, "success", report)

	t0 := f.origin.Add(time.Minute)
	if err := d.CompleteMessage(ctx, answered, StatusDelivered, t0, nil, 0); err != nil {
		t.Fatal(err)
	}

	// failed: a receipt, completed a second later.
	failed := f.submit(t, f.esme, ReceiptFailure, false)
	if err := d.CompleteMessage(ctx, failed, StatusFailed, t0.Add(time.Second), nil, 0x42); err != nil {
		t.Fatal(err)
	}

	// other: a receipt to another ESME.
	other := f.submit(t, otherESME, ReceiptFinal, false)
	if err := d.CompleteMessage(ctx, other, StatusDelivered, t0, nil, 0); err != nil {
		t.Fatal(err)
	}

	answerRows, failedRows, otherRows := notificationRows(t, d, answered), notificationRows(t, d, failed),
		notificationRows(t, d, other)
	if len(answerRows) != 2 || len(failedRows) != 1 || len(otherRows) != 1 {
		t.Fatalf("rows = %+v, %+v, %+v", answerRows, failedRows, otherRows)
	}

	simAnswer, answerReceipt, failedReceipt := answerRows[0].ID, answerRows[1].ID, failedRows[0].ID

	ids := func(t *testing.T, esme int64, now time.Time, exclude []int64, limit int) []int64 {
		t.Helper()

		ns, err := d.DueNotifications(ctx, esme, now, exclude, limit)
		if err != nil {
			t.Fatalf("DueNotifications: %v", err)
		}

		got := []int64{}
		for _, n := range ns {
			got = append(got, n.ID)
		}

		return got
	}

	later := t0.Add(time.Minute)

	tests := []struct {
		name    string
		esme    int64
		now     time.Time
		exclude []int64
		limit   int
		want    []int64
	}{
		{"before completion", f.esme, t0.Add(-time.Nanosecond), nil, 10, []int64{}},
		{"at the first completion", f.esme, t0, nil, 10, []int64{simAnswer}},
		{"the receipt waits for the SIM answer", f.esme, later, nil, 10, []int64{simAnswer, failedReceipt}},
		{"limit", f.esme, later, nil, 1, []int64{simAnswer}},
		{"in flight left out", f.esme, later, []int64{simAnswer}, 10, []int64{failedReceipt}},
		{"several in flight", f.esme, later, []int64{simAnswer, failedReceipt}, 10, []int64{}},
		{"another ESME", otherESME, later, nil, 10, []int64{otherRows[0].ID}},
		{"an unknown ESME", 42, later, nil, 10, []int64{}},
		{"expired", f.esme, t0.Add(defaultValidity + time.Second), nil, 10, []int64{}},
		{"at expiry", f.esme, t0.Add(defaultValidity), nil, 10, []int64{failedReceipt}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ids(t, tt.esme, tt.now, tt.exclude, tt.limit); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DueNotifications = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("contents", func(t *testing.T) {
		ns, err := d.DueNotifications(ctx, f.esme, later, nil, 10)
		if err != nil || len(ns) != 2 {
			t.Fatalf("DueNotifications = %+v, %v", ns, err)
		}

		a, r := ns[0], ns[1]

		if a.Type != NotificationSIMAnswer || a.ESMEID != f.esme || a.Attempts != 0 || a.MessageState != 0 ||
			a.TPStatus != 0 || a.DataCoding != 0x04 || a.UserMessageReference == nil ||
			*a.UserMessageReference != reference || !bytes.Equal(a.DeliverReport, report) || !a.Ack {
			t.Errorf("SIM answer = %+v", a)
		}

		if a.Message.ID != answered || a.Message.Originator != testOriginator || a.Message.Recipient != testRecipient ||
			a.Message.MSISDN != testMSISDN || a.Message.Origin != OriginSMPP || a.Message.SubmittedBy != f.user ||
			!bytes.Equal(a.Message.TPDU, []byte{0x41, 0x42}) || a.Message.Status != StatusDelivered ||
			!a.Message.SubmittedAt.Equal(f.origin) || !a.Message.UpdatedAt.Equal(t0) {
			t.Errorf("SIM answer message = %+v", a.Message)
		}

		if r.Type != NotificationReceipt || r.Message.ID != failed || r.MessageState != 5 || r.TPStatus != 0x42 ||
			r.UserMessageReference != nil || r.DeliverReport != nil || r.Ack || r.Message.Status != StatusFailed {
			t.Errorf("receipt = %+v", r)
		}
	})

	t.Run("an RP-ERROR report is not an ack", func(t *testing.T) {
		g := newSMPPFixture(t)

		id := g.submit(t, g.esme, ReceiptNone, true)
		g.attempt(t, id, "error", report)

		if err := g.d.CompleteMessage(ctx, id, StatusFailed, t0, nil, 0); err != nil {
			t.Fatal(err)
		}

		ns, err := g.d.DueNotifications(ctx, g.esme, t0, nil, 10)
		if err != nil || len(ns) != 1 || ns[0].Ack || !bytes.Equal(ns[0].DeliverReport, report) {
			t.Fatalf("DueNotifications = %+v, %v; want one SIM answer without Ack", ns, err)
		}
	})

	// Retrying moves the notification on, and counts the attempt.
	retryAt := later.Add(10 * time.Minute)
	if err := d.RetryNotification(ctx, failedReceipt, retryAt); err != nil {
		t.Fatalf("RetryNotification: %v", err)
	}

	if n := notificationByID(t, d, failedReceipt); n.Attempts != 1 || n.NextAt != retryAt.UnixNano() || n.Result.Valid {
		t.Fatalf("after RetryNotification = %+v", n)
	}

	if got := ids(t, f.esme, later, nil, 10); !reflect.DeepEqual(got, []int64{simAnswer}) {
		t.Fatalf("DueNotifications after a retry = %v, want only the SIM answer", got)
	}

	if ns, err := d.DueNotifications(ctx, f.esme, retryAt, []int64{simAnswer}, 10); err != nil || len(ns) != 1 ||
		ns[0].ID != failedReceipt || ns[0].Attempts != 1 {
		t.Fatalf("DueNotifications once the retry is due = %+v, %v", ns, err)
	}

	// Acknowledging the SIM answer frees the receipt of the same message.
	if err := d.CloseNotification(ctx, simAnswer, NotificationAcknowledged); err != nil {
		t.Fatalf("CloseNotification: %v", err)
	}

	if got := ids(t, f.esme, later.Add(time.Second), nil, 10); !reflect.DeepEqual(got, []int64{answerReceipt}) {
		t.Fatalf("DueNotifications after the SIM answer = %v, want its receipt", got)
	}

	if err := d.CloseNotification(ctx, simAnswer, NotificationRejected); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CloseNotification twice = %v, want ErrNotFound", err)
	}

	if err := d.CloseNotification(ctx, 4242, NotificationAcknowledged); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CloseNotification of an unknown ID = %v, want ErrNotFound", err)
	}

	// A retry of a closed notification changes nothing.
	if err := d.RetryNotification(ctx, simAnswer, retryAt); err != nil {
		t.Fatal(err)
	}

	if n := notificationByID(t, d, simAnswer); n.Attempts != 0 ||
		n.Result != (sql.Null[NotificationResult]{V: NotificationAcknowledged, Valid: true}) {
		t.Fatalf("closed SIM answer = %+v", n)
	}
}

func TestCloseNotification(t *testing.T) {
	for _, result := range []NotificationResult{
		NotificationAcknowledged, NotificationRejected, NotificationExpired, NotificationNoUserData,
	} {
		t.Run(string(result), func(t *testing.T) {
			ctx := context.Background()
			f := newSMPPFixture(t)

			id := f.submit(t, f.esme, ReceiptFinal, false)
			if err := f.d.CompleteMessage(ctx, id, StatusDelivered, f.origin, nil, 0); err != nil {
				t.Fatal(err)
			}

			n := notificationRows(t, f.d, id)[0]

			if err := f.d.CloseNotification(ctx, n.ID, result); err != nil {
				t.Fatalf("CloseNotification: %v", err)
			}

			if n = notificationByID(t, f.d, n.ID); n.Result != (sql.Null[NotificationResult]{V: result, Valid: true}) {
				t.Fatalf("notification = %+v, want result %s", n, result)
			}

			if err := f.d.CloseNotification(ctx, n.ID, result); !errors.Is(err, ErrNotFound) {
				t.Fatalf("CloseNotification twice = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestExpireAndCountNotifications(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)
	d := f.d

	otherESME := createTestESME(t, d, testESME("other", f.user))

	if counts, err := d.CountPendingNotifications(ctx, f.origin); err != nil ||
		!reflect.DeepEqual(counts, map[NotificationType]int{NotificationReceipt: 0, NotificationSIMAnswer: 0}) {
		t.Fatalf("CountPendingNotifications of none = %v, %v", counts, err)
	}

	if _, ok, err := d.NextNotificationAt(ctx, f.esme, f.origin); err != nil || ok {
		t.Fatalf("NextNotificationAt of none = %t, %v", ok, err)
	}

	t0 := f.origin.Add(time.Minute)

	complete := func(esme int64, receipt uint8, simAnswer bool, at time.Time) int64 {
		id := f.submit(t, esme, receipt, simAnswer)
		f.attempt(t, id, "success", []byte{0x00, 0x07})

		if err := d.CompleteMessage(ctx, id, StatusDelivered, at, nil, 0); err != nil {
			t.Fatal(err)
		}

		return id
	}

	first := complete(f.esme, ReceiptFinal, true, t0)
	second := complete(f.esme, ReceiptFinal, false, t0.Add(time.Hour))
	third := complete(otherESME, ReceiptFinal, false, t0)

	if counts, err := d.CountPendingNotifications(ctx, t0); err != nil ||
		!reflect.DeepEqual(counts, map[NotificationType]int{NotificationReceipt: 3, NotificationSIMAnswer: 1}) {
		t.Fatalf("CountPendingNotifications = %v, %v", counts, err)
	}

	if at, ok, err := d.NextNotificationAt(ctx, f.esme, f.origin); err != nil || !ok || !at.Equal(t0) {
		t.Fatalf("NextNotificationAt = %v, %t, %v, want %v", at, ok, err, t0)
	}

	// The rows already due, in flight or waiting behind another, do not count: the next is the second's (SRV-L2).
	if at, ok, err := d.NextNotificationAt(ctx, f.esme, t0); err != nil || !ok || !at.Equal(t0.Add(time.Hour)) {
		t.Fatalf("NextNotificationAt after the first is due = %v, %t, %v, want %v", at, ok, err, t0.Add(time.Hour))
	}

	// A retry later than its expiry: the expiry comes first.
	thirdReceipt := notificationRows(t, d, third)[0].ID
	if err := d.RetryNotification(ctx, thirdReceipt, t0.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if at, ok, err := d.NextNotificationAt(ctx, otherESME, t0); err != nil || !ok || !at.Equal(t0.Add(defaultValidity)) {
		t.Fatalf("NextNotificationAt = %v, %t, %v, want the expiry %v", at, ok, err, t0.Add(defaultValidity))
	}

	// Closed rows do not count.
	firstRows := notificationRows(t, d, first)
	if err := d.CloseNotification(ctx, firstRows[0].ID, NotificationAcknowledged); err != nil {
		t.Fatal(err)
	}

	if counts, err := d.CountPendingNotifications(ctx, t0); err != nil ||
		!reflect.DeepEqual(counts, map[NotificationType]int{NotificationReceipt: 3, NotificationSIMAnswer: 0}) {
		t.Fatalf("CountPendingNotifications after a close = %v, %v", counts, err)
	}

	expiry := t0.Add(defaultValidity)

	// Expired rows do not count, and nothing is expired yet before then.
	if counts, err := d.CountPendingNotifications(ctx, expiry); err != nil ||
		!reflect.DeepEqual(counts, map[NotificationType]int{NotificationReceipt: 1, NotificationSIMAnswer: 0}) {
		t.Fatalf("CountPendingNotifications at expiry = %v, %v", counts, err)
	}

	if expired, err := d.ExpireNotifications(ctx, expiry.Add(-time.Nanosecond)); err != nil || len(expired) != 0 {
		t.Fatalf("ExpireNotifications before expiry = %v, %v, want none", expired, err)
	}

	expired, err := d.ExpireNotifications(ctx, expiry)
	if err != nil || !reflect.DeepEqual(expired, map[NotificationType]int{NotificationReceipt: 2}) {
		t.Fatalf("ExpireNotifications = %v, %v, want 2 receipts", expired, err)
	}

	for _, id := range []int64{firstRows[1].ID, thirdReceipt} {
		if n := notificationByID(t, d, id); n.Result != (sql.Null[NotificationResult]{V: NotificationExpired, Valid: true}) {
			t.Errorf("expired notification = %+v", n)
		}
	}

	if n := notificationByID(t, d, firstRows[0].ID); n.Result.V != NotificationAcknowledged {
		t.Errorf("acknowledged notification = %+v, want it left acknowledged", n)
	}

	if _, ok, err := d.NextNotificationAt(ctx, otherESME, expiry); err != nil || ok {
		t.Fatalf("NextNotificationAt with only closed rows = %t, %v", ok, err)
	}

	if at, ok, err := d.NextNotificationAt(ctx, f.esme, t0); err != nil || !ok || !at.Equal(t0.Add(time.Hour)) {
		t.Fatalf("NextNotificationAt = %v, %t, %v, want the second's", at, ok, err)
	}

	expired, err = d.ExpireNotifications(ctx, expiry.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(expired, map[NotificationType]int{NotificationReceipt: 1}) {
		t.Fatalf("ExpireNotifications = %v, %v, want the second's receipt", expired, err)
	}

	if rows := notificationRows(t, d, second); rows[0].Result.V != NotificationExpired {
		t.Fatalf("second's receipt = %+v", rows[0])
	}
}

func TestSystemIDsFor(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)

	otherESME := createTestESME(t, f.d, testESME("other id", f.user))

	first := f.submit(t, f.esme, ReceiptNone, false)
	second := f.submit(t, otherESME, ReceiptNone, false)
	fromMS := create(t, f.d, f.origin)

	if ids, err := f.d.SystemIDsFor(ctx, nil); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("SystemIDsFor of none = %#v, %v", ids, err)
	}

	ids, err := f.d.SystemIDsFor(ctx, []int64{first, second, fromMS, 4242})
	if err != nil || !reflect.DeepEqual(ids, map[int64]string{first: "ota", second: "other id"}) {
		t.Fatalf("SystemIDsFor = %v, %v", ids, err)
	}
}

func TestPurgeKeepsMessagesWithNotificationsToSend(t *testing.T) {
	cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before := cutoff.Add(-time.Hour)

	tests := []struct {
		name string
		// validity is the default validity, which the notification expires after; 0 keeps the default.
		validity   time.Duration
		close      NotificationResult
		wantPurged bool
	}{
		{name: "open"},
		{name: "open, expiring at the cutoff", validity: time.Hour},
		{name: "open, expired before the cutoff", validity: time.Hour - time.Nanosecond, wantPurged: true},
		{name: "acknowledged", close: NotificationAcknowledged, wantPurged: true},
		{name: "rejected", close: NotificationRejected, wantPurged: true},
		{name: "closed as expired", close: NotificationExpired, wantPurged: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newSMPPFixture(t)
			f.origin = before.Add(-time.Hour)

			if tt.validity != 0 {
				if _, err := f.d.conn.ExecContext(ctx, `UPDATE delivery SET default_validity = ?`,
					int64(tt.validity)); err != nil {
					t.Fatal(err)
				}
			}

			id := f.submit(t, f.esme, ReceiptFinal, false)
			if err := f.d.CompleteMessage(ctx, id, StatusDelivered, before, nil, 0); err != nil {
				t.Fatal(err)
			}

			if tt.close != "" {
				if err := f.d.CloseNotification(ctx, notificationRows(t, f.d, id)[0].ID, tt.close); err != nil {
					t.Fatal(err)
				}
			}

			n, err := f.d.PurgeMessages(ctx, cutoff, 10)
			if err != nil {
				t.Fatalf("PurgeMessages: %v", err)
			}

			if (n == 1) != tt.wantPurged || n > 1 {
				t.Fatalf("PurgeMessages = %d, want purged %t", n, tt.wantPurged)
			}

			if _, err := f.d.GetMessage(ctx, id); errors.Is(err, ErrNotFound) != tt.wantPurged {
				t.Fatalf("GetMessage = %v, want purged %t", err, tt.wantPurged)
			}

			want := 1
			if tt.wantPurged {
				want = 0
			}

			for _, table := range []string{`smpp_messages`, `smpp_notifications`} {
				if got := count(t, f.d, table); got != want {
					t.Errorf("%s = %d, want %d", table, got, want)
				}
			}
		})
	}
}

// TestReplaceIsScopedToTheAccount checks that a replace type from the API or SMPP replaces only a pending message of
// the same account, while one from an MS replaces any of its originator (TS 23.040 clause 9.2.3.9).
func TestReplaceIsScopedToTheAccount(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)
	other := createTestUser(t, f.d, "other@example.com", RoleSubmitter)
	otherESME := createTestESME(t, f.d, testESME("other", other))

	api := func(user int64, at time.Time) NewMessage {
		m := replaceType(testMessage(0, false, []byte{0x01}, at))
		m.Origin, m.SubmittedBy = OriginAPI, user

		return m
	}

	status := func(id int64) MessageStatus {
		t.Helper()

		m, err := f.d.GetMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		return m.Status
	}

	create := func(m NewMessage) int64 {
		t.Helper()

		id, err := f.d.CreateMessage(ctx, m)
		if err != nil {
			t.Fatal(err)
		}

		return id
	}

	// SMPP: another ESME with the same originator does not replace it; the same ESME does.
	smppID := create(replaceType(smppMessage(f.esme, f.user, ReceiptNone, false, f.origin)))
	create(replaceType(smppMessage(otherESME, other, ReceiptNone, false, f.origin.Add(time.Second))))

	if s := status(smppID); s != StatusPending {
		t.Fatalf("SMPP message after another ESME's replace = %s, want pending", s)
	}

	// An API message of the same user as the ESME is another account.
	create(api(f.user, f.origin.Add(2*time.Second)))

	if s := status(smppID); s != StatusPending {
		t.Fatalf("SMPP message after its user's API replace = %s, want pending", s)
	}

	create(replaceType(smppMessage(f.esme, f.user, ReceiptNone, false, f.origin.Add(3*time.Second))))

	if s := status(smppID); s != StatusReplaced {
		t.Fatalf("SMPP message after its ESME's replace = %s, want replaced", s)
	}

	// API: another user does not replace it; the same user does.
	apiID := create(api(other, f.origin.Add(4*time.Second)))
	create(api(f.user, f.origin.Add(5*time.Second)))

	if s := status(apiID); s != StatusPending {
		t.Fatalf("API message after another user's replace = %s, want pending", s)
	}

	latest := create(api(other, f.origin.Add(6*time.Second)))

	if s := status(apiID); s != StatusReplaced {
		t.Fatalf("API message after its user's replace = %s, want replaced", s)
	}

	// An MS follows the plain rule: the latest pending message of the originator and the replace type.
	create(replaceType(testMessage(0, false, []byte{0x01}, f.origin.Add(7*time.Second))))

	if s := status(latest); s != StatusReplaced {
		t.Fatalf("API message after an MS replace = %s, want replaced", s)
	}
}
