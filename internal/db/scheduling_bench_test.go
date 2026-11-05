package db

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func seedBacklog(b *testing.B, recipients, perRecipient, hot int) (*DB, time.Time, []string) {
	b.Helper()

	ctx := context.Background()

	d, err := Open(ctx, filepath.Join(b.TempDir(), "smsc.db"))
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = d.Close() })

	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	var batch []NewMessage

	flush := func() {
		if _, err := d.CreateMessages(ctx, batch); err != nil {
			b.Fatal(err)
		}

		batch = batch[:0]
	}

	add := func(msisdn string, at time.Time) {
		m := testMessage(0, false, []byte{0x01}, at)
		m.MSISDN = msisdn
		batch = append(batch, m)

		if len(batch) == 500 {
			flush()
		}
	}

	n := 0

	for range hot {
		add("15559999999", base.Add(time.Duration(n)*time.Microsecond))
		n++
	}

	for range perRecipient {
		for r := range recipients {
			add(fmt.Sprintf("1555%07d", r), base.Add(time.Duration(n)*time.Microsecond))
			n++
		}
	}

	flush()

	busy := make([]string, 0, 256)
	if hot > 0 {
		busy = append(busy, "15559999999")
	}

	for r := 0; len(busy) < 256; r++ {
		busy = append(busy, fmt.Sprintf("1555%07d", r))
	}

	return d, base.Add(time.Hour), busy
}

func BenchmarkNextDue(b *testing.B) {
	for _, tc := range []struct {
		name                     string
		recipients, backlog, hot int
	}{
		{"spread", 10000, 3, 0},
		{"hot", 10000, 1, 20000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			d, now, busy := seedBacklog(b, tc.recipients, tc.backlog, tc.hot)
			ctx := context.Background()

			skip := map[string]bool{}
			for _, msisdn := range busy {
				skip[msisdn] = true
			}

			b.ResetTimer()

			for b.Loop() {
				due, _, err := d.DueRecipients(ctx, now, 1, func(msisdn string) bool { return skip[msisdn] })
				if err != nil || len(due) != 1 {
					b.Fatal(due, err)
				}

				if _, ok, err := d.NextDue(ctx, due[0], now); err != nil || !ok {
					b.Fatal(ok, err)
				}
			}
		})
	}
}

func BenchmarkDueRecipientsBatch(b *testing.B) {
	d, now, busy := seedBacklog(b, 10000, 3, 0)
	ctx := context.Background()

	skip := map[string]bool{}
	for _, msisdn := range busy {
		skip[msisdn] = true
	}

	b.ResetTimer()

	for b.Loop() {
		due, _, err := d.DueRecipients(ctx, now, 512, func(msisdn string) bool { return skip[msisdn] })
		if err != nil || len(due) != 512 {
			b.Fatal(len(due), err)
		}
	}

	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/512, "ns/recipient")
}
