package db

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type applyBench struct {
	d       *DB
	index   uint64
	stored  int64
	request uint64
	now     time.Time
}

func (a *applyBench) command(t CommandType, payload any) Command {
	retryable := t == cmdStoreMessages

	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}

	a.request++

	var id RequestID
	binary.BigEndian.PutUint64(id[:], a.request)

	return Command{Type: t, Version: 1, RequestID: id, Retryable: retryable, Payload: raw}
}

func (a *applyBench) entry() []byte {
	const stores, settles = 32, 16

	cmds := make([]Command, 0, stores+2*settles)

	for i := range stores {
		n := a.request + uint64(i)
		msisdn := fmt.Sprintf("1555%07d", n%5000)
		m := NewMessage{
			Originator:  Address{Digits: "15559990000", TypeOfNumber: 1, NumberingPlan: 1},
			Recipient:   Address{Digits: msisdn, TypeOfNumber: 1, NumberingPlan: 1},
			MSISDN:      msisdn,
			Origin:      OriginSMPP,
			TPDU:        make([]byte, 140),
			SubmittedAt: a.now,
			ExpiresAt:   a.now.Add(24 * time.Hour),
			SMPP:        &NewSMPPMessage{ESMEID: 1, Receipt: ReceiptFinal},
			TraceParent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		}

		cmds = append(cmds, a.command(cmdStoreMessages, storeMessages{Messages: []NewMessage{m}}))
	}

	for i := range settles {
		id := a.stored - stores + 1 + int64(i)
		if id < 1 {
			break
		}

		cmds = append(cmds,
			a.command(cmdRecordAttempt, recordAttempt{Attempt: DeliveryAttempt{
				MessageID: id, StartedAt: a.now, CompletedAt: a.now, Step: StepDelivery, Node: "mme", Outcome: "delivered",
			}}),
			a.command(cmdCompleteMessage, completeMessage{ID: id, Status: StatusDelivered, At: a.now, TPStatus: 0}))
	}

	a.stored += stores
	a.now = a.now.Add(time.Millisecond)

	entry, err := EncodeBatch(Batch{LeaderTime: a.now.UnixNano(), Commands: cmds})
	if err != nil {
		panic(err)
	}

	return entry
}

func (a *applyBench) apply(tb testing.TB, data ...[]byte) {
	entries := make([]Entry, len(data))
	for i, e := range data {
		a.index++
		entries[i] = Entry{Index: a.index, Term: 1, Data: e}
	}

	results, err := a.d.ApplyEntries(entries)
	if err != nil {
		tb.Fatal(err)
	}

	for _, rs := range results {
		for _, r := range rs {
			if r.Code != "" {
				tb.Fatalf("result %s: %s", r.Code, r.Message)
			}
		}
	}
}

func BenchmarkApply(b *testing.B) {
	for _, group := range []int{1, 8} {
		b.Run(fmt.Sprintf("entries=%d", group), func(b *testing.B) { benchmarkApply(b, group) })
	}
}

func benchmarkApply(b *testing.B, group int) {
	ctx := context.Background()

	d, err := Open(ctx, filepath.Join(b.TempDir(), "smsc.db"))
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = d.Close() })

	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	user, err := d.CreateUser(ctx, NewUser{Email: "esme@example.com", Role: RoleSubmitter, PasswordHash: "h"}, 10)
	if err != nil {
		b.Fatal(err)
	}

	if _, err := d.CreateESME(ctx, ESME{SystemID: "esme", UserID: user, PasswordHash: "x", CreatedAt: now}); err != nil {
		b.Fatal(err)
	}

	a := &applyBench{d: d, index: d.AppliedIndex(), now: now}

	for range 500 {
		a.apply(b, a.entry())
	}

	entries := make([][]byte, b.N)
	for i := range entries {
		entries[i] = a.entry()
	}

	latencies := make([]time.Duration, 0, len(entries)/group+1)

	b.ResetTimer()

	for i := 0; i < len(entries); i += group {
		start := time.Now()

		a.apply(b, entries[i:min(i+group, len(entries))]...)

		latencies = append(latencies, time.Since(start))
	}

	b.StopTimer()

	slices.Sort(latencies)

	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N), "µs/entry")
	b.ReportMetric(float64(latencies[len(latencies)*99/100].Microseconds()), "p99-µs/apply")
	b.ReportMetric(float64(latencies[len(latencies)-1].Microseconds()), "max-µs/apply")
}
