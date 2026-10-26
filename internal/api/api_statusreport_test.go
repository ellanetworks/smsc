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

// createMobileMessage stores a message from a phone, whose SMS-SUBMIT asks for status reports when srr is set.
func createMobileMessage(t *testing.T, a *testAPI, srr bool, protocolIdentifier uint8, replace bool) db.Message {
	t.Helper()

	first := "01"
	if srr {
		first = "21"
	}

	b, err := hex.DecodeString(first + "07" + "0b91" + "5155210300f2" + "00" + "04" + "02" + "6869")
	if err != nil {
		t.Fatal(err)
	}

	from := db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1}
	to := db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1}

	id, err := a.store.CreateMessage(context.Background(), db.NewMessage{
		Originator: from, Recipient: to, MSISDN: to.Digits, Origin: db.OriginMobile, MessageReference: 7,
		ProtocolIdentifier: protocolIdentifier, Replace: replace, TPDU: b, SubmittedAt: testNow,
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

func TestMessageStatusReports(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	m := createMobileMessage(t, a, true, 0, false)
	delivered := testNow.Add(time.Hour / 2)

	report, err := statusreport.For(m, tpdu.StatusReceived, delivered, reportSettings)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.store.CompleteMessage(ctx, m.ID, db.StatusDelivered, delivered, report); err != nil {
		t.Fatal(err)
	}

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages/1", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d %q", code, errMsg)
	}

	want := api.StatusReports{
		Requested: true,
		Reports: []api.StatusReport{{
			ID: 2, TPStatus: 0x00, TPStatusName: "received", Status: "pending",
			CreatedAt: "2026-09-29T10:30:00.000Z", UpdatedAt: "2026-09-29T10:30:00.000Z", ExpiresAt: "2026-09-30T10:30:00.000Z",
			NextAttemptAt: "2026-09-29T10:30:00.000Z",
		}},
	}

	if got := decode[api.Message](t, result); got.Status != "delivered" || !reflect.DeepEqual(got.StatusReport, want) {
		t.Fatalf("status_report = %+v, want %+v", got.StatusReport, want)
	}

	// The list shows the report under its message, and not on its own.
	code, result, _ = a.do(t, http.MethodGet, "/api/v1/messages", "")
	if list := decode[api.ListMessagesResponse](t, result); code != http.StatusOK || list.TotalCount != 1 ||
		!reflect.DeepEqual(list.Items[0].StatusReport, want) {
		t.Fatalf("list = %d %+v", code, list)
	}

	for _, path := range []string{"/api/v1/messages/2", "/api/v1/messages/2/attempts"} {
		if code, _, errMsg := a.do(t, http.MethodGet, path, ""); code != http.StatusNotFound || errMsg == "" {
			t.Errorf("%s: code = %d, error = %q, want 404", path, code, errMsg)
		}
	}
}

func TestMessageWithoutStatusReports(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	// A message from the API never asks for status reports, nor does a phone without TP-SRR.
	fromAPI := a.create(t, "+15550001111", "+15551230002", "hi")[0].ID
	fromPhone := createMobileMessage(t, a, false, 0, false).ID

	for _, id := range []int64{fromAPI, fromPhone} {
		code, result, _ := a.do(t, http.MethodGet, "/api/v1/messages/"+strconv.FormatInt(id, 10), "")
		if got := decode[api.Message](t, result); code != http.StatusOK || got.StatusReport.Requested ||
			got.StatusReport.Reports == nil || len(got.StatusReport.Reports) != 0 {
			t.Fatalf("message %d: status_report = %+v", id, got.StatusReport)
		}
	}
}

func TestReplacedMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	replaced := createMobileMessage(t, a, true, 0x41, true)
	replacement := createMobileMessage(t, a, true, 0x41, true)

	code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages?status=replaced", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d %q", code, errMsg)
	}

	list := decode[api.ListMessagesResponse](t, result)
	if list.TotalCount != 1 || list.Items[0].ID != replaced.ID || list.Items[0].Status != "replaced" {
		t.Fatalf("replaced messages = %+v", list)
	}

	if r := list.Items[0].StatusReport.Reports; len(r) != 1 || r[0].TPStatus != 0x02 || r[0].TPStatusName != "replaced" {
		t.Fatalf("reports = %+v", r)
	}

	_, result, _ = a.do(t, http.MethodGet, "/api/v1/messages/"+strconv.FormatInt(replacement.ID, 10), "")
	if got := decode[api.Message](t, result); got.Status != "pending" || len(got.StatusReport.Reports) != 0 {
		t.Fatalf("replacement = %+v", got)
	}
}
