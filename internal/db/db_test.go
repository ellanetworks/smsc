package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		Origin:           OriginMobile,
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

// TestDatabaseIsPrivate checks that only its owner can read the database, which holds the password hashes and the key
// that signs the access tokens, and its journal.
func TestDatabaseIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smsc.db")

	// An existing database that others can read is made private.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.CountUsers(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"smsc.db", "smsc.db-wal", "smsc.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}

		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, mode)
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

	if m.Kind != KindMessage || m.ReportFor != 0 || m.Originator != testOriginator || m.Recipient != testRecipient ||
		!bytes.Equal(m.TPDU, tpdu) || m.MessageReference != 7 || m.Origin != OriginMobile {
		t.Fatalf("message = %+v", m)
	}

	if m.Status != StatusPending || !m.SubmittedAt.Equal(submitted) || !m.UpdatedAt.Equal(submitted) {
		t.Fatalf("message = %+v", m)
	}

	delivered := submitted.Add(time.Second)
	if err := d.CompleteMessage(ctx, id, StatusDelivered, delivered, nil); err != nil {
		t.Fatalf("CompleteMessage: %v", err)
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

func TestCompleteMessageNotFound(t *testing.T) {
	err := openTestDB(t).CompleteMessage(context.Background(), 42, StatusFailed, time.Now(), nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCompleteMessageRejectsOtherStatuses(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []MessageStatus{StatusPending, StatusReplaced, "lost"} {
		if err := d.CompleteMessage(ctx, id, status, time.Now(), nil); err == nil {
			t.Fatalf("expected an error for status %q", status)
		}
	}
}

func TestCompleteMessageOnlyCompletesPendingMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusFailed, now, nil); err != nil {
		t.Fatal(err)
	}

	err = d.CompleteMessage(ctx, id, StatusDelivered, now.Add(time.Second), testReport(now))
	if !errors.Is(err, ErrNotPending) {
		t.Fatalf("err = %v, want ErrNotPending", err)
	}

	if m, err := d.GetMessage(ctx, id); err != nil || m.Status != StatusFailed || !m.UpdatedAt.Equal(now) {
		t.Fatalf("message = %+v, %v; want it left failed", m, err)
	}

	if reports, err := listStatusReports(ctx, d, id); err != nil || len(reports) != 0 {
		t.Fatalf("reports = %+v, %v; want none", reports, err)
	}
}

func u32(v uint32) *uint32 {
	return &v
}

func TestDeliveryAttempts(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	first := time.Date(2026, 9, 27, 12, 0, 1, 0, time.UTC)

	want := []DeliveryAttempt{
		{MessageID: id, StartedAt: first, CompletedAt: first.Add(30 * time.Second), Step: StepRouting, Outcome: "timeout"},
		{
			MessageID: id, StartedAt: first.Add(time.Second), CompletedAt: first.Add(time.Second + 7*time.Millisecond),
			Step: StepRouting, Node: "hss.example.org", Outcome: "success", ResultCode: u32(2001),
		},
		{
			MessageID: id, StartedAt: first.Add(time.Minute), CompletedAt: first.Add(time.Minute + time.Second),
			Step: StepDelivery, Node: "mme1.epc.example.org", NodeType: NodeTypeMME,
			Outcome: "absent_user", ResultCode: u32(5550), VendorID: u32(10415),
			AbsentUserDiagnostics: AbsentUserDiagnostics{MME: "no_paging_response_msc"},
		},
		{
			MessageID: id, StartedAt: first.Add(2 * time.Minute), CompletedAt: first.Add(2*time.Minute + time.Millisecond),
			Step: StepRouting, Node: "hss.example.org", Outcome: "absent_user",
			ResultCode: u32(5550), VendorID: u32(10415),
			AbsentUserDiagnostics: AbsentUserDiagnostics{
				MME: "imsi_detached", MSC: "roaming_restriction", SGSN: "gprs_detached",
				SMSF3GPP: "ms_purged_non_gprs", SMSFNon3GPP: "unknown_99",
			},
		},
		{
			MessageID: id, StartedAt: first.Add(3 * time.Minute), CompletedAt: first.Add(3*time.Minute + time.Second),
			Step: StepDelivery, Node: "smsf.5gc.example.org", NodeType: NodeTypeSMSF3GPP,
			Outcome: "sm_delivery_failure", ResultCode: u32(5555), VendorID: u32(10415),
			FailureCause: "equipment_protocol_error", TPFailureCause: "usim_sms_storage_full",
			DeliverReport: []byte{0x00, 0xd0, 0x00},
		},
		{
			MessageID: id, StartedAt: first.Add(4 * time.Minute), CompletedAt: first.Add(4*time.Minute + time.Second),
			Step: StepDelivery, Node: "smsf.5gc.example.org", NodeType: NodeTypeSMSF3GPP,
			Outcome: "success", ResultCode: u32(2001), DeliverReport: []byte{0x00, 0x04, 0x02, 0xca, 0xfe},
		},
	}

	for i := range want {
		aid, err := d.CreateDeliveryAttempt(ctx, want[i])
		if err != nil {
			t.Fatalf("CreateDeliveryAttempt: %v", err)
		}

		want[i].ID = aid
	}

	attempts, total, err := d.ListDeliveryAttempts(ctx, id, 1, 10)
	if err != nil {
		t.Fatalf("ListDeliveryAttempts: %v", err)
	}

	if !reflect.DeepEqual(attempts, want) || total != len(want) {
		t.Fatalf("attempts = %+v (total %d), want %+v", attempts, total, want)
	}

	page, total, err := d.ListDeliveryAttempts(ctx, id, 2, 2)
	if err != nil || total != len(want) || !reflect.DeepEqual(page, want[2:4]) {
		t.Fatalf("second page = %+v (total %d), %v", page, total, err)
	}

	none, total, err := d.ListDeliveryAttempts(ctx, id+1, 1, 10)
	if err != nil || total != 0 || none == nil || len(none) != 0 {
		t.Fatalf("attempts of another message = %+v (total %d), %v", none, total, err)
	}

	reports, err := d.ListDeliverReports(ctx, id)
	if err != nil || !reflect.DeepEqual(reports, want[4:]) {
		t.Fatalf("deliver reports = %+v, %v; want %+v", reports, err, want[4:])
	}

	if none, err := d.ListDeliverReports(ctx, id+1); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("deliver reports of another message = %+v, %v", none, err)
	}
}

func TestDeliveryAttemptRequiresMessage(t *testing.T) {
	a := DeliveryAttempt{MessageID: 42, StartedAt: time.Now(), CompletedAt: time.Now(), Step: StepDelivery, Node: "mme1", Outcome: "success"}
	if _, err := openTestDB(t).CreateDeliveryAttempt(context.Background(), a); err == nil {
		t.Fatal("expected a foreign key error for an unknown message")
	}
}

func TestDeliveryAttemptRejectsUnknownStep(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateDeliveryAttempt(ctx, DeliveryAttempt{MessageID: id, StartedAt: time.Now(), CompletedAt: time.Now(), Step: "report", Outcome: "success"}); err == nil {
		t.Fatal("expected an error for an unknown step")
	}

	if _, err := d.CreateDeliveryAttempt(ctx, DeliveryAttempt{
		MessageID: id, StartedAt: time.Now(), CompletedAt: time.Now(), Step: StepDelivery, NodeType: "msc", Outcome: "success",
	}); err == nil {
		t.Fatal("expected an error for an unknown node type")
	}
}

func TestDuplicateCheckIgnoresAPIMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Now()

	api := testMessage(0, false, []byte{0x01}, now)
	api.Origin = OriginAPI

	if _, err := d.CreateMessage(ctx, api); err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateMessage(ctx, testMessage(0, true, []byte{0x02}, now)); err != nil {
		t.Fatalf("CreateMessage = %v; an API message is not a previous SMS-SUBMIT from the phone", err)
	}

	if _, err := d.CreateMessage(ctx, testMessage(0, true, []byte{0x03}, now)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("CreateMessage = %v, want ErrDuplicate for the phone's own repeat", err)
	}
}

func TestMessageOriginIsRequired(t *testing.T) {
	m := testMessage(1, false, []byte{0x01}, time.Now())
	m.Origin = ""

	if _, err := openTestDB(t).CreateMessage(context.Background(), m); err == nil {
		t.Fatal("expected an error for a message without an origin")
	}
}

func TestCreateMessagesIsAtomic(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Now()

	if _, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now)); err != nil {
		t.Fatal(err)
	}

	_, err := d.CreateMessages(ctx, []NewMessage{
		testMessage(1, false, []byte{0x01}, now),
		testMessage(9, true, []byte{0x01}, now),
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}

	if _, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10); err != nil || total != 1 {
		t.Fatalf("total = %d, %v; a failed batch must store nothing", total, err)
	}

	ids, err := d.CreateMessages(ctx, []NewMessage{testMessage(1, false, []byte{0x01}, now), testMessage(2, false, []byte{0x02}, now)})
	if err != nil || len(ids) != 2 || ids[1] != ids[0]+1 {
		t.Fatalf("ids = %v, %v", ids, err)
	}
}

func TestListMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Now()

	toBob := testMessage(1, false, []byte{0x01}, now)
	toCarol := testMessage(2, false, []byte{0x01}, now)
	toCarol.MSISDN = "15551230003"
	fromCarol := testMessage(3, false, []byte{0x01}, now)
	fromCarol.Originator.Digits = "15551230003"

	ids, err := d.CreateMessages(ctx, []NewMessage{toBob, toCarol, fromCarol, toBob})
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, ids[3], StatusDelivered, now, nil); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		filter        MessageFilter
		page, perPage int
		want          []int64
		total         int
	}{
		"all newest first": {MessageFilter{}, 1, 10, []int64{ids[3], ids[2], ids[1], ids[0]}, 4},
		"by recipient":     {MessageFilter{MSISDN: testMSISDN}, 1, 10, []int64{ids[3], ids[2], ids[0]}, 3},
		"by originator":    {MessageFilter{Originator: "15551230003"}, 1, 10, []int64{ids[2]}, 1},
		"by status":        {MessageFilter{MSISDN: testMSISDN, Status: StatusPending}, 1, 10, []int64{ids[2], ids[0]}, 2},
		"second page":      {MessageFilter{}, 2, 3, []int64{ids[0]}, 4},
		"past the end":     {MessageFilter{}, 3, 3, []int64{}, 4},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			messages, total, err := d.ListMessages(ctx, tc.filter, tc.page, tc.perPage)
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}

			got := []int64{}
			for _, m := range messages {
				got = append(got, m.ID)
			}

			if !reflect.DeepEqual(got, tc.want) || total != tc.total {
				t.Fatalf("ids = %v (total %d), want %v (total %d)", got, total, tc.want, tc.total)
			}
		})
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

		if err := d.CompleteMessage(ctx, id, StatusDelivered, now, nil); err != nil {
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

func replaceType(m NewMessage) NewMessage {
	m.ProtocolIdentifier = 0x41
	m.Replace = true

	return m
}

func TestReplaceShortMessage(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	id, err := d.CreateMessage(ctx, replaceType(testMessage(1, false, []byte{0x01}, now)))
	if err != nil {
		t.Fatal(err)
	}

	// Only the originator and the replace type are checked: a message to another recipient replaces it too.
	replacement := replaceType(testMessage(2, false, []byte{0x02}, now.Add(time.Minute)))
	replacement.Recipient, replacement.MSISDN = Address{Digits: "15551239999", TypeOfNumber: 1, NumberingPlan: 1}, "15551239999"

	replacementID, err := d.CreateMessage(ctx, replacement)
	if err != nil || replacementID == id {
		t.Fatalf("replacement stored as %d, %v; want a new message", replacementID, err)
	}

	if m, err := d.GetMessage(ctx, id); err != nil || m.Status != StatusReplaced || !bytes.Equal(m.TPDU, []byte{0x01}) ||
		m.MessageReference != 1 || !m.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("replaced message = %+v, %v", m, err)
	}

	if m, err := d.GetMessage(ctx, replacementID); err != nil || m.Status != StatusPending ||
		!bytes.Equal(m.TPDU, []byte{0x02}) || m.Recipient != replacement.Recipient {
		t.Fatalf("replacement = %+v, %v", m, err)
	}

	tests := map[string]func(NewMessage) NewMessage{
		"another replace type": func(m NewMessage) NewMessage {
			m.ProtocolIdentifier = 0x42
			return m
		},
		"another originator": func(m NewMessage) NewMessage {
			m.Originator.Digits = "15551239999"
			return m
		},
	}

	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := d.CreateMessage(ctx, change(replaceType(testMessage(3, false, []byte{0x03}, now)))); err != nil {
				t.Fatal(err)
			}

			if m, err := d.GetMessage(ctx, replacementID); err != nil || m.Status != StatusPending {
				t.Fatalf("replacement = %+v, %v; want it still pending", m, err)
			}
		})
	}

	if err := d.CompleteMessage(ctx, replacementID, StatusDelivered, now, nil); err != nil {
		t.Fatal(err)
	}

	afterDelivery, err := d.CreateMessage(ctx, replaceType(testMessage(4, false, []byte{0x04}, now)))
	if err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, replacementID); err != nil || m.Status != StatusDelivered || afterDelivery == replacementID {
		t.Fatalf("delivered message = %+v, %v; want it left delivered", m, err)
	}
}

func TestReplacedMessageStatusReport(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	t.Run("stored with the replacement", func(t *testing.T) {
		d := openTestDB(t)

		id, err := d.CreateMessage(ctx, replaceType(testMessage(1, false, []byte{0x01}, now)))
		if err != nil {
			t.Fatal(err)
		}

		var seen Message

		replacement := replaceType(testMessage(2, false, []byte{0x02}, now.Add(time.Minute)))
		replacement.ReportReplaced = func(replaced Message) (*NewStatusReport, error) {
			seen = replaced
			return testReport(now.Add(time.Minute)), nil
		}

		if _, err := d.CreateMessage(ctx, replacement); err != nil {
			t.Fatal(err)
		}

		if seen.ID != id || seen.Status != StatusReplaced || !bytes.Equal(seen.TPDU, []byte{0x01}) {
			t.Fatalf("ReportReplaced got %+v", seen)
		}

		if reports, err := listStatusReports(ctx, d, id); err != nil || len(reports) != 1 {
			t.Fatalf("reports = %+v, %v; want one", reports, err)
		}
	})

	t.Run("none when not wanted", func(t *testing.T) {
		d := openTestDB(t)

		id, err := d.CreateMessage(ctx, replaceType(testMessage(1, false, []byte{0x01}, now)))
		if err != nil {
			t.Fatal(err)
		}

		replacement := replaceType(testMessage(2, false, []byte{0x02}, now))
		replacement.ReportReplaced = func(Message) (*NewStatusReport, error) { return nil, nil }

		if _, err := d.CreateMessage(ctx, replacement); err != nil {
			t.Fatal(err)
		}

		if reports, err := listStatusReports(ctx, d, id); err != nil || len(reports) != 0 {
			t.Fatalf("reports = %+v, %v; want none", reports, err)
		}
	})

	t.Run("a failure stores nothing", func(t *testing.T) {
		d := openTestDB(t)

		id, err := d.CreateMessage(ctx, replaceType(testMessage(1, false, []byte{0x01}, now)))
		if err != nil {
			t.Fatal(err)
		}

		replacement := replaceType(testMessage(2, false, []byte{0x02}, now))
		replacement.ReportReplaced = func(Message) (*NewStatusReport, error) { return nil, errors.New("no report") }

		if _, err := d.CreateMessage(ctx, replacement); err == nil {
			t.Fatal("expected an error")
		}

		if m, err := d.GetMessage(ctx, id); err != nil || m.Status != StatusPending {
			t.Fatalf("message = %+v, %v; want it still pending", m, err)
		}

		if messages, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10); err != nil || total != 1 {
			t.Fatalf("messages = %+v, %d, %v; want only the original", messages, total, err)
		}
	})
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

	if err := d.ScheduleRetry(ctx, first, base.Add(time.Hour), base.Add(time.Minute), false); err != nil {
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

	if next, ok, err := d.NextDueFor(ctx, testMSISDN, second, base.Add(time.Minute)); err != nil || ok {
		t.Fatalf("NextDueFor with the other message waiting for its retry = %+v, %v, %v", next, ok, err)
	}

	if next, ok, err := d.NextDueFor(ctx, testMSISDN, first, base.Add(time.Minute)); err != nil || !ok || next.ID != second {
		t.Fatalf("NextDueFor = %+v, %v, %v; want message %d", next, ok, err, second)
	}

	if err := d.CompleteMessage(ctx, second, StatusExpired, base, nil); err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, first, StatusDelivered, base, nil); err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, first, base, base, false); !errors.Is(err, ErrNotFound) {
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

	if err := d.ScheduleRetry(ctx, later, base.Add(2*time.Hour), base, false); err != nil {
		t.Fatal(err)
	}

	until := base.Add(time.Hour)
	if err := d.ScheduleRetry(ctx, first, until, base, true); err != nil {
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

	if err := d.ScheduleRetry(ctx, id, base.Add(time.Hour), base, true); err != nil {
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

	first, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base))
	if err != nil {
		t.Fatal(err)
	}

	until := base.Add(time.Hour)
	if err := d.ScheduleRetry(ctx, first, until, base, true); err != nil {
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

	if err := d.ScheduleRetry(ctx, first, base.Add(time.Hour), base, false); err != nil {
		t.Fatal(err)
	}

	afterRetry, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, afterRetry); err != nil || !m.NextAttemptAt.Equal(base.Add(time.Minute)) {
		t.Fatalf("message after a plain retry next attempt = %v, %v; a retry is not a hold", m.NextAttemptAt, err)
	}

	if err := d.ScheduleRetry(ctx, first, base.Add(2*time.Hour), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, first, base.Add(90*time.Minute), base.Add(time.Minute), true); err != nil {
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

func TestCountPending(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	for i := range uint8(3) {
		if _, err := d.CreateMessage(ctx, testMessage(i, false, []byte{0x01}, now)); err != nil {
			t.Fatal(err)
		}
	}

	// One is delivered, with a status report that is due, one waits for a retry, and one is due.
	if err := d.CompleteMessage(ctx, 1, StatusDelivered, now, testReport(now)); err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, 2, now.Add(time.Minute), now, false); err != nil {
		t.Fatal(err)
	}

	want := map[MessageKind]PendingCount{KindMessage: {Due: 1, Waiting: 1}, KindStatusReport: {Due: 1}}
	if got, err := d.CountPending(ctx, now); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("CountPending = %+v, %v, want %+v", got, err, want)
	}

	// Once its retry is due, the second is due too.
	want[KindMessage] = PendingCount{Due: 2}
	if got, err := d.CountPending(ctx, now.Add(time.Minute)); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("CountPending a minute later = %+v, %v, want %+v", got, err, want)
	}

	if err := d.CompleteMessage(ctx, 4, StatusDelivered, now, nil); err != nil {
		t.Fatal(err)
	}

	// Without pending status reports, their count is zero.
	want[KindStatusReport] = PendingCount{}
	if got, err := d.CountPending(ctx, now.Add(time.Minute)); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("CountPending without reports = %+v, %v, want %+v", got, err, want)
	}
}

func TestScheduleRetryHoldsOnlyWhenAsked(t *testing.T) {
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

	if err := d.ScheduleRetry(ctx, first, base.Add(time.Hour), base, false); err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, second); err != nil || !m.NextAttemptAt.Equal(base) {
		t.Fatalf("other message after a retry without hold = %v, %v; want it still due", m.NextAttemptAt, err)
	}

	if err := d.ScheduleRetry(ctx, second, base.Add(time.Minute), base, true); err != nil {
		t.Fatal(err)
	}

	for id, want := range map[int64]time.Time{first: base.Add(time.Hour), second: base.Add(time.Minute)} {
		if m, err := d.GetMessage(ctx, id); err != nil || !m.NextAttemptAt.Equal(want) {
			t.Fatalf("message %d next attempt = %v, %v; want %v", id, m.NextAttemptAt, err, want)
		}
	}
}

func TestMessagesAreDueNoLaterThanTheyExpire(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	retried := testMessage(1, false, []byte{0x01}, base)
	retried.ExpiresAt = base.Add(30 * time.Minute)

	first, err := d.CreateMessage(ctx, retried)
	if err != nil {
		t.Fatal(err)
	}

	waiting, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, base))
	if err != nil {
		t.Fatal(err)
	}

	expiring := testMessage(3, false, []byte{0x03}, base)
	expiring.ExpiresAt = base.Add(10 * time.Minute)

	held, err := d.CreateMessage(ctx, expiring)
	if err != nil {
		t.Fatal(err)
	}

	until := base.Add(time.Hour)
	if err := d.ScheduleRetry(ctx, first, until, base, true); err != nil {
		t.Fatal(err)
	}

	arriving := testMessage(4, false, []byte{0x04}, base.Add(time.Minute))
	arriving.ExpiresAt = base.Add(20 * time.Minute)

	late, err := d.CreateMessage(ctx, arriving)
	if err != nil {
		t.Fatal(err)
	}

	for id, want := range map[int64]time.Time{
		first:   base.Add(30 * time.Minute),
		waiting: until,
		held:    base.Add(10 * time.Minute),
		late:    base.Add(20 * time.Minute),
	} {
		if m, err := d.GetMessage(ctx, id); err != nil || !m.NextAttemptAt.Equal(want) {
			t.Fatalf("message %d next attempt = %v, %v; want %v", id, m.NextAttemptAt, err, want)
		}
	}
}

func TestNextDueForSkipsExpiredMessages(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	first, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, base))
	if err != nil {
		t.Fatal(err)
	}

	expiring := testMessage(2, false, []byte{0x02}, base)
	expiring.ExpiresAt = base.Add(time.Minute)

	if _, err := d.CreateMessage(ctx, expiring); err != nil {
		t.Fatal(err)
	}

	other := testMessage(3, false, []byte{0x03}, base)
	other.MSISDN = "15551230003"

	if _, err := d.CreateMessage(ctx, other); err != nil {
		t.Fatal(err)
	}

	if m, ok, err := d.NextDueFor(ctx, testMSISDN, first, base.Add(time.Minute)); err != nil || ok {
		t.Fatalf("NextDueFor = %+v, %v, %v; want nothing, since the only other message has expired", m, ok, err)
	}
}

func TestTimeStampsDifferPerRecipient(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	arrivals := []struct {
		msisdn string
		at     time.Time
		want   time.Time
	}{
		{testMSISDN, base.Add(100 * time.Millisecond), base},
		{testMSISDN, base.Add(200 * time.Millisecond), base.Add(time.Second)},
		{testMSISDN, base.Add(900 * time.Millisecond), base.Add(2 * time.Second)},
		{"15551230003", base.Add(900 * time.Millisecond), base},
		{testMSISDN, base.Add(5 * time.Second), base.Add(5 * time.Second)},
	}

	for i, a := range arrivals {
		m := testMessage(uint8(i), false, []byte{byte(i)}, a.at)
		m.MSISDN = a.msisdn

		id, err := d.CreateMessage(ctx, m)
		if err != nil {
			t.Fatal(err)
		}

		got, err := d.GetMessage(ctx, id)
		if err != nil || !got.TimeStamp.Equal(a.want) {
			t.Fatalf("message %d time stamp = %v, %v; want %v", i, got.TimeStamp, err, a.want)
		}
	}
}

func TestReplacedMessageTakesANewTimeStamp(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	original := testMessage(1, false, []byte{0x01}, base)
	original.ProtocolIdentifier = 0x41
	original.Replace = true

	id, err := d.CreateMessage(ctx, original)
	if err != nil {
		t.Fatal(err)
	}

	replacement := testMessage(2, false, []byte{0x02}, base.Add(300*time.Millisecond))
	replacement.ProtocolIdentifier = 0x41
	replacement.Replace = true

	replacementID, err := d.CreateMessage(ctx, replacement)
	if err != nil || replacementID == id {
		t.Fatalf("replacement stored as %d, %v; want a new message", replacementID, err)
	}

	if m, err := d.GetMessage(ctx, replacementID); err != nil || !m.TimeStamp.Equal(base.Add(time.Second)) {
		t.Fatalf("time stamp = %v, %v; want one the recipient has not seen", m.TimeStamp, err)
	}
}

func TestMessagesSubmittedBy(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	ota := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	other := createTestUser(t, d, "other@example.com", RoleSubmitter)

	fromAPI := func(user int64) NewMessage {
		m := testMessage(1, false, []byte{0x01}, now)
		m.Origin, m.SubmittedBy = OriginAPI, user

		return m
	}

	ids, err := d.CreateMessages(ctx, []NewMessage{
		fromAPI(ota), testMessage(2, false, []byte{0x01}, now), fromAPI(other), fromAPI(ota), fromAPI(0),
	})
	if err != nil {
		t.Fatal(err)
	}

	if m, err := d.GetMessage(ctx, ids[0]); err != nil || m.SubmittedBy != ota {
		t.Fatalf("GetMessage: SubmittedBy %d, %v, want %d", m.SubmittedBy, err, ota)
	}

	if m, err := d.GetMessage(ctx, ids[1]); err != nil || m.SubmittedBy != 0 {
		t.Fatalf("GetMessage of a message from an MS: SubmittedBy %d, %v, want 0", m.SubmittedBy, err)
	}

	messages, total, err := d.ListMessages(ctx, MessageFilter{SubmittedBy: ota}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}

	got := []int64{}
	for _, m := range messages {
		got = append(got, m.ID)
	}

	if want := []int64{ids[3], ids[0]}; !reflect.DeepEqual(got, want) || total != 2 {
		t.Fatalf("ListMessages by submitter = %v (total %d), want %v (total 2)", got, total, want)
	}

	for name, statement := range map[string]string{
		"message from an MS with a submitter": `UPDATE messages SET submitted_by = 1 WHERE origin = 'mobile'`,
		"unknown submitter":                   `UPDATE messages SET submitted_by = 42 WHERE origin = 'api'`,
	} {
		if _, err := d.conn.ExecContext(ctx, statement); err == nil {
			t.Errorf("%s: no constraint failed", name)
		}
	}
}
