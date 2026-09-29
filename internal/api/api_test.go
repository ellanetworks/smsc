package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
)

var testNow = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

type fakeDiameter struct {
	peers     []diameter.PeerStatus
	available bool
}

func (f fakeDiameter) Identity() diameter.Identity {
	return diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org"}
}

func (f fakeDiameter) Peers() []diameter.PeerStatus { return f.peers }

func (f fakeDiameter) HSSAvailable() bool { return f.available }

type testAPI struct {
	handler  http.Handler
	store    *db.DB
	notified atomic.Int32
}

func newTestAPI(t *testing.T, d api.Diameter) *testAPI {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	a := &testAPI{store: store}
	a.handler = api.NewHandler(api.Config{
		Store:           store,
		Diameter:        d,
		Notify:          func() { a.notified.Add(1) },
		DefaultValidity: 24 * time.Hour,
		Now:             func() time.Time { return testNow },
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	return a
}

func (a *testAPI) do(t *testing.T, method, path, body string) (int, json.RawMessage, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)

	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s %s: invalid JSON %q", method, path, rec.Body.String())
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}

	return rec.Code, resp.Result, resp.Error
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()

	var v T

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}

	return v
}

func (a *testAPI) create(t *testing.T, from, to, text string) []api.Message {
	t.Helper()

	body, _ := json.Marshal(api.CreateMessageParams{From: from, To: to, Text: text})

	code, result, errMsg := a.do(t, http.MethodPost, "/api/v1/messages", string(body))
	if code != http.StatusCreated {
		t.Fatalf("create = %d %q", code, errMsg)
	}

	return decode[api.CreateMessageResponse](t, result).Items
}

func str(s string) *string {
	return &s
}

func TestCreateMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	got := a.create(t, "+15550001111", "+15551230002", "hello")

	want := []api.Message{{
		ID:            1,
		From:          "+15550001111",
		To:            "+15551230002",
		Text:          str("hello"),
		Encoding:      "gsm7",
		Status:        "pending",
		CreatedAt:     "2026-09-29T10:00:00.000Z",
		UpdatedAt:     "2026-09-29T10:00:00.000Z",
		ExpiresAt:     "2026-09-30T10:00:00.000Z",
		NextAttemptAt: "2026-09-29T10:00:00.000Z",
	}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %+v, want %+v", got, want)
	}

	if a.notified.Load() != 1 {
		t.Fatal("the delivery worker was not notified")
	}

	m, err := a.store.GetMessage(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}

	if m.MSISDN != "15551230002" || m.Origin != db.OriginAPI || m.Originator != (db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1}) ||
		!bytes.Equal(m.TPDU, []byte{0x01, 0x00, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0x00, 0xf2, 0x00, 0x00, 0x05, 0xe8, 0x32, 0x9b, 0xfd, 0x06}) {
		t.Fatalf("stored = %+v (TPDU %x)", m, m.TPDU)
	}
}

func TestCreateConcatenatedMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	text := strings.Repeat("日本語", 30)
	got := a.create(t, "+15550001111", "+15551230002", text)

	if len(got) != 2 {
		t.Fatalf("items = %d, want 2", len(got))
	}

	var joined strings.Builder

	for i, m := range got {
		c := m.Concatenation
		if m.Encoding != "ucs2" || c == nil || c.Part != uint8(i+1) || c.Total != 2 || c.Reference != got[0].Concatenation.Reference {
			t.Fatalf("part %d = %+v, concatenation %+v", i+1, m, c)
		}

		joined.WriteString(*m.Text)
	}

	if joined.String() != text {
		t.Fatalf("text = %q", joined.String())
	}

	next := a.create(t, "+15550001111", "+15551230002", text)
	if next[0].Concatenation.Reference == got[0].Concatenation.Reference {
		t.Fatal("two long texts share a concatenation reference")
	}
}

func TestCreateMessageRejectsInvalidInput(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	tests := map[string]string{
		"not JSON":           `hello`,
		"missing from":       `{"to": "+15551230002", "text": "hi"}`,
		"from without plus":  `{"from": "15550001111", "to": "+15551230002", "text": "hi"}`,
		"from leading zero":  `{"from": "+05550001111", "to": "+15551230002", "text": "hi"}`,
		"from too long":      `{"from": "+1234567890123456", "to": "+15551230002", "text": "hi"}`,
		"alphanumeric to":    `{"from": "+15550001111", "to": "+1555ABC", "text": "hi"}`,
		"missing text":       `{"from": "+15550001111", "to": "+15551230002"}`,
		"text over 255 SMSs": `{"from": "+15550001111", "to": "+15551230002", "text": "` + strings.Repeat("a", 255*153+1) + `"}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if code, _, errMsg := a.do(t, http.MethodPost, "/api/v1/messages", body); code != http.StatusBadRequest || errMsg == "" {
				t.Fatalf("code = %d, error = %q", code, errMsg)
			}
		})
	}

	if _, total, _ := a.store.ListMessages(context.Background(), db.MessageFilter{}, 1, 10); total != 0 || a.notified.Load() != 0 {
		t.Fatalf("stored %d messages after rejected requests", total)
	}
}

func TestGetMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	id := a.create(t, "+15550001111", "+15551230002", "hello")[0].ID

	if _, err := a.store.CreateDeliveryAttempt(context.Background(), db.DeliveryAttempt{
		MessageID: id, StartedAt: testNow, CompletedAt: testNow, Step: db.StepRouting, Outcome: "timeout",
	}); err != nil {
		t.Fatal(err)
	}

	status, result, _ := a.do(t, http.MethodGet, "/api/v1/messages/1", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	if got := decode[api.Message](t, result); got.ID != id || *got.Text != "hello" {
		t.Fatalf("message = %+v", got)
	}
}

func TestListMessageAttempts(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	id := a.create(t, "+15550001111", "+15551230002", "hello")[0].ID

	code, failure, vendor := uint32(5550), uint32(5555), uint32(10415)
	ms := time.Millisecond

	for _, at := range []db.DeliveryAttempt{
		{
			MessageID: id, StartedAt: testNow, CompletedAt: testNow.Add(12 * ms), Step: db.StepRouting, Node: "hss.example.org",
			Outcome: "success", ResultCode: new(uint32(2001)),
		},
		{
			MessageID: id, StartedAt: testNow.Add(15 * ms), CompletedAt: testNow.Add(30*time.Second + 15*ms), Step: db.StepDelivery,
			Node: "mme.example.org", NodeType: db.NodeTypeMME, Outcome: "absent_user",
			ResultCode: &code, VendorID: &vendor, AbsentDiagnostic: "no_paging_response_msc",
		},
		{
			MessageID: id, StartedAt: testNow.Add(time.Minute), CompletedAt: testNow.Add(time.Minute + 9*ms), Step: db.StepRouting,
			Node: "hss.example.org", Outcome: "absent_user",
			ResultCode: &code, VendorID: &vendor, AbsentDiagnostics: db.AbsentDiagnostics{SMSF3GPP: "ms_purged_non_gprs"},
		},
		{
			MessageID: id, StartedAt: testNow.Add(2 * time.Minute), CompletedAt: testNow.Add(2*time.Minute + 480*ms), Step: db.StepDelivery,
			Node: "smsf.example.org", NodeType: db.NodeTypeSMSF3GPP, Outcome: "sm_delivery_failure", ResultCode: &failure, VendorID: &vendor,
			FailureCause: "equipment_protocol_error", TPFailureCause: "usim_sms_storage_full",
		},
	} {
		if _, err := a.store.CreateDeliveryAttempt(ctx, at); err != nil {
			t.Fatal(err)
		}
	}

	status, result, _ := a.do(t, http.MethodGet, "/api/v1/messages/1/attempts", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	got := decode[api.ListAttemptsResponse](t, result)

	want := []api.Attempt{
		{
			ID: 1, StartedAt: "2026-09-29T10:00:00.000Z", CompletedAt: "2026-09-29T10:00:00.012Z", Step: "routing",
			Node: "hss.example.org", Outcome: "success", ResultCode: new(uint32(2001)),
		},
		{
			ID: 2, StartedAt: "2026-09-29T10:00:00.015Z", CompletedAt: "2026-09-29T10:00:30.015Z", Step: "delivery",
			Node: "mme.example.org", NodeType: "mme", Outcome: "absent_user",
			ResultCode: &code, VendorID: &vendor, AbsentDiagnostic: "no_paging_response_msc",
		},
		{
			ID: 3, StartedAt: "2026-09-29T10:01:00.000Z", CompletedAt: "2026-09-29T10:01:00.009Z", Step: "routing",
			Node: "hss.example.org", Outcome: "absent_user",
			ResultCode: &code, VendorID: &vendor, AbsentDiagnostics: &api.AbsentDiagnostics{SMSF3GPP: "ms_purged_non_gprs"},
		},
		{
			ID: 4, StartedAt: "2026-09-29T10:02:00.000Z", CompletedAt: "2026-09-29T10:02:00.480Z", Step: "delivery",
			Node: "smsf.example.org", NodeType: "smsf_3gpp", Outcome: "sm_delivery_failure",
			ResultCode: &failure, VendorID: &vendor, FailureCause: "equipment_protocol_error", TPFailureCause: "usim_sms_storage_full",
		},
	}

	if !reflect.DeepEqual(got.Items, want) || got.Page != 1 || got.PerPage != 25 || got.TotalCount != 4 {
		t.Fatalf("attempts = %+v", got)
	}

	if !strings.Contains(string(result), `"absent_diagnostics":{"smsf_3gpp":"ms_purged_non_gprs"}`) ||
		strings.Count(string(result), `"absent_diagnostics"`) != 1 || strings.Count(string(result), `"failure_cause"`) != 1 ||
		strings.Count(string(result), `"node_type"`) != 2 {
		t.Fatalf("attempt details are not omitted when absent: %s", result)
	}

	status, result, _ = a.do(t, http.MethodGet, "/api/v1/messages/1/attempts?page=2&per_page=3", "")
	if page := decode[api.ListAttemptsResponse](t, result); status != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != 4 || page.TotalCount != 4 {
		t.Fatalf("second page = %d %+v", status, page)
	}
}

func TestListMessageAttemptsWithoutAttempts(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	a.create(t, "+15550001111", "+15551230002", "hello")

	status, result, _ := a.do(t, http.MethodGet, "/api/v1/messages/1/attempts", "")
	if got := decode[api.ListAttemptsResponse](t, result); status != http.StatusOK || got.Items == nil || len(got.Items) != 0 || got.TotalCount != 0 {
		t.Fatalf("status = %d, attempts = %s", status, result)
	}
}

func TestListMessageAttemptsErrors(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	a.create(t, "+15550001111", "+15551230002", "hello")

	tests := map[string]struct {
		path string
		code int
	}{
		"unknown message": {"/api/v1/messages/42/attempts", http.StatusNotFound},
		"invalid id":      {"/api/v1/messages/abc/attempts", http.StatusBadRequest},
		"invalid page":    {"/api/v1/messages/1/attempts?page=abc", http.StatusBadRequest},
		"zero page":       {"/api/v1/messages/1/attempts?page=0", http.StatusBadRequest},
		"page too large":  {"/api/v1/messages/1/attempts?per_page=101", http.StatusBadRequest},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if code, _, errMsg := a.do(t, http.MethodGet, tc.path, ""); code != tc.code || errMsg == "" {
				t.Fatalf("code = %d, error = %q", code, errMsg)
			}
		})
	}
}

func TestGetMessageErrors(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	if code, _, _ := a.do(t, http.MethodGet, "/api/v1/messages/42", ""); code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", code)
	}

	if code, _, _ := a.do(t, http.MethodGet, "/api/v1/messages/abc", ""); code != http.StatusBadRequest {
		t.Fatalf("invalid id = %d", code)
	}
}

func TestListMessages(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	a.create(t, "+15550001111", "+15551230002", "one")
	a.create(t, "+15550001111", "+15551230003", "two")
	a.create(t, "+15551230003", "+15551230002", "three")

	if err := a.store.SetMessageStatus(context.Background(), 3, db.StatusDelivered, testNow); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		query string
		ids   []int64
		total int
	}{
		"newest first":   {"", []int64{3, 2, 1}, 3},
		"by recipient":   {"?to=%2B15551230002", []int64{3, 1}, 2},
		"by originator":  {"?from=%2B15551230003", []int64{3}, 1},
		"by status":      {"?to=%2B15551230002&status=pending", []int64{1}, 1},
		"paged":          {"?page=2&per_page=2", []int64{1}, 3},
		"no match":       {"?to=%2B19999999999", []int64{}, 0},
		"delivered only": {"?status=delivered", []int64{3}, 1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages"+tc.query, "")
			if code != http.StatusOK {
				t.Fatalf("code = %d %q", code, errMsg)
			}

			resp := decode[api.ListMessagesResponse](t, result)

			ids := []int64{}
			for _, m := range resp.Items {
				ids = append(ids, m.ID)
			}

			if !reflect.DeepEqual(ids, tc.ids) || resp.TotalCount != tc.total {
				t.Fatalf("ids = %v (total %d), want %v (total %d)", ids, resp.TotalCount, tc.ids, tc.total)
			}
		})
	}
}

func TestListMessagesRejectsInvalidFilters(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	for _, query := range []string{"?page=0", "?page=abc", "?per_page=0", "?per_page=x", "?per_page=101", "?to=15551230002", "?from=abc", "?status=lost"} {
		if code, _, errMsg := a.do(t, http.MethodGet, "/api/v1/messages"+query, ""); code != http.StatusBadRequest || errMsg == "" {
			t.Fatalf("%s: code = %d, error = %q", query, code, errMsg)
		}
	}
}

func TestGetDiameterStatus(t *testing.T) {
	since := time.Date(2026, 9, 29, 9, 59, 0, 0, time.UTC)

	a := newTestAPI(t, fakeDiameter{
		available: true,
		peers: []diameter.PeerStatus{{
			Host:       "mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org",
			Realm:      "epc.mnc001.mcc001.3gppnetwork.org",
			RemoteAddr: netip.MustParseAddr("::ffff:10.0.0.5"),
			State:      diameter.PeerOpen,
			Since:      since,
			Applications: []diameter.Application{
				{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
				{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
				{ID: 42},
			},
		}},
	})

	code, result, _ := a.do(t, http.MethodGet, "/api/v1/diameter", "")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}

	want := api.DiameterStatus{
		Host:         "smsc.example.org",
		Realm:        "example.org",
		HSSAvailable: true,
		Peers: []api.DiameterPeer{{
			Host:         "mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org",
			Realm:        "epc.mnc001.mcc001.3gppnetwork.org",
			Address:      "10.0.0.5",
			State:        "open",
			Applications: []string{"s6c", "sgd", "42"},
			Since:        "2026-09-29T09:59:00.000Z",
		}},
	}

	if got := decode[api.DiameterStatus](t, result); !reflect.DeepEqual(got, want) {
		t.Fatalf("status = %+v, want %+v", got, want)
	}
}

func TestGetDiameterStatusWithoutPeers(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	_, result, _ := a.do(t, http.MethodGet, "/api/v1/diameter", "")
	if !strings.Contains(string(result), `"peers":[]`) || !strings.Contains(string(result), `"hss_available":false`) {
		t.Fatalf("result = %s", result)
	}
}
