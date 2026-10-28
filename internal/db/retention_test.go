package db

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestPurgeMessages(t *testing.T) {
	cutoff := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	before, after := cutoff.Add(-time.Hour), cutoff.Add(time.Hour)
	submitted := cutoff.Add(-24 * time.Hour)

	tests := []struct {
		name string
		// setup stores a message, and returns it and its status report, if any (0 otherwise).
		setup      func(t *testing.T, d *DB) (message, report int64)
		wantPurged bool
	}{
		{
			name:  "pending",
			setup: func(t *testing.T, d *DB) (int64, int64) { return create(t, d, submitted), 0 },
		},
		{
			name:       "delivered before the cutoff",
			setup:      completed(StatusDelivered, before),
			wantPurged: true,
		},
		{
			name:       "failed before the cutoff",
			setup:      completed(StatusFailed, before),
			wantPurged: true,
		},
		{
			name:       "expired before the cutoff",
			setup:      completed(StatusExpired, before),
			wantPurged: true,
		},
		{
			name: "replaced before the cutoff",
			setup: func(t *testing.T, d *DB) (int64, int64) {
				id := create(t, d, submitted)
				if _, err := d.conn.ExecContext(context.Background(), `UPDATE messages SET status = ?, updated_at = ? WHERE id = ?`,
					StatusReplaced, before.UnixNano(), id); err != nil {
					t.Fatal(err)
				}

				return id, 0
			},
			wantPurged: true,
		},
		{
			name:  "delivered at the cutoff",
			setup: completed(StatusDelivered, cutoff),
		},
		{
			name:  "delivered after the cutoff",
			setup: completed(StatusDelivered, after),
		},
		{
			name:  "with a pending status report",
			setup: reported(before, time.Time{}),
		},
		{
			name:  "with a status report completed after the cutoff",
			setup: reported(before, after),
		},
		{
			name:       "with a status report completed before the cutoff",
			setup:      reported(before, before.Add(time.Minute)),
			wantPurged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			d := openTestDB(t)

			id, report := tt.setup(t, d)
			attempt(t, d, id, submitted)

			recipientsBefore := count(t, d, `recipients`)

			n, err := d.PurgeMessages(ctx, cutoff, 10)
			if err != nil {
				t.Fatalf("PurgeMessages: %v", err)
			}

			want := 0
			if tt.wantPurged {
				want = 1
			}

			if n != want || int(testutil.ToFloat64(d.metrics.purged)) != want {
				t.Fatalf("PurgeMessages = %d, purged metric = %v, want %d", n, testutil.ToFloat64(d.metrics.purged), want)
			}

			for _, id := range []int64{id, report} {
				if id == 0 {
					continue
				}

				if _, err := d.GetMessage(ctx, id); errors.Is(err, ErrNotFound) != tt.wantPurged {
					t.Errorf("GetMessage(%d) = %v, want purged %v", id, err, tt.wantPurged)
				}
			}

			if got := count(t, d, `delivery_attempts`); got != 1-want {
				t.Errorf("delivery attempts = %d, want %d", got, 1-want)
			}

			if got := count(t, d, `recipients`); got != recipientsBefore {
				t.Errorf("recipients = %d, want %d", got, recipientsBefore)
			}
		})
	}
}

func TestPurgeMessagesInBatches(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 2500 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages (kind, originator, originator_ton, originator_npi, recipient, recipient_ton,
			recipient_npi, msisdn, origin, message_reference, protocol_identifier, tpdu, status, single_shot, submitted_at,
			time_stamp, expires_at, next_attempt_at, retries, updated_at)
			VALUES ('message', '1', 1, 1, '2', 1, 1, '2', 'api', 0, 0, x'00', 'delivered', 0, ?1, ?1, ?1, ?1, 0, ?1)`,
			at.Add(time.Duration(i)).UnixNano()); err != nil {
			t.Fatal(err)
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var got []int

	for range 4 {
		n, err := d.PurgeMessages(ctx, at.Add(time.Hour), 1000)
		if err != nil {
			t.Fatalf("PurgeMessages: %v", err)
		}

		got = append(got, n)
	}

	if want := []int{1000, 1000, 500, 0}; !slices.Equal(got, want) {
		t.Fatalf("batches = %v, want %v", got, want)
	}
}

func TestPurgeMessagesUsesItsIndex(t *testing.T) {
	d := openTestDB(t)

	rows, err := d.conn.QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+purgeableMessages, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var plan []string

	for rows.Next() {
		var (
			id, parent, notUsed int
			detail              string
		)

		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}

		plan = append(plan, detail)
	}

	if !strings.Contains(strings.Join(plan, "\n"), "messages_purgeable") {
		t.Fatalf("query plan does not use messages_purgeable:\n%s", strings.Join(plan, "\n"))
	}
}

func TestListMessagesByTime(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	ids := map[string]int64{}
	for name, at := range map[string]time.Time{
		"before": start.Add(-time.Nanosecond), "start": start, "inside": start.Add(time.Minute),
		"end": end, "after": end.Add(time.Minute),
	} {
		ids[name] = create(t, d, at)
	}

	tests := []struct {
		name   string
		filter MessageFilter
		want   []string
	}{
		{"unbounded", MessageFilter{}, []string{"after", "end", "inside", "start", "before"}},
		{"start only", MessageFilter{Start: start}, []string{"after", "end", "inside", "start"}},
		{"end only", MessageFilter{End: end}, []string{"inside", "start", "before"}},
		{"both", MessageFilter{Start: start, End: end}, []string{"inside", "start"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages, total, err := d.ListMessages(ctx, tt.filter, 1, 10)
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}

			got := make([]int64, len(messages))
			for i, m := range messages {
				got[i] = m.ID
			}

			want := make([]int64, len(tt.want))
			for i, name := range tt.want {
				want[i] = ids[name]
			}

			slices.Sort(got)
			slices.Sort(want)

			if total != len(want) || !slices.Equal(got, want) {
				t.Fatalf("ListMessages = %v (total %d), want %v %v", got, total, tt.want, want)
			}
		})
	}
}

// create stores a short message from a phone, submitted at at, and returns its ID.
func create(t *testing.T, d *DB, at time.Time) int64 {
	t.Helper()

	id, err := d.CreateMessage(context.Background(), testMessage(0, false, []byte{0x01}, at))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	return id
}

func completed(status MessageStatus, at time.Time) func(t *testing.T, d *DB) (int64, int64) {
	return func(t *testing.T, d *DB) (int64, int64) {
		id := create(t, d, at.Add(-time.Hour))
		if err := d.CompleteMessage(context.Background(), id, status, at, nil, 0); err != nil {
			t.Fatalf("CompleteMessage: %v", err)
		}

		return id, 0
	}
}

// reported stores a message delivered at delivered with a status report on it, which reached its final status at
// reportDone, or is pending when reportDone is zero.
func reported(delivered, reportDone time.Time) func(t *testing.T, d *DB) (int64, int64) {
	return func(t *testing.T, d *DB) (int64, int64) {
		ctx := context.Background()
		id := create(t, d, delivered.Add(-time.Hour))

		if err := d.CompleteMessage(ctx, id, StatusDelivered, delivered, testReport(delivered), 0); err != nil {
			t.Fatalf("CompleteMessage: %v", err)
		}

		reports, err := listStatusReports(ctx, d, id)
		if err != nil || len(reports) != 1 {
			t.Fatalf("reports = %+v, %v", reports, err)
		}

		if !reportDone.IsZero() {
			if err := d.CompleteMessage(ctx, reports[0].ID, StatusDelivered, reportDone, nil, 0); err != nil {
				t.Fatalf("CompleteMessage of the report: %v", err)
			}
		}

		return id, reports[0].ID
	}
}

func attempt(t *testing.T, d *DB, id int64, at time.Time) {
	t.Helper()

	if _, err := d.CreateDeliveryAttempt(context.Background(), DeliveryAttempt{
		MessageID: id, StartedAt: at, CompletedAt: at, Step: StepRouting, Node: "hss.example.org", Outcome: "success",
	}); err != nil {
		t.Fatalf("CreateDeliveryAttempt: %v", err)
	}
}

func count(t *testing.T, d *DB, table string) int {
	t.Helper()

	var n int
	if err := d.conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}
