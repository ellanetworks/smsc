package db

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/encryption"
)

func rawRow(t *testing.T, d *DB, query string, id int64) ([]byte, []byte) {
	t.Helper()

	var value, cipherID []byte
	if err := d.writer.QueryRowContext(context.Background(), query, id).Scan(&value, &cipherID); err != nil {
		t.Fatal(err)
	}

	return value, cipherID
}

func TestMessageContentIsEncrypted(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	text := []byte{0x01, 0x07, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0xf2, 0x00, 0x00, 0x05, 0xc8, 0x32, 0x9b, 0xfd, 0x06}

	id, err := d.CreateMessage(ctx, testMessage(7, false, text, now))
	if err != nil {
		t.Fatal(err)
	}

	stored, cipherID := rawRow(t, d, `SELECT tpdu, cipher_id FROM messages WHERE id = ?`, id)
	if len(cipherID) != cipherIDLength || bytes.Contains(stored, text) {
		t.Fatalf("stored tpdu %x with cipher ID %x is not encrypted", stored, cipherID)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil || !bytes.Equal(m.TPDU, text) {
		t.Fatalf("GetMessage = %x, %v", m.TPDU, err)
	}

	listed, _, err := d.ListMessages(ctx, MessageFilter{}, 1, 10)
	if err != nil || len(listed) != 1 || !bytes.Equal(listed[0].TPDU, text) {
		t.Fatalf("ListMessages = %+v, %v", listed, err)
	}

	due, ok, err := d.NextDue(ctx, testMSISDN, now)
	if err != nil || !ok || !bytes.Equal(due.TPDU, text) {
		t.Fatalf("NextDue = %x, %v, %v", due.TPDU, ok, err)
	}

	report := []byte{0x00, 0x00}

	if _, err := d.CreateDeliveryAttempt(ctx, DeliveryAttempt{
		MessageID: id, StartedAt: now, CompletedAt: now, Step: StepDelivery, Node: "mme", Outcome: "success",
		DeliverReport: report,
	}); err != nil {
		t.Fatal(err)
	}

	stored, cipherID = rawRow(t, d, `SELECT deliver_report, cipher_id FROM delivery_attempts WHERE message_id = ?`, id)
	if len(cipherID) != cipherIDLength || len(stored) != len(report)+encryption.Overhead {
		t.Fatalf("stored deliver report %x with cipher ID %x is not encrypted", stored, cipherID)
	}

	reports, err := d.ListDeliverReports(ctx, id)
	if err != nil || len(reports) != 1 || !bytes.Equal(reports[0].DeliverReport, report) {
		t.Fatalf("ListDeliverReports = %+v, %v", reports, err)
	}
}

func TestMessageContentIsBoundToItsRow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name   string
		tamper string
	}{
		{"swapped content", `UPDATE messages SET tpdu = (SELECT tpdu FROM messages WHERE id = ?2) WHERE id = ?1`},
		{"swapped cipher ID", `UPDATE messages SET cipher_id = (SELECT cipher_id FROM messages WHERE id = ?2) WHERE id = ?1`},
		{"redirected", `UPDATE messages SET msisdn = '15550000000' WHERE id = ?1 AND ?2 > 0`},
		{"other sender", `UPDATE messages SET originator = '15550000000' WHERE id = ?1 AND ?2 > 0`},
		{"plaintext", `UPDATE messages SET tpdu = x'0107' WHERE id = ?1 AND ?2 > 0`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := openTestDB(t)

			first, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01, 0x01}, now))
			if err != nil {
				t.Fatal(err)
			}

			second, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x01, 0x02}, now))
			if err != nil {
				t.Fatal(err)
			}

			if _, err := d.writer.ExecContext(ctx, tt.tamper, first, second); err != nil {
				t.Fatal(err)
			}

			if _, err := d.GetMessage(ctx, first); err == nil {
				t.Fatal("a tampered message was read")
			}

			if _, err := d.GetMessage(ctx, second); err != nil {
				t.Fatalf("the other message: %v", err)
			}
		})
	}
}

func TestLockedNodeRefusesMessageContent(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	keys := d.keys
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	id, err := d.CreateMessage(ctx, smppReady(t, d, now))
	if err != nil {
		t.Fatal(err)
	}

	keys.Lock()

	if _, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x01}, now)); !errors.Is(err, ErrKeysLocked) ||
		!errors.Is(err, ErrBusy) || !errors.Is(err, encryption.ErrLocked) {
		t.Fatalf("CreateMessage on a locked node = %v", err)
	}

	if _, err := d.GetMessage(ctx, id); !errors.Is(err, ErrKeysLocked) {
		t.Fatalf("GetMessage on a locked node = %v", err)
	}

	if _, err := d.ClaimNotifications(ctx, "node", 1, now.Add(time.Hour), time.Minute, 10); !errors.Is(err, ErrKeysLocked) {
		t.Fatalf("ClaimNotifications on a locked node = %v", err)
	}

	if n := count(t, d, `smpp_notifications WHERE claimed_by IS NOT NULL`); n != 0 {
		t.Fatalf("a locked node claimed %d notifications", n)
	}

	if n := count(t, d, `messages`); n != 1 {
		t.Fatalf("messages = %d, want 1", n)
	}
}

func smppReady(t *testing.T, d *DB, now time.Time) NewMessage {
	t.Helper()

	owner := createTestUser(t, d, "ota@example.com", RoleSubmitter)
	esme := createTestESME(t, d, testESME("ota", owner))

	return smppMessage(esme, owner, ReceiptFinal, true, now)
}

func TestNotificationsAreDecrypted(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	m := smppReady(t, d, now)

	id, err := d.CreateMessage(ctx, m)
	if err != nil {
		t.Fatal(err)
	}

	report := []byte{0x00, 0x00, 0x02, 0x90, 0x00}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, now, nil, 0, Fence{}, DeliveryAttempt{
		MessageID: id, StartedAt: now, CompletedAt: now, Step: StepDelivery, Node: "mme", Outcome: "success",
		DeliverReport: report,
	}); err != nil {
		t.Fatal(err)
	}

	ns, err := d.ClaimNotifications(ctx, "node", m.SMPP.ESMEID, now, time.Minute, 10)
	if err != nil || len(ns) == 0 {
		t.Fatalf("ClaimNotifications = %+v, %v", ns, err)
	}

	for _, n := range ns {
		if !bytes.Equal(n.Message.TPDU, m.TPDU) {
			t.Errorf("notification %d message = %x, want %x", n.ID, n.Message.TPDU, m.TPDU)
		}

		if n.Type == NotificationSIMAnswer && !bytes.Equal(n.DeliverReport, report) {
			t.Errorf("SIM answer = %x, want %x", n.DeliverReport, report)
		}
	}
}

func TestAppliesNeverDecrypt(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	r := recordOf(testMessage(3, false, []byte{0x01, 0x03}, now))
	if err := d.sealMessage(&r); err != nil {
		t.Fatal(err)
	}

	d.keys.Lock()

	if _, err := submit[storedMessages](ctx, d, cmdStoreMessages, storeMessages{Messages: []messageRecord{r}}); err != nil {
		t.Fatalf("applying an encrypted message without keys: %v", err)
	}
}

func TestPlaintextContentIsRefusedByTheApply(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	plain := storeMessages{Messages: []messageRecord{recordOf(testMessage(4, false, []byte{0x01, 0x04}, now))}}

	_, err := submit[storedMessages](ctx, d, cmdStoreMessages, plain)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a plaintext message = %v, want %v", err, ErrInvalid)
	}

	if fatal := (*FatalError)(nil); errors.As(err, &fatal) {
		t.Fatalf("a plaintext message stopped the state machine: %v", err)
	}

	id, err := d.CreateMessage(ctx, testMessage(5, false, []byte{0x01, 0x05}, now))
	if err != nil {
		t.Fatal(err)
	}

	attempt := recordAttempt{Attempt: DeliveryAttempt{
		MessageID: id, StartedAt: now, CompletedAt: now, Step: StepDelivery, Node: "mme", Outcome: "success",
		DeliverReport: []byte{0x00, 0x00},
	}}

	if _, err := submit[recordedAttempt](ctx, d, cmdRecordAttempt, attempt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a plaintext deliver report = %v, want %v", err, ErrInvalid)
	}
}
