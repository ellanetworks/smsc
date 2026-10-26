package api_test

import (
	"context"
	"encoding/hex"
	"net/http"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/statusreport"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

var reportSettings = settings.Settings{
	Operator: settings.Operator{ServiceCentreAddress: "15550000000"},
	Delivery: settings.Delivery{DefaultValidity: 24 * time.Hour},
}

// createMobileMessage stores a message from a phone, whose SMS-SUBMIT asks for status reports with the Selective Status
// Report (TS 23.040 clause 9.2.3.24.5).
func createMobileMessage(t *testing.T, a *testAPI, selective byte, protocolIdentifier uint8, replace bool) db.Message {
	t.Helper()

	b, err := hex.DecodeString("61" + "07" + "0b91" + "5155210300f2" + "00" + "04" + "04" + "030601")
	if err != nil {
		t.Fatal(err)
	}

	from := db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1}
	to := db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1}

	id, err := a.store.CreateMessage(context.Background(), db.NewMessage{
		Originator: from, Recipient: to, MSISDN: to.Digits, Origin: db.OriginMobile, MessageReference: 7,
		ProtocolIdentifier: protocolIdentifier, Replace: replace, TPDU: append(b, selective), SubmittedAt: testNow,
		ExpiresAt: testNow.Add(time.Hour),
		ReportReplaced: func(replaced db.Message) (*db.NewStatusReport, error) {
			return statusreport.For(replaced, tpdu.StatusReplaced, testNow, reportSettings)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	m, err := a.store.GetMessage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func report(t *testing.T, m db.Message, status tpdu.Status, at time.Time) *db.NewStatusReport {
	t.Helper()

	r, err := statusreport.For(m, status, at, reportSettings)
	if err != nil || r == nil {
		t.Fatalf("For = %+v, %v", r, err)
	}

	return r
}

func TestMessageStatusReports(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	m := createMobileMessage(t, a, 0x0f, 0, false)
	retried, delivered := testNow.Add(time.Minute), testNow.Add(time.Hour/2)

	if err := a.store.ScheduleRetry(ctx, m.ID, retried.Add(time.Minute), retried, true,
		report(t, m, tpdu.TemporaryNoResponse.StillTrying(), retried)); err != nil {
		t.Fatal(err)
	}

	if err := a.store.CompleteMessage(ctx, m.ID, db.StatusDelivered, delivered, report(t, m, tpdu.StatusReceived, delivered)); err != nil {
		t.Fatal(err)
	}

	// The interim report was delivered.
	if err := a.store.CompleteMessage(ctx, 2, db.StatusDelivered, delivered, nil); err != nil {
		t.Fatal(err)
	}

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages/1", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d %q", code, errMsg)
	}

	want := api.StatusReports{
		Requested: true,
		Classes:   []string{"still_trying", "completed", "permanent", "gave_up"},
		Reports: []api.StatusReport{
			{
				ID: 2, TPStatus: 0x22, TPStatusName: "no_response", Class: "still_trying", Status: "delivered",
				CreatedAt: "2026-09-29T10:01:00.000Z", UpdatedAt: "2026-09-29T10:30:00.000Z", ExpiresAt: "2026-09-30T10:01:00.000Z",
			},
			{
				ID: 3, TPStatus: 0x00, TPStatusName: "received", Class: "completed", Status: "pending",
				CreatedAt: "2026-09-29T10:30:00.000Z", UpdatedAt: "2026-09-29T10:30:00.000Z", ExpiresAt: "2026-09-30T10:30:00.000Z",
				NextAttemptAt: "2026-09-29T10:30:00.000Z",
			},
		},
	}

	if got := decode[api.Message](t, result); got.Status != "delivered" || !reflect.DeepEqual(got.StatusReport, want) {
		t.Fatalf("status_report = %+v, want %+v", got.StatusReport, want)
	}

	// The list shows the reports under their message, and not on their own.
	code, result, _ = a.do(t, http.MethodGet, "/api/v1/messages", "")
	if list := decode[api.ListMessagesResponse](t, result); code != http.StatusOK || list.TotalCount != 1 ||
		!reflect.DeepEqual(list.Items[0].StatusReport, want) {
		t.Fatalf("list = %d %+v", code, list)
	}
}

func TestMessageWithoutStatusReports(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	// A message from the API never asks for status reports.
	code, result, _ := a.do(t, http.MethodGet, "/api/v1/messages/"+itoa(a.create(t, "+15550001111", "+15551230002", "hi")[0].ID), "")
	if got := decode[api.Message](t, result); code != http.StatusOK || got.StatusReport.Requested ||
		got.StatusReport.Classes == nil || len(got.StatusReport.Classes) != 0 ||
		got.StatusReport.Reports == nil || len(got.StatusReport.Reports) != 0 {
		t.Fatalf("status_report = %+v", got.StatusReport)
	}

	// Nor does a phone that selects no outcome.
	m := createMobileMessage(t, a, 0x00, 0, false)

	_, result, _ = a.do(t, http.MethodGet, "/api/v1/messages/"+itoa(m.ID), "")
	if got := decode[api.Message](t, result); !got.StatusReport.Requested || len(got.StatusReport.Classes) != 0 {
		t.Fatalf("status_report = %+v", got.StatusReport)
	}
}

func TestReplacedMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	replaced := createMobileMessage(t, a, 0x01, 0x41, true)
	replacement := createMobileMessage(t, a, 0x01, 0x41, true)

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages?status=replaced", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d %q", code, errMsg)
	}

	list := decode[api.ListMessagesResponse](t, result)
	if list.TotalCount != 1 || list.Items[0].ID != replaced.ID || list.Items[0].Status != "replaced" {
		t.Fatalf("replaced messages = %+v", list)
	}

	if r := list.Items[0].StatusReport.Reports; len(r) != 1 || r[0].TPStatus != 0x02 || r[0].TPStatusName != "replaced" ||
		r[0].Class != "completed" {
		t.Fatalf("reports = %+v", r)
	}

	_, result, _ = a.do(t, http.MethodGet, "/api/v1/messages/"+itoa(replacement.ID), "")
	if got := decode[api.Message](t, result); got.Status != "pending" || len(got.StatusReport.Reports) != 0 {
		t.Fatalf("replacement = %+v", got)
	}
}

func TestStatusReportAttempts(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	m := createMobileMessage(t, a, 0x01, 0, false)
	other := a.create(t, "+15550001111", "+15551230002", "other")[0]

	if err := a.store.CompleteMessage(ctx, m.ID, db.StatusDelivered, testNow, report(t, m, tpdu.StatusReceived, testNow)); err != nil {
		t.Fatal(err)
	}

	reportID := int64(3)

	if _, err := a.store.CreateDeliveryAttempt(ctx, db.DeliveryAttempt{
		MessageID: reportID, StartedAt: testNow, CompletedAt: testNow, Step: db.StepRouting, Outcome: "absent_user",
	}); err != nil {
		t.Fatal(err)
	}

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages/1/status_reports/3/attempts", "")
	if got := decode[api.ListAttemptsResponse](t, result); code != http.StatusOK || got.TotalCount != 1 || got.Items[0].Outcome != "absent_user" {
		t.Fatalf("attempts = %d %q %+v", code, errMsg, got)
	}

	// A status report is not a message of its own, and is only under the message it is on.
	for path, want := range map[string]int{
		"/api/v1/messages/3":          http.StatusNotFound,
		"/api/v1/messages/3/attempts": http.StatusNotFound,
		"/api/v1/messages/" + itoa(other.ID) + "/status_reports/3/attempts": http.StatusNotFound,
		"/api/v1/messages/1/status_reports/1/attempts":                      http.StatusNotFound,
		"/api/v1/messages/1/status_reports/42/attempts":                     http.StatusNotFound,
		"/api/v1/messages/1/status_reports/abc/attempts":                    http.StatusBadRequest,
		"/api/v1/messages/1/status_reports/3/attempts?page=0":               http.StatusBadRequest,
	} {
		if code, _, errMsg := a.do(t, http.MethodGet, path, ""); code != want || errMsg == "" {
			t.Errorf("%s: code = %d, error = %q, want %d", path, code, errMsg, want)
		}
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
