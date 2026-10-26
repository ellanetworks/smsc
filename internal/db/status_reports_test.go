package db

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

var testServiceCentre = Address{Digits: "15550000000", TypeOfNumber: 1, NumberingPlan: 1}

func testReport(at time.Time) *NewStatusReport {
	return &NewStatusReport{
		Originator: testServiceCentre,
		Status:     0x00,
		TPDU:       []byte{0x06, 0x07},
		CreatedAt:  at,
		ExpiresAt:  at.Add(7 * 24 * time.Hour),
	}
}

func TestCompleteMessageStoresStatusReport(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	submitted := time.Date(2026, 9, 27, 12, 0, 0, 300*int(time.Millisecond), time.UTC)
	delivered := submitted.Add(time.Minute)

	id, err := d.CreateMessage(ctx, testMessage(7, false, []byte{0x01}, submitted))
	if err != nil {
		t.Fatal(err)
	}

	report := testReport(delivered)
	report.Status = 0x46

	if err := d.CompleteMessage(ctx, id, StatusDelivered, delivered, report); err != nil {
		t.Fatalf("CompleteMessage: %v", err)
	}

	reports, err := listStatusReports(ctx, d, id)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %+v, %v; want one", reports, err)
	}

	r := reports[0]
	if r.Kind != KindStatusReport || r.ReportFor != id || r.ReportStatus != 0x46 || r.Status != StatusPending {
		t.Fatalf("report = %+v", r)
	}

	if r.Originator != testServiceCentre || r.Recipient != testOriginator || r.MSISDN != testOriginator.Digits ||
		r.Origin != OriginMobile || r.MessageReference != 7 || !bytes.Equal(r.TPDU, report.TPDU) || r.SingleShot {
		t.Fatalf("report = %+v", r)
	}

	if !r.SubmittedAt.Equal(delivered) || !r.TimeStamp.Equal(submitted) || !r.NextAttemptAt.Equal(delivered) ||
		!r.ExpiresAt.Equal(report.ExpiresAt) {
		t.Fatalf("report times = %+v", r)
	}

	if got, err := d.GetMessage(ctx, r.ID); err != nil || got.ReportFor != id {
		t.Fatalf("GetMessage = %+v, %v", got, err)
	}

	// The report is delivered to the originator, as a message to it is.
	if next, ok, err := d.NextDueFor(ctx, testOriginator.Digits, 0, delivered); err != nil || !ok || next.ID != r.ID {
		t.Fatalf("NextDueFor = %+v, %v, %v; want the report", next, ok, err)
	}
}

func TestStatusReportJoinsTheRecipientsQueue(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	// A message to the originator waits for a retry, which holds the originator.
	toOriginator := testMessage(1, false, []byte{0x01}, base)
	toOriginator.Originator, toOriginator.Recipient, toOriginator.MSISDN = testRecipient, testOriginator, testOriginator.Digits

	held, err := d.CreateMessage(ctx, toOriginator)
	if err != nil {
		t.Fatal(err)
	}

	until := base.Add(time.Hour)
	if err := d.ScheduleRetry(ctx, held, until, base, true); err != nil {
		t.Fatal(err)
	}

	id, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, base.Add(time.Minute), testReport(base.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}

	reports, err := listStatusReports(ctx, d, id)
	if err != nil || len(reports) != 1 || !reports[0].NextAttemptAt.Equal(until) {
		t.Fatalf("reports = %+v, %v; want one held until %v", reports, err, until)
	}

	// A report takes none of its recipient's time stamps: the next message to the originator in the same second gets
	// the first free one after the held message's.
	next := toOriginator
	next.SubmittedAt = base.Add(time.Minute)

	nextID, err := d.CreateMessage(ctx, next)
	if err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, nextID); err != nil || !m.TimeStamp.Equal(base.Add(time.Minute)) {
		t.Fatalf("time stamp = %v, %v; want %v", m.TimeStamp, err, base.Add(time.Minute))
	}
}

func TestStatusReportOnlyOnMessagesFromAnMS(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	fromAPI := testMessage(0, false, []byte{0x01}, now)
	fromAPI.Origin = OriginAPI

	id, err := d.CreateMessage(ctx, fromAPI)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, now, testReport(now)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	if m, err := d.GetMessage(ctx, id); err != nil || m.Status != StatusPending {
		t.Fatalf("message = %+v, %v; want it still pending, since its completion failed", m, err)
	}

	// Nor on a status report.
	mobile, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, mobile, StatusDelivered, now, testReport(now)); err != nil {
		t.Fatal(err)
	}

	reports, err := listStatusReports(ctx, d, mobile)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %+v, %v", reports, err)
	}

	if err := d.CompleteMessage(ctx, reports[0].ID, StatusDelivered, now, testReport(now)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("report on a report: err = %v, want ErrNotFound", err)
	}
}

func TestMessageQueriesLeaveOutStatusReports(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	id, err := d.CreateMessage(ctx, testMessage(5, false, []byte{0x01}, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, now, testReport(now)); err != nil {
		t.Fatal(err)
	}

	for name, f := range map[string]MessageFilter{
		"all":                   {},
		"to the originator":     {MSISDN: testOriginator.Digits},
		"from the SC":           {Originator: testServiceCentre.Digits},
		"pending (the reports)": {Status: StatusPending},
	} {
		messages, total, err := d.ListMessages(ctx, f, 1, 10)
		if err != nil {
			t.Fatal(err)
		}

		for _, m := range messages {
			if m.Kind != KindMessage {
				t.Errorf("%s: listed %+v", name, m)
			}
		}

		if total != len(messages) {
			t.Errorf("%s: total = %d, listed %d", name, total, len(messages))
		}
	}
}

func TestSchemaKeepsStatusReportsConsistent(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, now, testReport(now)); err != nil {
		t.Fatal(err)
	}

	for name, statement := range map[string]string{
		"report without its message":  `UPDATE messages SET report_for = NULL WHERE kind = 'status_report'`,
		"report without its status":   `UPDATE messages SET report_status = NULL WHERE kind = 'status_report'`,
		"message on another message":  `UPDATE messages SET report_for = id WHERE kind = 'message'`,
		"report replaced":             `UPDATE messages SET status = 'replaced' WHERE kind = 'status_report'`,
		"status out of range":         `UPDATE messages SET report_status = 256 WHERE kind = 'status_report'`,
		"report on a missing message": `UPDATE messages SET report_for = 42 WHERE kind = 'status_report'`,
		"unknown kind":                `UPDATE messages SET kind = 'command' WHERE kind = 'message'`,
	} {
		if _, err := d.conn.ExecContext(ctx, statement); err == nil {
			t.Errorf("%s: no constraint failed", name)
		}
	}
}

// listStatusReports returns the status reports on a message, oldest first.
func listStatusReports(ctx context.Context, d *DB, messageID int64) ([]Message, error) {
	reports, err := d.StatusReportsFor(ctx, []int64{messageID})

	return reports[messageID], err
}
