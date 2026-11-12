package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const testRange = "40000-40009"

func openMigratedDB(t *testing.T) *DB {
	t.Helper()

	d := openTestDB(t)
	migrateAll(t, d)

	return d
}

type moFixture struct {
	d    *DB
	app  int64
	user int64
	now  time.Time
}

func newMOFixture(t *testing.T, routes ...string) moFixture {
	t.Helper()

	d := openMigratedDB(t)
	user := createTestUser(t, d, "app@example.com", RoleSubmitter)

	app := testESME("app", user)
	app.Routes = routes

	return moFixture{
		d:    d,
		user: user,
		app:  createTestESME(t, d, app),
		now:  time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
	}
}

func semiOctets(digits string) []byte {
	out := make([]byte, 0, (len(digits)+1)/2)

	for i := 0; i < len(digits); i += 2 {
		var b byte

		if i+1 < len(digits) {
			b = (digits[i+1]&0x0f)<<4 | (digits[i] & 0x0f)
		} else {
			b = 0xf0 | (digits[i] & 0x0f)
		}

		out = append(out, b)
	}

	return out
}

func submitTPDU(to string, report bool) []byte {
	first := byte(0x01)
	if report {
		first |= 0x20
	}

	out := []byte{first, 0x2a, byte(len(to)), 0x81}
	out = append(out, semiOctets(to)...)

	return append(out, 0x00, 0x04, 0x00)
}

func delegatedMessage(f moFixture, to string, report bool, expires time.Time) NewMessage {
	return NewMessage{
		Originator:  testOriginator,
		Recipient:   Address{Digits: to, TypeOfNumber: 0, NumberingPlan: 1},
		MSISDN:      "1555" + to,
		Origin:      OriginMobile,
		TPDU:        submitTPDU(to, report),
		SubmittedAt: f.now,
		ExpiresAt:   expires,
	}
}

type moDeliveryRow struct {
	ID        int64
	MessageID int64
	ESMEID    int64
	Route     string
	ExpiresAt int64
	Attempts  int
	Result    string
}

func moDeliveryRows(t *testing.T, d *DB, messageID int64) []moDeliveryRow {
	t.Helper()

	rows, err := d.writer.QueryContext(context.Background(),
		`SELECT id, message_id, COALESCE(esme_id, 0), route, expires_at, attempts, COALESCE(result, '')
		FROM esme_deliveries WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var out []moDeliveryRow

	for rows.Next() {
		var r moDeliveryRow
		if err := rows.Scan(&r.ID, &r.MessageID, &r.ESMEID, &r.Route, &r.ExpiresAt, &r.Attempts, &r.Result); err != nil {
			t.Fatal(err)
		}

		out = append(out, r)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func attemptRows(t *testing.T, d *DB, messageID int64) []DeliveryAttempt {
	t.Helper()

	rows, err := d.writer.QueryContext(context.Background(),
		`SELECT id, message_id, started_at, completed_at, step, node, outcome
		FROM delivery_attempts WHERE message_id = ? ORDER BY id`, messageID)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	var out []DeliveryAttempt

	for rows.Next() {
		var (
			a                  DeliveryAttempt
			started, completed int64
		)

		if err := rows.Scan(&a.ID, &a.MessageID, &started, &completed, &a.Step, &a.Node, &a.Outcome); err != nil {
			t.Fatal(err)
		}

		a.StartedAt = time.Unix(0, started)
		a.CompletedAt = time.Unix(0, completed)

		out = append(out, a)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func TestCreateMessageDelegatesToTheRoutedESME(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	m, err := f.d.GetMessage(ctx, id)
	if err != nil || m.Status != StatusDelegated {
		t.Fatalf("message = %+v, %v; want %v", m, err, StatusDelegated)
	}

	deliveries := moDeliveryRows(t, f.d, id)
	if len(deliveries) != 1 || deliveries[0].ESMEID != f.app || deliveries[0].Result != "" ||
		deliveries[0].ExpiresAt != f.now.Add(time.Hour).UnixNano() {
		t.Fatalf("deliveries = %+v", deliveries)
	}

	attempts := attemptRows(t, f.d, id)
	if len(attempts) != 1 || attempts[0].Step != StepRouting || attempts[0].Node != "app" ||
		attempts[0].Outcome != "delegated_to_esme" {
		t.Fatalf("attempts = %+v", attempts)
	}

	due, _, err := f.d.DueRecipients(ctx, f.now.Add(2*time.Hour), 10, nil)
	if err != nil || len(due) != 0 {
		t.Fatalf("due = %v, %v; the delegated message must not be due for network delivery", due, err)
	}

	plain, err := f.d.CreateMessage(ctx, delegatedMessage(f, "15551230002", false, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	m, err = f.d.GetMessage(ctx, plain)
	if err != nil || m.Status != StatusPending {
		t.Fatalf("unrouted message = %+v, %v; want %v", m, err, StatusPending)
	}

	if rows := moDeliveryRows(t, f.d, plain); len(rows) != 0 {
		t.Fatalf("unrouted message queued an ESME delivery: %+v", rows)
	}
}

func TestCreateMessageMatchesOnlyNumbersOfTheRangeLength(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, "4000", "40010-40019")

	for to, want := range map[string]string{
		"4000":       "4000",
		"40010":      "40010-40019",
		"40019":      "40010-40019",
		"40001":      "",
		"40020":      "",
		"400100":     "",
		"4000123456": "",
		"4001":       "",
	} {
		id, err := f.d.CreateMessage(ctx, delegatedMessage(f, to, false, f.now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("CreateMessage to %s: %v", to, err)
		}

		rows := moDeliveryRows(t, f.d, id)

		if want == "" {
			if len(rows) != 0 {
				t.Errorf("a message to %s was routed by %+v; no route has its length and range", to, rows)
			}

			continue
		}

		if len(rows) != 1 || rows[0].ESMEID != f.app || rows[0].Route != want {
			t.Errorf("deliveries of a message to %s = %+v, want route %s", to, rows, want)
		}
	}
}

func TestESMERoutesAreValidatedAndDisjoint(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	for _, route := range []string{
		"40a0", "123456789012345678901", "40000-4009", "40009-40000", "+15551230000-15551230999", "4000-", "-4000",
		"4000-4001-4002", "+",
	} {
		if err := f.d.UpdateESME(ctx, ESME{ID: f.app, Routes: []string{route}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("route %q = %v, want %v", route, err, ErrInvalid)
		}
	}

	other := testESME("other", f.user)

	for _, route := range []string{"40005", "39990-40000", "40009-40019", "40000-40009", "39999-40010"} {
		other.Routes = []string{route}
		if _, err := f.d.CreateESME(ctx, other); !errors.Is(err, ErrRouteOverlap) {
			t.Errorf("route %s on another ESME = %v, want %v", route, err, ErrRouteOverlap)
		}
	}

	other.Routes = []string{"40010-40019", "4000", "400000-400099", "+40000-+40009"}
	if _, err := f.d.CreateESME(ctx, other); err != nil {
		t.Fatalf("adjacent, shorter, longer and international routes on another ESME: %v", err)
	}

	got, err := f.d.GetESMEBySystemID(ctx, "other")
	if err != nil || !slices.Equal(got.Routes, []string{"+40000-+40009", "4000", "400000-400099", "40010-40019"}) {
		t.Fatalf("routes = %+v, %v", got.Routes, err)
	}

	if err := f.d.UpdateESME(ctx, ESME{ID: f.app, Routes: []string{"40020-40029", "40025"}}); !errors.Is(err,
		ErrRouteOverlap) {
		t.Fatalf("two overlapping routes on one ESME = %v, want %v", err, ErrRouteOverlap)
	}

	if err := f.d.UpdateESME(ctx, ESME{ID: f.app, Routes: []string{"40000-40005", "40006-40006"}}); err != nil {
		t.Fatalf("an ESME replacing its own routes: %v", err)
	}

	got, err = f.d.GetESMEBySystemID(ctx, "app")
	if err != nil || !slices.Equal(got.Routes, []string{"40000-40005", "40006"}) {
		t.Fatalf("routes after update = %+v, %v", got.Routes, err)
	}
}

func TestCloseMODeliveryReportsToTheOriginator(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	cases := []struct {
		name     string
		result   NotificationResult
		want     MessageStatus
		tpStatus uint8
		outcome  string
	}{
		{"acknowledged", NotificationAcknowledged, StatusDelivered, uint8(tpdu.StatusReceived), "esme_acknowledged"},
		{"rejected", NotificationRejected, StatusFailed, uint8(tpdu.StatusConnectionRejected), "esme_rejected"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", true, f.now.Add(time.Hour)))
			if err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}

			delivery := moDeliveryRows(t, f.d, id)[0]
			at := f.now.Add(2 * time.Minute)

			if err := f.d.CloseMODelivery(ctx, delivery.ID, c.result, at); err != nil {
				t.Fatalf("CloseMODelivery: %v", err)
			}

			m, err := f.d.GetMessage(ctx, id)
			if err != nil || m.Status != c.want {
				t.Fatalf("message = %+v, %v; want %v", m, err, c.want)
			}

			if rows := moDeliveryRows(t, f.d, id); len(rows) != 1 || rows[0].Result != string(c.result) {
				t.Fatalf("delivery = %+v; want result %q", rows, c.result)
			}

			attempts := attemptRows(t, f.d, id)
			if len(attempts) != 2 || attempts[1].Step != StepDelivery || attempts[1].Node != "app" ||
				attempts[1].Outcome != c.outcome {
				t.Fatalf("attempts = %+v", attempts)
			}

			reports, err := f.d.StatusReportsFor(ctx, []int64{id})
			if err != nil || len(reports[id]) != 1 {
				t.Fatalf("reports = %+v, %v; want one status report", reports, err)
			}

			if r := reports[id][0]; r.Kind != KindStatusReport || r.ReportFor != id || r.ReportStatus != c.tpStatus ||
				r.Recipient != testOriginator || r.Status != StatusPending {
				t.Fatalf("report = %+v", r)
			}

			if err := f.d.CloseMODelivery(ctx, delivery.ID, c.result, at); !errors.Is(err, ErrNotFound) {
				t.Fatalf("closing again = %v, want %v", err, ErrNotFound)
			}
		})
	}
}

func TestCloseMODeliveryWithoutAReportRequest(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	delivery := moDeliveryRows(t, f.d, id)[0]

	if err := f.d.CloseMODelivery(ctx, delivery.ID, NotificationAcknowledged, f.now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseMODelivery: %v", err)
	}

	reports, err := f.d.StatusReportsFor(ctx, []int64{id})
	if err != nil || len(reports[id]) != 0 {
		t.Fatalf("reports = %+v, %v; want none", reports, err)
	}
}

func TestCloseMODeliveryQueuesTheSMPPReceipt(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	m := delegatedMessage(f, "40001", false, f.now.Add(time.Hour))
	m.SMPP = &NewSMPPMessage{ESMEID: f.app, Receipt: ReceiptFinal}

	id, err := f.d.CreateMessage(ctx, m)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	delivery := moDeliveryRows(t, f.d, id)[0]

	if err := f.d.CloseMODelivery(ctx, delivery.ID, NotificationAcknowledged, f.now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseMODelivery: %v", err)
	}

	rows := notificationRows(t, f.d, id)
	if len(rows) != 1 || rows[0].Type != NotificationReceipt || rows[0].ESMEID != f.app ||
		!rows[0].TPStatus.Valid || rows[0].TPStatus.V != uint8(tpdu.StatusReceived) {
		t.Fatalf("notifications = %+v; want one delivered receipt", rows)
	}
}

func TestExpireMODeliveries(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", true, f.now.Add(time.Minute)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if n, err := f.d.ExpireMODeliveries(ctx, f.now.Add(30*time.Second)); err != nil || n != 0 {
		t.Fatalf("expire before validity = %d, %v", n, err)
	}

	n, err := f.d.ExpireMODeliveries(ctx, f.now.Add(2*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("expire = %d, %v; want 1", n, err)
	}

	m, err := f.d.GetMessage(ctx, id)
	if err != nil || m.Status != StatusExpired {
		t.Fatalf("message = %+v, %v; want %v", m, err, StatusExpired)
	}

	reports, err := f.d.StatusReportsFor(ctx, []int64{id})
	if err != nil || len(reports[id]) != 1 ||
		reports[id][0].ReportStatus != uint8(tpdu.StatusValidityPeriodExpired) {
		t.Fatalf("reports = %+v, %v", reports, err)
	}

	if rows := moDeliveryRows(t, f.d, id); len(rows) != 1 || rows[0].Result != string(NotificationExpired) {
		t.Fatalf("delivery = %+v", rows)
	}
}

func TestDeleteESMEFailsItsDelegatedMessages(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	open := []int64{}

	for range 2 {
		id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", true, f.now.Add(time.Hour)))
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}

		open = append(open, id)
	}

	closed, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40002", false, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := f.d.CloseMODelivery(ctx, moDeliveryRows(t, f.d, closed)[0].ID, NotificationAcknowledged,
		f.now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseMODelivery: %v", err)
	}

	if err := f.d.DeleteESME(ctx, f.app); err != nil {
		t.Fatalf("DeleteESME: %v", err)
	}

	for _, id := range open {
		m, err := f.d.GetMessage(ctx, id)
		if err != nil || m.Status != StatusFailed {
			t.Fatalf("message %d = %+v, %v; want %v", id, m, err, StatusFailed)
		}

		attempts := attemptRows(t, f.d, id)
		if len(attempts) != 2 || attempts[1].Outcome != "esme_deleted" {
			t.Fatalf("attempts of %d = %+v", id, attempts)
		}

		reports, err := f.d.StatusReportsFor(ctx, []int64{id})
		if err != nil || len(reports[id]) != 1 ||
			reports[id][0].ReportStatus != uint8(tpdu.StatusNotObtainable) {
			t.Fatalf("reports of %d = %+v, %v", id, reports, err)
		}
	}

	if got, err := f.d.GetMessage(ctx, closed); err != nil || got.Status != StatusDelivered {
		t.Fatalf("the closed message = %+v, %v; it must keep its result", got, err)
	}

	for _, id := range open {
		if rows := moDeliveryRows(t, f.d, id); len(rows) != 1 || rows[0].ESMEID != 0 ||
			rows[0].Result != string(NotificationRejected) || rows[0].Route != testRange {
			t.Fatalf("deliveries of %d = %+v; want one rejected, routed by 4000, with no ESME", id, rows)
		}
	}

	if rows := moDeliveryRows(t, f.d, closed); len(rows) != 1 || rows[0].ESMEID != 0 ||
		rows[0].Result != string(NotificationAcknowledged) {
		t.Fatalf("deliveries of %d = %+v; want one acknowledged with no ESME", closed, rows)
	}

	if routes, err := f.d.RoutesFor(ctx, append(open, closed)); err != nil || len(routes) != 3 ||
		routes[closed] != testRange {
		t.Fatalf("RoutesFor after deleting the ESME = %v, %v", routes, err)
	}
}

func TestClaimNotificationsClaimsMODeliveries(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	claimed, err := f.d.ClaimNotifications(ctx, "node-a", f.app, f.now, time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v, %v; want one MO delivery", claimed, err)
	}

	n := claimed[0]
	if n.Type != NotificationMO || n.ESMEID != f.app || n.Message.ID != id ||
		n.Message.Recipient.Digits != "40001" || n.Message.Status != StatusDelegated {
		t.Fatalf("notification = %+v", n)
	}

	if again, err := f.d.ClaimNotifications(ctx, "node-a", f.app, f.now, time.Minute, 10); err != nil || len(again) != 0 {
		t.Fatalf("claimed again = %+v, %v; the lease must hold it", again, err)
	}

	if err := f.d.ReleaseMODelivery(ctx, n.ID, "node-a", f.now.Add(90*time.Second)); err != nil {
		t.Fatalf("ReleaseMODelivery: %v", err)
	}

	if claimed, err := f.d.ClaimNotifications(ctx, "node-b", f.app, f.now.Add(90*time.Second), time.Minute, 10); err != nil ||
		len(claimed) != 1 || claimed[0].ID != n.ID {
		t.Fatalf("claimed after release = %+v, %v", claimed, err)
	}

	if err := f.d.RetryMODelivery(ctx, n.ID, f.now.Add(2*time.Minute), "node-b"); err != nil {
		t.Fatalf("RetryMODelivery: %v", err)
	}

	rows := moDeliveryRows(t, f.d, id)
	if rows[0].Attempts != 1 {
		t.Fatalf("attempts = %d; the retry must count one", rows[0].Attempts)
	}
}

func TestNextNotificationAtCoversESMEsWithNoNotifications(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	if _, ok, err := f.d.NextNotificationAt(ctx, f.app, f.now); err != nil || ok {
		t.Fatalf("NextNotificationAt of none = %t, %v; want no next and no error", ok, err)
	}

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	at, ok, err := f.d.NextNotificationAt(ctx, f.app, f.now.Add(10*time.Second))
	if err != nil || !ok || !at.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("NextNotificationAt with a due delivery = %v, %t, %v; want its expiry %v", at, ok, err,
			f.now.Add(time.Hour))
	}

	if m, err := f.d.GetMessage(ctx, id); err != nil || m.Status != StatusDelegated {
		t.Fatalf("message = %+v, %v", m, err)
	}
}

func TestPurgeKeepsDelegatedAndOpenDeliveries(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	delegated, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	closed, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40002", false, f.now))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := f.d.CloseMODelivery(ctx, moDeliveryRows(t, f.d, closed)[0].ID, NotificationAcknowledged,
		f.now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseMODelivery: %v", err)
	}

	purged, err := f.d.PurgeMessages(ctx, f.now.Add(2*time.Hour), 100)
	if err != nil {
		t.Fatalf("PurgeMessages: %v", err)
	}

	if purged != 1 {
		t.Fatalf("purged = %d; want only the closed delivery", purged)
	}

	if _, err := f.d.GetMessage(ctx, delegated); err != nil {
		t.Fatalf("the delegated message was purged: %v", err)
	}

	if _, err := f.d.GetMessage(ctx, closed); err == nil {
		t.Fatalf("the closed message was not purged")
	}
}

func TestRoutingMigrationPreservesDataAndTriggers(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	delivered, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, now))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	pending := testMessage(2, false, []byte{0x02}, now)
	pending.ExpiresAt = now.Add(24 * time.Hour)

	pendingID, err := d.CreateMessage(ctx, pending)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := d.CompleteMessage(ctx, delivered, StatusDelivered, now.Add(time.Minute),
		testReport(now.Add(time.Minute)), 0, Fence{}); err != nil {
		t.Fatalf("CompleteMessage: %v", err)
	}

	if _, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10); err != nil || total != 2 {
		t.Fatalf("messages before the migration = %d, %v", total, err)
	}

	if err := d.Migrate(ctx, routingSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	messages, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10)
	if err != nil || total != 2 {
		t.Fatalf("messages after the migration = %d of %d, %v", len(messages), total, err)
	}

	reports, err := d.StatusReportsFor(ctx, []int64{delivered})
	if err != nil || len(reports[delivered]) != 1 || reports[delivered][0].Status != StatusPending {
		t.Fatalf("reports after the migration = %+v, %v", reports, err)
	}

	due, _, err := d.DueRecipients(ctx, now.Add(2*time.Hour), 10, nil)
	if err != nil || !slices.Contains(due, pending.MSISDN) || !slices.Contains(due, testOriginator.Digits) {
		t.Fatalf("due after the migration = %v, %v; the pending message and the pending status report must stay due",
			due, err)
	}

	if m, err := d.GetMessage(ctx, pendingID); err != nil || m.Status != StatusPending {
		t.Fatalf("the pending message = %+v, %v", m, err)
	}

	user := createTestUser(t, d, "app@example.com", RoleSubmitter)

	app := testESME("app", user)
	app.Routes = []string{testRange}
	appID := createTestESME(t, d, app)

	m := delegatedMessage(moFixture{d: d, app: appID, now: now.Add(time.Hour)}, "40001", false,
		now.Add(2*time.Hour))

	id, err := d.CreateMessage(ctx, m)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if got, err := d.GetMessage(ctx, id); err != nil || got.Status != StatusDelegated {
		t.Fatalf("message after the migration = %+v, %v; want %v", got, err, StatusDelegated)
	}

	if delegated, total, err := d.ListMessages(ctx, MessageFilter{Status: StatusDelegated}, 1, 10); err != nil ||
		total != 1 || delegated[0].ID != id {
		t.Fatalf("delegated messages read after the migration = %+v, %d, %v", delegated, total, err)
	}
}

func TestDeletingAnESMECountsTheMessagesItFails(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	failed := func() float64 {
		return testutil.ToFloat64(f.d.metrics.completed.WithLabelValues(string(KindMessage), string(StatusFailed)))
	}

	for range 2 {
		if _, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Hour))); err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}
	}

	if err := f.d.DeleteESME(ctx, f.app); err != nil {
		t.Fatalf("DeleteESME: %v", err)
	}

	if got := failed(); got != 2 {
		t.Fatalf("failed messages counted = %v, want 2", got)
	}

	other := testESME("other", f.user)
	other.Routes = []string{testRange}
	createTestESME(t, f.d, other)

	if _, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40002", false, f.now.Add(time.Hour))); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := f.d.DeleteUser(ctx, f.user); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if got := failed(); got != 3 {
		t.Fatalf("failed messages counted after deleting the user = %v, want 3", got)
	}
}

func TestDeleteResultsBeforeRoutesMatchTheBinaryBeforeRoutes(t *testing.T) {
	before, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	after, err := json.Marshal(failedDelegations{})
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(before, after) {
		t.Fatalf("a delete with no failed delegations encodes as %s, the binary before routes as %s", after, before)
	}
}

func TestDeleteUserFailsTheDelegatedMessagesOfItsESMEs(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", true, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := f.d.DeleteUser(ctx, f.user); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	m, err := f.d.GetMessage(ctx, id)
	if err != nil || m.Status != StatusFailed {
		t.Fatalf("message = %+v, %v; want %v", m, err, StatusFailed)
	}

	if attempts := attemptRows(t, f.d, id); len(attempts) != 2 || attempts[1].Outcome != "esme_deleted" {
		t.Fatalf("attempts = %+v", attempts)
	}

	reports, err := f.d.StatusReportsFor(ctx, []int64{id})
	if err != nil || len(reports[id]) != 1 || reports[id][0].ReportStatus != uint8(tpdu.StatusNotObtainable) {
		t.Fatalf("reports = %+v, %v", reports, err)
	}

	if rows := moDeliveryRows(t, f.d, id); len(rows) != 1 || rows[0].ESMEID != 0 || rows[0].Route != testRange {
		t.Fatalf("deliveries = %+v; the row must stay, with its route and no ESME", rows)
	}
}

func TestRoutingMigrationKeepsTheMessageSequence(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	var last int64

	for i := range 3 {
		id, err := d.CreateMessage(ctx, testMessage(uint8(i), false, []byte{byte(i)}, now))
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}

		last = id
	}

	if _, err := d.writer.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, last); err != nil {
		t.Fatal(err)
	}

	if err := d.Migrate(ctx, routingSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id, err := d.CreateMessage(ctx, testMessage(9, false, []byte{0x09}, now))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if id <= last {
		t.Fatalf("a message after the migration got id %d; ids up to %d were already used", id, last)
	}
}

func TestRoutingMigrationOfAnEmptyMessagesTable(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if err := d.Migrate(ctx, routingSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil || id != 1 {
		t.Fatalf("first message after the migration = %d, %v", id, err)
	}
}

func messagesTable(t *testing.T, d *DB) (string, int64) {
	t.Helper()

	var (
		definition string
		rootpage   int64
	)

	if err := d.writer.QueryRowContext(context.Background(), `SELECT sql, rootpage FROM sqlite_schema
		WHERE type = 'table' AND name = 'messages'`).Scan(&definition, &rootpage); err != nil {
		t.Fatal(err)
	}

	return definition, rootpage
}

func writableSchema(t *testing.T, d *DB) int {
	t.Helper()

	var on int

	if err := d.writer.QueryRowContext(context.Background(), `PRAGMA writable_schema`).Scan(&on); err != nil {
		t.Fatal(err)
	}

	return on
}

func TestRoutingMigrationEditsTheMessagesTableInPlace(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now())); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	before, rootpage := messagesTable(t, d)

	if err := d.Migrate(ctx, routingSchema); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	after, rootpageAfter := messagesTable(t, d)

	if rootpageAfter != rootpage {
		t.Fatalf("messages rootpage = %d, was %d; the migration must not rebuild the table", rootpageAfter, rootpage)
	}

	if want := strings.Replace(before, statusCheck, delegatedStatusCheck, 1); after != want || after == before {
		t.Fatalf("messages after the migration:\n%s\nwant:\n%s", after, want)
	}

	if on := writableSchema(t, d); on != 0 {
		t.Fatalf("writable_schema = %d after the migration", on)
	}

	var check string

	if err := d.writer.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
}

func TestDelegatedStatusInTheMigrationTransaction(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	id, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, time.Now()))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = 'delegated' WHERE id = ?`, id); !isConstraint(err) {
		t.Fatalf("a delegated status before the migration = %v, want a constraint error", err)
	}

	if err := allowDelegatedStatus(ctx, tx); err != nil {
		t.Fatalf("allowDelegatedStatus: %v", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = 'delegated' WHERE id = ?`, id); err != nil {
		t.Fatalf("a delegated status in the migration transaction = %v", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE messages SET status = 'lost' WHERE id = ?`, id); !isConstraint(err) {
		t.Fatalf("an unknown status after the migration = %v, want a constraint error", err)
	}
}

func TestAllowDelegatedStatusRefusesAnUnexpectedTable(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t)
	before, _ := messagesTable(t, d)

	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = allowDelegatedStatus(ctx, tx)
	_ = tx.Rollback()

	if err == nil {
		t.Fatal("widening an already widened messages table succeeded")
	}

	if on := writableSchema(t, d); on != 0 {
		t.Fatalf("writable_schema = %d after a refused migration", on)
	}

	if after, _ := messagesTable(t, d); after != before {
		t.Fatalf("a refused migration changed the messages table:\n%s", after)
	}
}

func TestRoutesBeforeTheUpgradeAreBusyNotFatal(t *testing.T) {
	d := openTestDB(t)
	user := createTestUser(t, d, "app@example.com", RoleSubmitter)

	app := testESME("app", user)
	app.Routes = []string{"4000"}

	_, err := d.CreateESME(context.Background(), app)
	if !errors.Is(err, ErrUpgradePending) || !errors.Is(err, ErrBusy) {
		t.Fatalf("CreateESME with routes at schema %d = %v, want %v", d.Schema(), err, ErrUpgradePending)
	}

	if code := CodeOf(ErrBusy); code != "" {
		t.Fatalf("CodeOf(ErrBusy) = %q; a transient busy error must stay uncoded so callers retry it", code)
	}

	app.Routes = nil
	app.ID = createTestESME(t, d, app)
	app.Routes = []string{"4000"}

	if err := d.UpdateESME(context.Background(), app); !errors.Is(err, ErrUpgradePending) {
		t.Fatalf("UpdateESME with routes at schema %d = %v, want %v", d.Schema(), err, ErrUpgradePending)
	}

	app.Routes = nil

	if err := d.UpdateESME(context.Background(), app); err != nil {
		t.Fatalf("UpdateESME without routes at schema %d = %v", d.Schema(), err)
	}
}

func TestSchemaOneAppliesLikeTheBinaryBeforeRoutes(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	user := createTestUser(t, d, "app@example.com", RoleSubmitter)

	app := testESME("app", user)
	app.Routes = []string{"4000"}

	id, err := submit[int64](ctx, d, cmdCreateESME, app)
	if err != nil {
		t.Fatalf("an ESME with routes applied at schema %d = %v; it must be created as before routes", d.Schema(), err)
	}

	app.ID = id

	if _, err := submit[struct{}](ctx, d, cmdUpdateESME, app); err != nil {
		t.Fatalf("an ESME update with routes applied at schema %d = %v", d.Schema(), err)
	}

	m := NewMessage{
		Originator:  testOriginator,
		Recipient:   Address{Digits: "1234", TypeOfNumber: tonNetworkSpecific, NumberingPlan: 1},
		MSISDN:      "1234",
		Origin:      OriginMobile,
		TPDU:        submitTPDU("1234", false),
		SubmittedAt: time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	messageID, err := d.CreateMessage(ctx, m)
	if err != nil {
		t.Fatalf("a network-specific recipient applied at schema %d = %v; it must be stored as before routes",
			d.Schema(), err)
	}

	got, err := d.GetMessage(ctx, messageID)
	if err != nil || got.Status != StatusPending {
		t.Fatalf("message = %+v, %v; want pending", got, err)
	}
}

func TestRejectDuplicatesCoversDelegatedMessages(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	m := delegatedMessage(f, "40001", false, f.now.Add(time.Hour))
	m.MessageReference = 0x2a
	m.RejectDuplicates = true

	if _, err := f.d.CreateMessage(ctx, m); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	other := delegatedMessage(f, "40002", false, f.now.Add(time.Hour))
	other.MessageReference = 0x2b

	if _, err := f.d.CreateMessage(ctx, other); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if _, err := f.d.CreateMessage(ctx, m); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("a resent SMS-SUBMIT for a delegated message = %v, want %v", err, ErrDuplicate)
	}
}

func TestDelegatedMessageNeverSchedulesItsRecipient(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	if _, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Hour))); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	var next sql.Null[int64]

	if err := f.d.writer.QueryRowContext(ctx, `SELECT next_attempt_at FROM recipients WHERE msisdn = ?`,
		"155540001").Scan(&next); err != nil {
		t.Fatal(err)
	}

	if next.Valid {
		t.Fatalf("recipient next_attempt_at = %d; a delegated message must not schedule network delivery", next.V)
	}
}

func TestExpireMODeliveriesCountsOnlyTheMessagesItExpires(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", false, f.now.Add(time.Minute)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if _, err := f.d.writer.ExecContext(ctx, `UPDATE messages SET status = ? WHERE id = ?`, StatusFailed,
		id); err != nil {
		t.Fatal(err)
	}

	if n, err := f.d.ExpireMODeliveries(ctx, f.now.Add(2*time.Minute)); err != nil || n != 0 {
		t.Fatalf("expire = %d, %v; the message was no longer delegated", n, err)
	}
}

func TestCloseMODeliveryWithNoUserData(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	id, err := f.d.CreateMessage(ctx, delegatedMessage(f, "40001", true, f.now.Add(time.Hour)))
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if err := f.d.CloseMODelivery(ctx, moDeliveryRows(t, f.d, id)[0].ID, NotificationNoUserData,
		f.now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseMODelivery: %v", err)
	}

	if m, err := f.d.GetMessage(ctx, id); err != nil || m.Status != StatusFailed {
		t.Fatalf("message = %+v, %v; want %v", m, err, StatusFailed)
	}

	if attempts := attemptRows(t, f.d, id); len(attempts) != 2 || attempts[1].Outcome != "esme_undecodable" {
		t.Fatalf("attempts = %+v", attempts)
	}

	reports, err := f.d.StatusReportsFor(ctx, []int64{id})
	if err != nil || len(reports[id]) != 1 || reports[id][0].ReportStatus != uint8(tpdu.StatusNoInterworking) {
		t.Fatalf("reports = %+v, %v", reports, err)
	}
}

func TestUndecodableDelegatedMessagesCompleteWithoutAReport(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	undecodable := func(to string, expires time.Time) int64 {
		t.Helper()

		m := delegatedMessage(f, to, true, expires)
		m.TPDU = []byte{0x01}

		id, err := f.d.CreateMessage(ctx, m)
		if err != nil {
			t.Fatalf("CreateMessage: %v", err)
		}

		return id
	}

	acknowledged := undecodable("40001", f.now.Add(time.Hour))
	expired := undecodable("40002", f.now.Add(time.Minute))
	deleted := undecodable("40003", f.now.Add(time.Hour))

	if err := f.d.CloseMODelivery(ctx, moDeliveryRows(t, f.d, acknowledged)[0].ID, NotificationAcknowledged,
		f.now.Add(time.Second)); err != nil {
		t.Fatalf("CloseMODelivery: %v", err)
	}

	if n, err := f.d.ExpireMODeliveries(ctx, f.now.Add(2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("ExpireMODeliveries = %d, %v", n, err)
	}

	if err := f.d.DeleteESME(ctx, f.app); err != nil {
		t.Fatalf("DeleteESME: %v", err)
	}

	for id, want := range map[int64]MessageStatus{
		acknowledged: StatusDelivered, expired: StatusExpired, deleted: StatusFailed,
	} {
		if m, err := f.d.GetMessage(ctx, id); err != nil || m.Status != want {
			t.Errorf("message %d = %+v, %v; want %v", id, m, err, want)
		}
	}

	reports, err := f.d.StatusReportsFor(ctx, []int64{acknowledged, expired, deleted})
	if err != nil || len(reports) != 0 {
		t.Fatalf("reports = %+v, %v; an undecodable SMS-SUBMIT has no report to build", reports, err)
	}
}

func TestRouteForms(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange, "400010-400019", "4000")
	national := createTestESME(t, f.d, func() ESME {
		e := testESME("national", f.user)
		e.Routes = []string{"+3340000-+3349999", "+33400000-+33499999"}

		return e
	}())
	us := createTestESME(t, f.d, func() ESME {
		e := testESME("us", f.user)
		e.Routes = []string{"+15551230000-+15551239999"}

		return e
	}())

	tests := []struct {
		name   string
		to     Address
		msisdn string
		want   int64
		err    error
	}{
		{"dialled short number before an E.164 range", Address{Digits: "40001", TypeOfNumber: 0, NumberingPlan: 1}, "3340001", f.app, nil},
		{"national number as dialled", Address{Digits: "400012", TypeOfNumber: 2, NumberingPlan: 1}, "33400012", f.app, nil},
		{"E.164 range of a national number", Address{Digits: "401234", TypeOfNumber: 2, NumberingPlan: 1}, "33401234", national, nil},
		{"longer number than the short code", Address{Digits: "40001234", TypeOfNumber: 0, NumberingPlan: 1}, "3340001234", 0, nil},
		{"international number is not a dialled short number", Address{Digits: "40001", TypeOfNumber: 1, NumberingPlan: 1}, "40001", 0, nil},
		{"E.164 range of an international number", Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1}, "15551230002", us, nil},
		{"abbreviated number with a subaddress", Address{Digits: "4000#12", TypeOfNumber: 6, NumberingPlan: 1}, "4000#12", f.app, nil},
		{"subaddress digits are not routed", Address{Digits: "4#0001", TypeOfNumber: 6, NumberingPlan: 1}, "4#0001", 0, ErrNoRoute},
		{"abbreviated number without a route", Address{Digits: "999", TypeOfNumber: 6, NumberingPlan: 1}, "999", 0, ErrNoRoute},
		{"network-specific number is not E.164", Address{Digits: "15551230002", TypeOfNumber: 3, NumberingPlan: 1}, "15551230002", 0, ErrNoRoute},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := delegatedMessage(f, "40001", false, f.now.Add(time.Hour))
			m.Recipient, m.MSISDN, m.MessageReference = tt.to, tt.msisdn, uint8(i)

			id, err := f.d.CreateMessage(ctx, m)
			if !errors.Is(err, tt.err) {
				t.Fatalf("CreateMessage = %v, want %v", err, tt.err)
			}

			if tt.err != nil {
				return
			}

			rows := moDeliveryRows(t, f.d, id)

			switch {
			case tt.want == 0 && len(rows) != 0:
				t.Fatalf("deliveries = %+v; want none", rows)
			case tt.want != 0 && (len(rows) != 1 || rows[0].ESMEID != tt.want):
				t.Fatalf("deliveries = %+v; want ESME %d", rows, tt.want)
			}
		})
	}
}

func TestParseRoute(t *testing.T) {
	for s, want := range map[string]string{
		"4000":                      "4000",
		"4000-4000":                 "4000",
		"40000-40099":               "40000-40099",
		"0-9":                       "0-9",
		"+15551230000-+15551230999": "+15551230000-+15551230999",
		"+4000":                     "+4000",
		"+123456789012345":          "+123456789012345",
		"12345678901234567890":      "12345678901234567890",
	} {
		r, err := ParseRoute(s)
		if err != nil || r.String() != want {
			t.Errorf("ParseRoute(%q) = %q, %v; want %q", s, r, err, want)
		}
	}

	for _, s := range []string{
		"", "+", "-", "4000-", "-4000", "40a0", "++4000", "4+000", "+1234567890123456", "123456789012345678901",
		"40000-4009", "40009-40000", "+15551230000-15551230999", "15551230000-+15551230999", "4000-4001-4002",
	} {
		if r, err := ParseRoute(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRoute(%q) = %q, %v; want %v", s, r, err, ErrInvalid)
		}
	}
}

func TestRouteOverlaps(t *testing.T) {
	route := func(s string) Route {
		t.Helper()

		r, err := ParseRoute(s)
		if err != nil {
			t.Fatal(err)
		}

		return r
	}

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"40000-40009", "40005", true},
		{"40000-40009", "40009-40019", true},
		{"40000-40009", "39990-40000", true},
		{"40000-40009", "39999-40010", true},
		{"40000-40009", "40010-40019", false},
		{"40000-40009", "4000", false},
		{"40000-40009", "400000-400099", false},
		{"40000-40009", "+40000-+40009", false},
		{"+4000-+4009", "+4005", true},
	} {
		if got := route(tc.a).Overlaps(route(tc.b)); got != tc.want {
			t.Errorf("%s overlaps %s = %v, want %v", tc.a, tc.b, got, tc.want)
		}

		if got := route(tc.b).Overlaps(route(tc.a)); got != tc.want {
			t.Errorf("%s overlaps %s = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestReplaceADelegatedMessage(t *testing.T) {
	ctx := context.Background()
	f := newMOFixture(t, testRange)

	first := delegatedMessage(f, "40001", true, f.now.Add(time.Hour))
	first.ProtocolIdentifier, first.Replace, first.ReportReplaced = 0x41, true, true

	firstID, err := f.d.CreateMessage(ctx, first)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	delivery := moDeliveryRows(t, f.d, firstID)[0].ID

	second := first
	second.Recipient, second.MSISDN, second.MessageReference = Address{Digits: "40002", NumberingPlan: 1}, "155540002", 0x2b
	second.SubmittedAt = f.now.Add(time.Minute)

	secondID, err := f.d.CreateMessage(ctx, second)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if m, err := f.d.GetMessage(ctx, firstID); err != nil || m.Status != StatusReplaced {
		t.Fatalf("first = %+v, %v; want %v", m, err, StatusReplaced)
	}

	if rows := moDeliveryRows(t, f.d, firstID); len(rows) != 1 || rows[0].Result != string(NotificationReplaced) {
		t.Fatalf("first delivery = %+v; want %s", rows, NotificationReplaced)
	}

	if err := f.d.CloseMODelivery(ctx, delivery, NotificationAcknowledged, f.now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a late acknowledgement of the replaced message = %v, want %v", err, ErrNotFound)
	}

	reports, err := f.d.StatusReportsFor(ctx, []int64{firstID})
	if err != nil || len(reports[firstID]) != 1 || reports[firstID][0].ReportStatus != uint8(tpdu.StatusReplaced) {
		t.Fatalf("reports = %+v, %v", reports, err)
	}

	if m, err := f.d.GetMessage(ctx, secondID); err != nil || m.Status != StatusDelegated {
		t.Fatalf("second = %+v, %v; want %v", m, err, StatusDelegated)
	}
}
