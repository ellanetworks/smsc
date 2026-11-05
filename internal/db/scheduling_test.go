package db

import (
	"context"
	"testing"
	"time"
)

func checkRecipientSchedule(t *testing.T, d *DB) {
	t.Helper()

	rows, err := d.reader().QueryContext(context.Background(), `SELECT r.msisdn, r.next_attempt_at,
		(SELECT MIN(m.next_attempt_at) FROM messages m WHERE m.msisdn = r.msisdn AND m.status = 'pending')
		FROM recipients r`)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			msisdn    string
			got, want *int64
		)

		if err := rows.Scan(&msisdn, &got, &want); err != nil {
			t.Fatal(err)
		}

		if (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Fatalf("recipient %s next attempt = %v, want %v", msisdn, deref(got), deref(want))
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	var missing int
	if err := d.reader().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM messages
		WHERE status = 'pending' AND msisdn NOT IN (SELECT msisdn FROM recipients)`).Scan(&missing); err != nil || missing != 0 {
		t.Fatalf("pending messages without a recipient = %d, %v", missing, err)
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}

	return *p
}

func TestRecipientScheduleFollowsMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	must := func(err error) {
		t.Helper()

		if err != nil {
			t.Fatal(err)
		}

		checkRecipientSchedule(t, d)
	}

	create := func(m NewMessage) int64 {
		t.Helper()

		id, err := d.CreateMessage(ctx, m)
		must(err)

		return id
	}

	first := create(testMessage(1, false, []byte{0x01}, base))
	second := create(testMessage(2, false, []byte{0x02}, base.Add(time.Second)))

	fromOther := testMessage(3, false, []byte{0x03}, base)
	fromOther.Originator, fromOther.MSISDN = testRecipient, testOriginator.Digits
	other := create(fromOther)

	must(d.ScheduleRetry(ctx, first, base.Add(time.Hour), base, false, time.Time{}, Fence{}))
	must(d.ScheduleRetry(ctx, second, base.Add(2*time.Hour), base, true, time.Time{}, Fence{}))

	held := create(testMessage(4, false, []byte{0x04}, base.Add(time.Minute)))

	if m, err := d.GetMessage(ctx, held); err != nil || !m.NextAttemptAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("message to a held recipient next attempt = %v, %v", m.NextAttemptAt, err)
	}

	_, err := d.AlertRecipient(ctx, testMSISDN, base.Add(10*time.Minute))
	must(err)

	replacement := testMessage(5, false, []byte{0x05}, base.Add(20*time.Minute))
	replacement.Replace = true
	create(replacement)

	must(d.CompleteMessage(ctx, first, StatusDelivered, base.Add(30*time.Minute), testReport(base.Add(30*time.Minute)), 0,
		Fence{}))
	must(d.CompleteMessage(ctx, second, StatusExpired, base.Add(30*time.Minute), nil, 0, Fence{}))
	must(d.CompleteMessage(ctx, other, StatusFailed, base.Add(30*time.Minute), nil, 0, Fence{}))

	_, err = d.PurgeMessages(ctx, base.Add(365*24*time.Hour), 100)
	must(err)

	due, _, err := d.DueRecipients(ctx, base.Add(365*24*time.Hour), 10, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, msisdn := range due {
		if _, ok, err := d.NextDue(ctx, msisdn, base.Add(365*24*time.Hour)); err != nil || !ok {
			t.Fatalf("due recipient %s has no due message: %v, %v", msisdn, ok, err)
		}
	}
}

func TestDueRecipientsOrderAndWakeup(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	for i, msisdn := range []string{"15551230005", "15551230004", "15551230003"} {
		m := testMessage(uint8(i), false, []byte{byte(i)}, base.Add(time.Duration(i)*time.Minute))
		m.MSISDN = msisdn

		if _, err := d.CreateMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	due, wakeup, err := d.DueRecipients(ctx, base.Add(time.Minute), 10, func(msisdn string) bool { return msisdn == "15551230005" })
	if err != nil || len(due) != 1 || due[0] != "15551230004" || !wakeup.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("DueRecipients = %v, %v, %v", due, wakeup, err)
	}

	due, wakeup, err = d.DueRecipients(ctx, base.Add(time.Hour), 2, nil)
	if err != nil || len(due) != 2 || due[0] != "15551230005" || due[1] != "15551230004" || !wakeup.IsZero() {
		t.Fatalf("DueRecipients with a limit = %v, %v, %v", due, wakeup, err)
	}
}
