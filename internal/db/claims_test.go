package db

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func applyAt(t *testing.T, d *DB, term uint64, typ CommandType, payload any) Result {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var id RequestID

	_, _ = rand.Read(id[:])

	entry, err := EncodeBatch(Batch{LeaderTime: time.Now().UnixNano(), Commands: []Command{
		{Type: typ, Version: 1, RequestID: id, Payload: raw},
	}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := d.Apply(d.AppliedIndex()+1, term, entry)
	if err != nil {
		t.Fatal(err)
	}

	return results[0]
}

func claimAt(t *testing.T, d *DB, logTerm, term uint64) (Claim, error) {
	t.Helper()

	r := applyAt(t, d, logTerm, cmdClaimRecipient, claimRecipient{MSISDN: testMSISDN, Term: term})
	if err := r.Err(); err != nil {
		return Claim{}, err
	}

	var c Claim
	if err := json.Unmarshal(r.Value, &c); err != nil {
		t.Fatal(err)
	}

	return c, nil
}

func TestClaimsAreFencedByTerm(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	old, err := claimAt(t, d, 3, 3)
	if err != nil || old.Term != 3 || old.Index == 0 {
		t.Fatalf("claim = %+v, %v", old, err)
	}

	if _, err := claimAt(t, d, 4, 3); !errors.Is(err, ErrStaleTerm) {
		t.Fatalf("a deposed leader's claim appended in a newer term = %v, want %v", err, ErrStaleTerm)
	}

	if held, err := d.ClaimedBefore(ctx, 4, time.Time{}); err != nil || len(held) != 1 || held[0] != testMSISDN {
		t.Fatalf("claimed before term 4 = %v, %v", held, err)
	}

	current, err := claimAt(t, d, 4, 4)
	if err != nil || current.Term != 4 {
		t.Fatalf("claim = %+v, %v", current, err)
	}

	if _, err := claimAt(t, d, 4, 3); !errors.Is(err, ErrStaleTerm) {
		t.Fatalf("an older claim over a newer one = %v, want %v", err, ErrStaleTerm)
	}

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{1}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}

	stale := Fence{Term: 3, Claim: old}
	if err := d.ScheduleRetry(ctx, id, time.Now(), time.Now(), false, time.Time{}, stale); !errors.Is(err, ErrStaleTerm) {
		t.Fatalf("retry of a deposed term = %v, want %v", err, ErrStaleTerm)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, time.Now(), nil, 0, stale); err != nil {
		t.Fatalf("a deposed term's delivery = %v, want it recorded", err)
	}

	if held, err := d.ClaimedBefore(ctx, 5, time.Time{}); err != nil || len(held) != 1 {
		t.Fatalf("a deposed term's outcome released the newer claim: %v, %v", held, err)
	}

	applyAt(t, d, 4, cmdReleaseRecipient, releaseRecipient{MSISDN: testMSISDN, Claim: current})

	if held, err := d.ClaimedBefore(ctx, 5, time.Time{}); err != nil || len(held) != 0 {
		t.Fatalf("the claim was not released: %v, %v", held, err)
	}
}

func TestReleaseNeedsTheSameClaim(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	first, err := claimAt(t, d, 2, 2)
	if err != nil {
		t.Fatal(err)
	}

	second, err := claimAt(t, d, 2, 2)
	if err != nil {
		t.Fatal(err)
	}

	applyAt(t, d, 2, cmdReleaseRecipient, releaseRecipient{MSISDN: testMSISDN, Claim: first})

	if held, _ := d.ClaimedBefore(ctx, 3, time.Time{}); len(held) != 1 {
		t.Fatalf("an older claim of the same term released the newer one")
	}

	applyAt(t, d, 2, cmdReleaseRecipient, releaseRecipient{MSISDN: testMSISDN, Claim: second})

	if held, _ := d.ClaimedBefore(ctx, 3, time.Time{}); len(held) != 0 {
		t.Fatalf("the claim was not released")
	}
}
