package db

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestSMPPSessionLimitAcrossNodes(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)
	d := f.d

	open := func(node string, id uint64) error {
		return d.OpenSMPPSession(ctx, SMPPSession{
			NodeID: node, SessionID: id, ESMEID: f.esme, BindType: "transceiver", Remote: "192.0.2.1:4000",
			BoundAt: f.origin,
		}, 2)
	}

	if err := open("a", 1); err != nil {
		t.Fatal(err)
	}

	if err := open("b", 1); err != nil {
		t.Fatal(err)
	}

	if err := open("b", 2); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("third session = %v, want %v", err, ErrTooManySessions)
	}

	if err := d.CloseSMPPSession(ctx, "a", 1); err != nil {
		t.Fatal(err)
	}

	if err := open("b", 2); err != nil {
		t.Fatalf("after a close = %v", err)
	}

	if ids, err := d.NodeSMPPSessions(ctx, "b"); err != nil || !slices.Equal(ids, []uint64{1, 2}) {
		t.Fatalf("node b sessions = %v, %v", ids, err)
	}

	if err := d.SyncSMPPSessions(ctx, "b", []uint64{2}); err != nil {
		t.Fatal(err)
	}

	if sessions, err := d.SMPPSessions(ctx, f.esme); err != nil || len(sessions) != 1 || sessions[0].SessionID != 2 {
		t.Fatalf("sessions after a sync = %+v, %v", sessions, err)
	}

	if err := d.SyncSMPPSessions(ctx, "b", nil); err != nil {
		t.Fatal(err)
	}

	if nodes, err := d.SMPPSessionNodes(ctx); err != nil || len(nodes) != 0 {
		t.Fatalf("nodes after dropping b = %v, %v", nodes, err)
	}

	if err := d.OpenSMPPSession(ctx, SMPPSession{
		NodeID: "a", SessionID: 9, ESMEID: 4242, BindType: "receiver",
		Remote: "192.0.2.1:4000", BoundAt: f.origin,
	}, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a session of an unknown ESME = %v, want %v", err, ErrNotFound)
	}
}

// SMPP 3.4 section 4.6.2
func TestNotificationClaims(t *testing.T) {
	ctx := context.Background()
	f := newSMPPFixture(t)
	d := f.d

	id := f.submit(t, f.esme, ReceiptFinal, false)

	t0 := f.origin.Add(time.Minute)
	if err := d.CompleteMessage(ctx, id, StatusDelivered, t0, nil, 0, Fence{}); err != nil {
		t.Fatal(err)
	}

	claimed, err := d.ClaimNotifications(ctx, "a", f.esme, t0, time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}

	if again, err := d.ClaimNotifications(ctx, "b", f.esme, t0.Add(time.Second), time.Minute, 10); err != nil || len(again) != 0 {
		t.Fatalf("a claimed notification was claimed again: %+v, %v", again, err)
	}

	if err := d.RetryNotification(ctx, claimed[0].ID, t0, "b"); err != nil {
		t.Fatal(err)
	}

	if n := notificationByID(t, d, claimed[0].ID); n.Attempts != 0 {
		t.Fatalf("another node's retry changed the claim: %+v", n)
	}

	if lapsed, err := d.ClaimNotifications(ctx, "b", f.esme, t0.Add(time.Minute), time.Minute, 10); err != nil ||
		len(lapsed) != 1 {
		t.Fatalf("a lapsed claim was not taken over: %+v, %v", lapsed, err)
	}

	if err := d.ReleaseNotification(ctx, claimed[0].ID, "b", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if back, err := d.ClaimNotifications(ctx, "a", f.esme, t0.Add(time.Minute), time.Minute, 10); err != nil || len(back) != 1 {
		t.Fatalf("a released notification was not claimable: %+v, %v", back, err)
	}

	if err := d.CloseNotification(ctx, claimed[0].ID, NotificationAcknowledged); err != nil {
		t.Fatal(err)
	}

	if done, err := d.ClaimNotifications(ctx, "b", f.esme, t0.Add(time.Hour), time.Minute, 10); err != nil || len(done) != 0 {
		t.Fatalf("a closed notification was claimed: %+v, %v", done, err)
	}
}
