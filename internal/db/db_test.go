package db

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var (
	testOriginator = Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1}
	testRecipient  = Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1}
)

const testMSISDN = "15551230002"

func testMessage(reference uint8, rejectDuplicates bool, tpdu []byte, at time.Time) NewMessage {
	return NewMessage{
		Originator:       testOriginator,
		Recipient:        testRecipient,
		MSISDN:           testMSISDN,
		MessageReference: reference,
		RejectDuplicates: rejectDuplicates,
		TPDU:             tpdu,
		SubmittedAt:      at,
		ExpiresAt:        at.Add(7 * 24 * time.Hour),
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()

	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = d.Close() })

	return d
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smsc.db")

	for range 2 {
		d, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMessageLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	submitted := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	tpdu := []byte{0x01, 0x00, 0x0b, 0x91}

	id, err := d.CreateMessage(ctx, testMessage(7, false, tpdu, submitted))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}

	if m.Originator != testOriginator || m.Recipient != testRecipient || !bytes.Equal(m.TPDU, tpdu) ||
		m.MessageReference != 7 {
		t.Fatalf("message = %+v", m)
	}

	if m.Status != StatusPending || !m.SubmittedAt.Equal(submitted) || !m.UpdatedAt.Equal(submitted) {
		t.Fatalf("message = %+v", m)
	}

	delivered := submitted.Add(time.Second)
	if err := d.SetMessageStatus(ctx, id, StatusDelivered, delivered); err != nil {
		t.Fatalf("SetMessageStatus: %v", err)
	}

	m, err = d.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}

	if m.Status != StatusDelivered || !m.UpdatedAt.Equal(delivered) || !m.SubmittedAt.Equal(submitted) {
		t.Fatalf("message = %+v", m)
	}
}

func TestGetMessageNotFound(t *testing.T) {
	if _, err := openTestDB(t).GetMessage(context.Background(), 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSetMessageStatusNotFound(t *testing.T) {
	err := openTestDB(t).SetMessageStatus(context.Background(), 42, StatusFailed, time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSetMessageStatusRejectsUnknownStatus(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.SetMessageStatus(ctx, id, "lost", time.Now()); err == nil {
		t.Fatal("expected an error for an unknown status")
	}
}

func TestDeliveryAttempts(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	first := time.Date(2026, 9, 27, 12, 0, 1, 0, time.UTC)
	second := first.Add(time.Minute)

	if _, err := d.CreateDeliveryAttempt(ctx, id, "mme1.epc.example.org", 5550, first); err != nil {
		t.Fatalf("CreateDeliveryAttempt: %v", err)
	}

	if _, err := d.CreateDeliveryAttempt(ctx, id, "mme2.epc.example.org", 2001, second); err != nil {
		t.Fatalf("CreateDeliveryAttempt: %v", err)
	}

	attempts, err := d.ListDeliveryAttempts(ctx, id)
	if err != nil {
		t.Fatalf("ListDeliveryAttempts: %v", err)
	}

	if len(attempts) != 2 {
		t.Fatalf("got %d attempts, want 2", len(attempts))
	}

	if attempts[0].ServingNode != "mme1.epc.example.org" || attempts[0].ResultCode != 5550 || !attempts[0].AttemptedAt.Equal(first) {
		t.Fatalf("attempts[0] = %+v", attempts[0])
	}

	if attempts[1].ServingNode != "mme2.epc.example.org" || attempts[1].ResultCode != 2001 || !attempts[1].AttemptedAt.Equal(second) {
		t.Fatalf("attempts[1] = %+v", attempts[1])
	}
}

func TestDeliveryAttemptRequiresMessage(t *testing.T) {
	if _, err := openTestDB(t).CreateDeliveryAttempt(context.Background(), 42, "mme1", 2001, time.Now()); err == nil {
		t.Fatal("expected a foreign key error for an unknown message")
	}
}

func TestRejectDuplicates(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("same reference as the previous submission", func(t *testing.T) {
		d := openTestDB(t)

		if _, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now)); err != nil {
			t.Fatal(err)
		}

		other := testMessage(9, true, []byte{0x01}, now)
		other.Recipient.Digits = "15551239999"

		if _, err := d.CreateMessage(ctx, other); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("same reference and destination still held", func(t *testing.T) {
		d := openTestDB(t)

		if _, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now)); err != nil {
			t.Fatal(err)
		}

		if _, err := d.CreateMessage(ctx, testMessage(10, false, []byte{0x01}, now)); err != nil {
			t.Fatal(err)
		}

		if _, err := d.CreateMessage(ctx, testMessage(9, true, []byte{0x01}, now)); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("delivered message no longer held", func(t *testing.T) {
		d := openTestDB(t)

		id, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now))
		if err != nil {
			t.Fatal(err)
		}

		if _, err := d.CreateMessage(ctx, testMessage(10, false, []byte{0x01}, now)); err != nil {
			t.Fatal(err)
		}

		if err := d.SetMessageStatus(ctx, id, StatusDelivered, now); err != nil {
			t.Fatal(err)
		}

		if _, err := d.CreateMessage(ctx, testMessage(9, true, []byte{0x01}, now)); err != nil {
			t.Fatalf("err = %v, want accepted", err)
		}
	})

	t.Run("without TP-RD the repeat is accepted", func(t *testing.T) {
		d := openTestDB(t)

		for range 2 {
			if _, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now)); err != nil {
				t.Fatalf("err = %v, want accepted", err)
			}
		}
	})

	t.Run("different originator is not a duplicate", func(t *testing.T) {
		d := openTestDB(t)

		if _, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now)); err != nil {
			t.Fatal(err)
		}

		other := testMessage(9, true, []byte{0x01}, now)
		other.Originator.Digits = "15551238888"

		if _, err := d.CreateMessage(ctx, other); err != nil {
			t.Fatalf("err = %v, want accepted", err)
		}
	})
}

func TestExpiryAndSingleShotStored(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	submitted := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	withExpiry := testMessage(1, false, []byte{0x01}, submitted)
	withExpiry.ExpiresAt = submitted.Add(time.Hour)
	withExpiry.SingleShot = true
	withExpiry.ProtocolIdentifier = 0x41

	id, err := d.CreateMessage(ctx, withExpiry)
	if err != nil {
		t.Fatal(err)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if !m.ExpiresAt.Equal(submitted.Add(time.Hour)) || !m.SingleShot || m.ProtocolIdentifier != 0x41 {
		t.Fatalf("message = %+v", m)
	}
}

func TestReplaceShortMessage(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	original := testMessage(1, false, []byte{0x01}, now)
	original.ProtocolIdentifier = 0x41
	original.Replace = true

	id, err := d.CreateMessage(ctx, original)
	if err != nil {
		t.Fatal(err)
	}

	replacement := testMessage(2, false, []byte{0x02}, now.Add(time.Minute))
	replacement.ProtocolIdentifier = 0x41
	replacement.Replace = true
	replacement.Recipient.Digits = "15551239999"

	replacedID, err := d.CreateMessage(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}

	if replacedID != id {
		t.Fatalf("replacement stored as %d, want it to replace %d", replacedID, id)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(m.TPDU, []byte{0x02}) || m.Recipient.Digits != "15551239999" || m.MessageReference != 2 ||
		!m.SubmittedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("replaced message = %+v", m)
	}

	otherType := testMessage(3, false, []byte{0x03}, now)
	otherType.ProtocolIdentifier = 0x42
	otherType.Replace = true

	otherID, err := d.CreateMessage(ctx, otherType)
	if err != nil || otherID == id {
		t.Fatalf("different replace type stored as %d, %v; want a new message", otherID, err)
	}

	if err := d.SetMessageStatus(ctx, id, StatusDelivered, now); err != nil {
		t.Fatal(err)
	}

	afterDelivery := testMessage(4, false, []byte{0x04}, now)
	afterDelivery.ProtocolIdentifier = 0x41
	afterDelivery.Replace = true

	newID, err := d.CreateMessage(ctx, afterDelivery)
	if err != nil || newID == id {
		t.Fatalf("replace after delivery stored as %d, %v; want a new message", newID, err)
	}
}

func TestDeliveryScheduling(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	first, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base))
	if err != nil {
		t.Fatal(err)
	}

	second, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := d.NextDue(ctx, base.Add(-time.Second), nil); err != nil || ok {
		t.Fatalf("NextDue before anything is due = %v, %v", ok, err)
	}

	m, ok, err := d.NextDue(ctx, base.Add(time.Minute), nil)
	if err != nil || !ok || m.ID != first {
		t.Fatalf("NextDue = %+v, %v, %v; want message %d", m, ok, err, first)
	}

	if err := d.ScheduleRetry(ctx, first, base.Add(time.Hour), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	m, ok, err = d.NextDue(ctx, base.Add(time.Minute), nil)
	if err != nil || !ok || m.ID != second {
		t.Fatalf("NextDue after retry = %+v, %v, %v; want message %d", m, ok, err, second)
	}

	retried, err := d.GetMessage(ctx, first)
	if err != nil || retried.Retries != 1 || !retried.NextAttemptAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("retried message = %+v, %v", retried, err)
	}

	wakeup, ok, err := d.NextWakeup(ctx, nil)
	if err != nil || !ok || !wakeup.Equal(base.Add(time.Second)) {
		t.Fatalf("NextWakeup = %v, %v, %v", wakeup, ok, err)
	}

	pending, err := d.CountPendingFor(ctx, testMSISDN, first)
	if err != nil || pending != 1 {
		t.Fatalf("CountPendingFor = %d, %v", pending, err)
	}

	if err := d.SetMessageStatus(ctx, second, StatusExpired, base); err != nil {
		t.Fatal(err)
	}

	if err := d.SetMessageStatus(ctx, first, StatusDelivered, base); err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, first, base, base); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ScheduleRetry on a delivered message = %v, want ErrNotFound", err)
	}

	if _, ok, err := d.NextWakeup(ctx, nil); err != nil || ok {
		t.Fatalf("NextWakeup with nothing pending = %v, %v", ok, err)
	}
}

func TestNextDueSkipsBusyRecipients(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	if _, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base)); err != nil {
		t.Fatal(err)
	}

	other := testMessage(2, false, []byte{0x02}, base.Add(time.Second))
	other.MSISDN = "15551230003"

	otherID, err := d.CreateMessage(ctx, other)
	if err != nil {
		t.Fatal(err)
	}

	m, ok, err := d.NextDue(ctx, base.Add(time.Minute), []string{testMSISDN})
	if err != nil || !ok || m.ID != otherID || m.MSISDN != "15551230003" {
		t.Fatalf("NextDue = %+v, %v, %v; want message %d", m, ok, err, otherID)
	}

	if _, ok, err := d.NextDue(ctx, base.Add(time.Minute), []string{testMSISDN, "15551230003"}); err != nil || ok {
		t.Fatalf("NextDue with every recipient busy = %v, %v", ok, err)
	}

	wakeup, ok, err := d.NextWakeup(ctx, []string{testMSISDN})
	if err != nil || !ok || !wakeup.Equal(base.Add(time.Second)) {
		t.Fatalf("NextWakeup = %v, %v, %v", wakeup, ok, err)
	}
}

func TestHoldAndAlertRecipient(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	first, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base))
	if err != nil {
		t.Fatal(err)
	}

	second, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base))
	if err != nil {
		t.Fatal(err)
	}

	later, err := d.CreateMessage(ctx, testMessage(3, false, []byte{0x03}, base))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, later, base.Add(2*time.Hour), base); err != nil {
		t.Fatal(err)
	}

	until := base.Add(time.Hour)
	if err := d.HoldRecipient(ctx, testMSISDN, until, base); err != nil {
		t.Fatal(err)
	}

	for id, want := range map[int64]time.Time{first: until, second: until, later: base.Add(2 * time.Hour)} {
		m, err := d.GetMessage(ctx, id)
		if err != nil || !m.NextAttemptAt.Equal(want) {
			t.Fatalf("message %d next attempt = %v, %v; want %v", id, m.NextAttemptAt, err, want)
		}
	}

	if _, ok, err := d.NextDue(ctx, base.Add(time.Minute), nil); err != nil || ok {
		t.Fatalf("NextDue while held = %v, %v", ok, err)
	}

	alertedAt := base.Add(time.Minute)

	recipients, err := d.AlertRecipient(ctx, testMSISDN, alertedAt)
	if err != nil || len(recipients) != 1 || recipients[0] != testMSISDN {
		t.Fatalf("AlertRecipient = %v, %v", recipients, err)
	}

	for _, id := range []int64{first, second, later} {
		m, err := d.GetMessage(ctx, id)
		if err != nil || !m.NextAttemptAt.Equal(alertedAt) {
			t.Fatalf("message %d next attempt after alert = %v, %v; want %v", id, m.NextAttemptAt, err, alertedAt)
		}
	}
}

func TestAlertMSISDN(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.HoldRecipient(ctx, testMSISDN, base.Add(time.Hour), base); err != nil {
		t.Fatal(err)
	}

	if err := d.SetAlertMSISDN(ctx, testMSISDN, "15559990000", base); err != nil {
		t.Fatal(err)
	}

	if err := d.SetAlertMSISDN(ctx, testMSISDN, "15559990001", base); err != nil {
		t.Fatal(err)
	}

	recipients, err := d.AlertRecipient(ctx, "15559990000", base)
	if err != nil || len(recipients) != 1 {
		t.Fatalf("AlertRecipient with a stale Alert MSISDN = %v, %v", recipients, err)
	}

	recipients, err = d.AlertRecipient(ctx, "15559990001", base.Add(time.Minute))
	if err != nil || len(recipients) != 2 || recipients[1] != testMSISDN {
		t.Fatalf("AlertRecipient by Alert MSISDN = %v, %v", recipients, err)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil || !m.NextAttemptAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("next attempt after alert = %v, %v", m.NextAttemptAt, err)
	}

	if err := d.SetAlertMSISDN(ctx, testMSISDN, "", base); err != nil {
		t.Fatal(err)
	}

	recipients, err = d.AlertRecipient(ctx, "15559990001", base)
	if err != nil || len(recipients) != 1 {
		t.Fatalf("AlertRecipient after clearing the Alert MSISDN = %v, %v", recipients, err)
	}
}

func TestNewMessageJoinsRecipientHold(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	if _, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base)); err != nil {
		t.Fatal(err)
	}

	until := base.Add(time.Hour)
	if err := d.HoldRecipient(ctx, testMSISDN, until, base); err != nil {
		t.Fatal(err)
	}

	held, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	other := testMessage(3, false, []byte{0x03}, base.Add(time.Minute))
	other.MSISDN = "15551230003"

	free, err := d.CreateMessage(ctx, other)
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range map[int64]time.Time{held: until, free: base.Add(time.Minute)} {
		m, err := d.GetMessage(ctx, id)
		if err != nil || !m.NextAttemptAt.Equal(want) {
			t.Fatalf("message %d next attempt = %v, %v; want %v", id, m.NextAttemptAt, err, want)
		}
	}

	if _, err := d.AlertRecipient(ctx, testMSISDN, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, held); err != nil || !m.NextAttemptAt.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("next attempt after alert = %v, %v", m.NextAttemptAt, err)
	}
}

func TestOnlyHoldsDelayNewMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	first, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, first, base.Add(time.Hour), base); err != nil {
		t.Fatal(err)
	}

	afterRetry, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, afterRetry); err != nil || !m.NextAttemptAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("message after a plain retry next attempt = %v, %v; a retry is not a hold", m.NextAttemptAt, err)
	}

	if err := d.HoldRecipient(ctx, testMSISDN, base.Add(2*time.Hour), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := d.HoldRecipient(ctx, testMSISDN, base.Add(90*time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	whileHeld, err := d.CreateMessage(ctx, testMessage(3, false, []byte{0x03}, base.Add(2*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.AlertRecipient(ctx, testMSISDN, base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	afterAlert, err := d.CreateMessage(ctx, testMessage(4, false, []byte{0x04}, base.Add(4*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, afterAlert); err != nil || !m.NextAttemptAt.Equal(base.Add(4*time.Minute)) {
		t.Fatalf("message after the alert next attempt = %v, %v", m.NextAttemptAt, err)
	}

	m, err := d.GetMessage(ctx, whileHeld)
	if err != nil || !m.NextAttemptAt.Equal(base.Add(3*time.Minute)) {
		t.Fatalf("held message next attempt = %v, %v; want the alert time", m.NextAttemptAt, err)
	}
}
