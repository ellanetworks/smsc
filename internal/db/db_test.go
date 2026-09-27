package db

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

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

	id, err := d.CreateMessage(ctx, "15551230001", "15551230002", tpdu, submitted)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	m, err := d.GetMessage(ctx, id)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}

	if m.Originator != "15551230001" || m.Recipient != "15551230002" || !bytes.Equal(m.TPDU, tpdu) {
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

	id, err := d.CreateMessage(ctx, "15551230001", "15551230002", []byte{0x01}, time.Now())
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

	id, err := d.CreateMessage(ctx, "15551230001", "15551230002", []byte{0x01}, time.Now())
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
