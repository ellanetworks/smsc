package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/ellanetworks/smsc/internal/settings"
	"golang.org/x/crypto/bcrypt"
)

var testNow = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

type fakeDiameter struct {
	peers  []diameter.PeerStatus
	routes []api.Route
}

func (f fakeDiameter) Identity() diameter.Identity {
	return diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org"}
}

func (f fakeDiameter) Peers() []diameter.PeerStatus { return f.peers }

func (f fakeDiameter) Peer(id string) (diameter.PeerStatus, bool) {
	for _, p := range f.peers {
		if p.ID == id {
			return p, true
		}
	}

	return diameter.PeerStatus{}, false
}

func (f fakeDiameter) Routes() []api.Route { return f.routes }

type testAPI struct {
	handler  http.Handler
	store    *db.DB
	notified atomic.Int32
	// token is the access token of the admin, which do sends.
	token string
	// now is the time of the API's clock.
	now time.Time
	// logs are the API's logs, as JSON lines.
	logs bytes.Buffer
}

const (
	testAdmin    = "admin@example.com"
	testPassword = "correct horse"
)

var testJWTSecret = []byte("0123456789abcdef0123456789abcdef")

// testConfig is the configuration of an API on a store, with authentication at the cheapest bcrypt cost.
func testConfig(t *testing.T, store *db.DB) api.Config {
	t.Helper()

	return api.Config{
		Store:      store,
		Diameter:   fakeDiameter{},
		Settings:   settings.NewLive(store, testSettings(t, store)),
		Notify:     func() {},
		Auth:       store,
		ESMEs:      store,
		JWTSecret:  testJWTSecret,
		BcryptCost: bcrypt.MinCost,
		Now:        func() time.Time { return testNow },
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func openStore(t *testing.T) *db.DB {
	t.Helper()

	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "smsc.db"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close() })

	return store
}

// initialize creates the first user, an admin, and returns its access token.
func initialize(t *testing.T, h http.Handler) string {
	t.Helper()

	rec := serve(h, request(http.MethodPost, "/api/v1/init",
		`{"email": "`+testAdmin+`", "password": "`+testPassword+`"}`, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/init = %d %s", rec.Code, rec.Body)
	}

	var resp struct {
		Result api.LoginResponse `json:"result"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	return resp.Result.Token
}

// request is a request with a bearer token, unless token is empty.
func request(method, path, body, token string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func testSettings(t *testing.T, store *db.DB) settings.Settings {
	t.Helper()

	s, err := store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	s.Delivery.DefaultValidity = 24 * time.Hour

	return s
}

func newTestAPI(t *testing.T, d api.Diameter) *testAPI {
	t.Helper()

	a := newUninitializedAPI(t, d)
	a.token = initialize(t, a.handler)

	return a
}

// newUninitializedAPI returns an API without users, whose clock is a.now.
func newUninitializedAPI(t *testing.T, d api.Diameter) *testAPI {
	t.Helper()

	store := openStore(t)
	a := &testAPI{store: store, now: testNow}

	cfg := testConfig(t, store)
	cfg.Diameter = d
	cfg.Notify = func() { a.notified.Add(1) }
	cfg.Now = func() time.Time { return a.now }
	cfg.Logger = slog.New(slog.NewJSONHandler(&a.logs, nil))

	a.handler = api.NewHandler(cfg)

	return a
}

// do sends a request as the admin, and returns its status, and its result or error.
func (a *testAPI) do(t *testing.T, method, path, body string) (int, json.RawMessage, string) {
	t.Helper()

	return a.doAs(t, a.token, method, path, body)
}

// doAs sends a request with a bearer token, or none when token is empty.
func (a *testAPI) doAs(t *testing.T, token, method, path, body string) (int, json.RawMessage, string) {
	t.Helper()

	rec := serve(a.handler, request(method, path, body, token))

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

func TestCreateMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	got := a.create(t, "+15550001111", "+15551230002", "hello")

	want := []api.Message{{
		ID:            1,
		From:          "+15550001111",
		To:            "+15551230002",
		Encoding:      "gsm7",
		Status:        "pending",
		CreatedAt:     "2026-09-29T10:00:00.000Z",
		UpdatedAt:     "2026-09-29T10:00:00.000Z",
		ExpiresAt:     "2026-09-30T10:00:00.000Z",
		NextAttemptAt: "2026-09-29T10:00:00.000Z",
		StatusReport:  api.StatusReports{Reports: []api.StatusReport{}},
		SubmittedBy:   testAdmin,
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

	named := a.create(t, "EllaSMSC", "+15551230002", "hello")
	if len(named) != 1 || named[0].From != "EllaSMSC" {
		t.Fatalf("items = %+v, want one from EllaSMSC", named)
	}

	m, err = a.store.GetMessage(context.Background(), named[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	if m.Originator != (db.Address{Digits: "EllaSMSC", TypeOfNumber: 5, NumberingPlan: 0}) {
		t.Fatalf("stored originator = %+v", m.Originator)
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

		joined.WriteString(*a.content(t, a.token, m.ID).Text)
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

	const (
		e164From = "from must be an E.164 number such as +15551230001"
		nameFrom = "from must be an E.164 number such as +15551230001, or a name of 1 to 11 letters and digits, with single spaces between words"
	)

	tests := map[string]struct {
		body, err string
	}{
		"not JSON":            {`hello`, "Invalid request data"},
		"missing from":        {`{"to": "+15551230002", "text": "hi"}`, nameFrom},
		"from without plus":   {`{"from": "15550001111", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"from leading zero":   {`{"from": "+05550001111", "to": "+15551230002", "text": "hi"}`, e164From},
		"from too long":       {`{"from": "+1234567890123456", "to": "+15551230002", "text": "hi"}`, e164From},
		"from plus name":      {`{"from": "+EllaSMSC", "to": "+15551230002", "text": "hi"}`, e164From},
		"empty name":          {`{"from": "", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"all-digit name":      {`{"from": "15551230001", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name too long":       {`{"from": "EllaNetworks", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name underscore":     {`{"from": "Ella_Net", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name not ASCII":      {`{"from": "Café", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name only spaces":    {`{"from": "   ", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name leading space":  {`{"from": " Ella", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name trailing space": {`{"from": "Ella ", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"name double space":   {`{"from": "ACME  Ltd", "to": "+15551230002", "text": "hi"}`, nameFrom},
		"alphanumeric to":     {`{"from": "+15550001111", "to": "+1555ABC", "text": "hi"}`, "to must be an E.164 number such as +15551230002"},
		"name to":             {`{"from": "+15550001111", "to": "EllaSMSC", "text": "hi"}`, "to must be an E.164 number such as +15551230002"},
		"missing text":        {`{"from": "+15550001111", "to": "+15551230002"}`, "text or binary is required"},
		"text over 255 SMSs":  {`{"from": "+15550001111", "to": "+15551230002", "text": "` + strings.Repeat("a", 255*153+1) + `"}`, "text is too long"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if code, _, errMsg := a.do(t, http.MethodPost, "/api/v1/messages", tc.body); code != http.StatusBadRequest || errMsg != tc.err {
				t.Fatalf("code = %d, error = %q; want %q", code, errMsg, tc.err)
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

	if got := decode[api.Message](t, result); got.ID != id || got.Encoding != "gsm7" {
		t.Fatalf("message = %+v", got)
	}

	if strings.Contains(string(result), "hello") {
		t.Fatalf("GET /api/v1/messages/1 carries the content: %s", result)
	}
}

// content returns the content of a message, read with token.
func (a *testAPI) content(t *testing.T, token string, id int64) api.MessageContent {
	t.Helper()

	code, result, errMsg := a.doAs(t, token, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/content", id), "")
	if code != http.StatusOK {
		t.Fatalf("GET content of %d = %d %q", id, code, errMsg)
	}

	return decode[api.MessageContent](t, result)
}

// auditEvents returns the details of the audit events with action that the API logged, oldest first.
func (a *testAPI) auditEvents(t *testing.T, action string) []string {
	t.Helper()

	var details []string

	for line := range strings.Lines(a.logs.String()) {
		var e struct{ Msg, Action, Actor, Details string }
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}

		if e.Msg == "audit event" && e.Action == action {
			details = append(details, e.Actor+": "+e.Details)
		}
	}

	return details
}

func TestGetMessageContent(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	id := a.create(t, "+15550001111", "+15551230002", "hello")[0].ID

	binary, err := a.store.CreateMessage(ctx, db.NewMessage{
		Originator: db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:  db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:     "15551230002", Origin: db.OriginMobile,
		// TP-DCS 0x04: 8-bit data.
		TPDU:        []byte{0x01, 0x00, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0x00, 0xf2, 0x00, 0x04, 0x02, 0xca, 0xfe},
		SubmittedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := a.content(t, a.token, id); got.Text == nil || *got.Text != "hello" {
		t.Fatalf("content = %+v", got)
	}

	if got := a.content(t, a.token, binary); !reflect.DeepEqual(got, api.MessageContent{Binary: &api.BinaryContent{
		DataCodingScheme: 0x04, UserData: api.Hex{0xca, 0xfe},
	}}) {
		t.Fatalf("content of a binary message = %+v", got)
	}

	want := []string{
		fmt.Sprintf("%s: Content of message %d read", testAdmin, id),
		fmt.Sprintf("%s: Content of message %d read", testAdmin, binary),
	}
	if got := a.auditEvents(t, "read_message_content"); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit events = %q, want %q", got, want)
	}
}

func TestGetMessageContentRefusals(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	submitter := a.userLogin(t, "ota@example.com", db.RoleSubmitter)
	readOnly := a.userLogin(t, "ro@example.com", db.RoleReadOnly)

	admins := a.create(t, "+15550001111", "+15551230002", "hello")[0].ID

	code, result, errMsg := a.doAs(t, submitter, http.MethodPost, "/api/v1/messages",
		`{"from": "+15550001111", "to": "+15551230002", "text": "mine"}`)
	if code != http.StatusCreated {
		t.Fatalf("create as a Submitter = %d %q", code, errMsg)
	}

	own := decode[api.CreateMessageResponse](t, result).Items[0].ID

	m, err := a.store.CreateMessage(ctx, db.NewMessage{
		Originator: db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:  db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:     "15550001111", Origin: db.OriginMobile,
		TPDU:        []byte{0x21, 0x00, 0x0b, 0x91, 0x51, 0x55, 0x00, 0x10, 0x11, 0xf1, 0x00, 0x00, 0x01, 0x31},
		SubmittedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.store.CompleteMessage(ctx, m, db.StatusDelivered, testNow, &db.NewStatusReport{
		Originator: db.Address{Digits: "15550000000", TypeOfNumber: 1, NumberingPlan: 1}, TPDU: []byte{0x06},
		CreatedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
	}, 0); err != nil {
		t.Fatal(err)
	}

	reports, err := a.store.StatusReportsFor(ctx, []int64{m})
	if err != nil || len(reports[m]) != 1 {
		t.Fatalf("reports = %+v, %v", reports, err)
	}

	tests := []struct {
		name, token string
		id          int64
		want        int
	}{
		{"Read Only", readOnly, admins, http.StatusForbidden},
		{"Submitter on another user's message", submitter, admins, http.StatusNotFound},
		{"a status report", a.token, reports[m][0].ID, http.StatusNotFound},
		{"an unknown message", a.token, 999, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code, _, _ := a.doAs(t, tt.token, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/content", tt.id),
				""); code != tt.want {
				t.Fatalf("code = %d, want %d", code, tt.want)
			}
		})
	}

	if got := a.auditEvents(t, "read_message_content"); len(got) != 0 {
		t.Fatalf("refused requests logged audit events %q", got)
	}

	if got := a.content(t, submitter, own); got.Text == nil || *got.Text != "mine" {
		t.Fatalf("content of its own message = %+v", got)
	}

	if got := a.auditEvents(t, "read_message_content"); !reflect.DeepEqual(got,
		[]string{fmt.Sprintf("ota@example.com: Content of message %d read", own)}) {
		t.Fatalf("audit events = %q", got)
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
			ResultCode: &code, VendorID: &vendor, AbsentUserDiagnostics: db.AbsentUserDiagnostics{MME: "no_paging_response_msc"},
		},
		{
			MessageID: id, StartedAt: testNow.Add(time.Minute), CompletedAt: testNow.Add(time.Minute + 9*ms), Step: db.StepRouting,
			Node: "hss.example.org", Outcome: "absent_user",
			ResultCode: &code, VendorID: &vendor, AbsentUserDiagnostics: db.AbsentUserDiagnostics{SMSF3GPP: "ms_purged_non_gprs"},
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
			ResultCode: &code, VendorID: &vendor, AbsentUserDiagnostics: &api.AbsentUserDiagnostics{MME: "no_paging_response_msc"},
		},
		{
			ID: 3, StartedAt: "2026-09-29T10:01:00.000Z", CompletedAt: "2026-09-29T10:01:00.009Z", Step: "routing",
			Node: "hss.example.org", Outcome: "absent_user",
			ResultCode: &code, VendorID: &vendor, AbsentUserDiagnostics: &api.AbsentUserDiagnostics{SMSF3GPP: "ms_purged_non_gprs"},
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

	if !strings.Contains(string(result), `"absent_user_diagnostics":{"mme":"no_paging_response_msc"}`) ||
		!strings.Contains(string(result), `"absent_user_diagnostics":{"smsf_3gpp":"ms_purged_non_gprs"}`) ||
		strings.Count(string(result), `"absent_user_diagnostics"`) != 2 || strings.Count(string(result), `"failure_cause"`) != 1 ||
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

	a.create(t, "EllaSMSC", "+15551230004", "four")

	// A numeric originator with the same characters as the name, which only the network could store.
	if _, err := a.store.CreateMessage(context.Background(), db.NewMessage{
		Originator: db.Address{Digits: "EllaSMSC", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:  db.Address{Digits: "15551230004", TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:     "15551230004", Origin: db.OriginMobile,
		TPDU:        []byte{0x01, 0x00, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0x00, 0xf4, 0x00, 0x00, 0x02, 0xe8, 0x34},
		SubmittedAt: testNow, ExpiresAt: testNow.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.store.CompleteMessage(context.Background(), 3, db.StatusDelivered, testNow, nil, 0); err != nil {
		t.Fatal(err)
	}

	tests := map[string]struct {
		query string
		ids   []int64
		total int
	}{
		"newest first":          {"", []int64{5, 4, 3, 2, 1}, 5},
		"by recipient":          {"?to=%2B15551230002", []int64{3, 1}, 2},
		"by originator":         {"?from=%2B15551230003", []int64{3}, 1},
		"by name":               {"?from=EllaSMSC", []int64{4}, 1},
		"by name in any case":   {"?from=ellasmsc", []int64{4}, 1},
		"not by part of a name": {"?from=Ella", []int64{}, 0},
		"by part of recipient":  {"?to=123000", []int64{5, 4, 3, 2, 1}, 5},
		"by recipient prefix":   {"?to=%2B1555123000", []int64{5, 4, 3, 2, 1}, 5},
		"by end of recipient":   {"?to=0002", []int64{3, 1}, 2},
		"by part of originator": {"?from=0001111", []int64{2, 1}, 2},
		"by status":             {"?to=%2B15551230002&status=pending", []int64{1}, 1},
		"paged":                 {"?page=2&per_page=2", []int64{3, 2}, 5},
		"no match":              {"?to=%2B19999999999", []int64{}, 0},
		"delivered only":        {"?status=delivered", []int64{3}, 1},
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

func TestListMessagesByTime(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	for i := range 3 {
		at := testNow.Add(time.Duration(i) * time.Hour)
		if _, err := a.store.CreateMessage(context.Background(), db.NewMessage{
			Originator: db.Address{Digits: "15550001111", TypeOfNumber: 1, NumberingPlan: 1},
			Recipient:  db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1},
			MSISDN:     "15551230002", Origin: db.OriginAPI,
			TPDU:        []byte{0x01, 0x00, 0x0b, 0x91, 0x51, 0x55, 0x21, 0x03, 0x00, 0xf2, 0x00, 0x00, 0x02, 0xe8, 0x34},
			SubmittedAt: at, ExpiresAt: at.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	tests := map[string]struct {
		query string
		ids   []int64
	}{
		"start":               {"?start=2026-09-29T11:00:00Z", []int64{3, 2}},
		"end":                 {"?end=2026-09-29T11:00:00Z", []int64{1}},
		"both":                {"?start=2026-09-29T10:00:00Z&end=2026-09-29T12:00:00Z", []int64{2, 1}},
		"another time zone":   {"?start=2026-09-29T13:00:00%2B02:00", []int64{3, 2}},
		"fractional seconds":  {"?start=2026-09-29T10:00:00.001Z", []int64{3, 2}},
		"equal start and end": {"?start=2026-09-29T11:00:00Z&end=2026-09-29T11:00:00Z", []int64{}},
		"with another filter": {"?start=2026-09-29T11:00:00Z&status=pending&to=%2B15551230002", []int64{3, 2}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			code, result, errMsg := a.do(t, http.MethodGet, "/api/v1/messages"+tc.query, "")
			if code != http.StatusOK {
				t.Fatalf("code = %d %q", code, errMsg)
			}

			ids := []int64{}
			for _, m := range decode[api.ListMessagesResponse](t, result).Items {
				ids = append(ids, m.ID)
			}

			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("ids = %v, want %v", ids, tc.ids)
			}
		})
	}
}

func TestListMessagesRejectsInvalidFilters(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	for _, query := range []string{
		"?page=0", "?page=abc", "?per_page=0", "?per_page=x", "?per_page=101", "?to=%2B", "?to=1555-123", "?to=1234567890123456", "?from=%2B", "?from=Ella_Net", "?status=lost",
		"?start=yesterday", "?end=2026-09-29", "?end=9999-12-31T00:00:00Z", "?start=1000-01-01T00:00:00Z", "?start=2026-09-29T11:00:00Z&end=2026-09-29T10:00:00Z",
	} {
		if code, _, errMsg := a.do(t, http.MethodGet, "/api/v1/messages"+query, ""); code != http.StatusBadRequest || errMsg == "" {
			t.Fatalf("%s: code = %d, error = %q", query, code, errMsg)
		}
	}
}

func TestGetDiameterStatus(t *testing.T) {
	since := time.Date(2026, 9, 29, 9, 59, 0, 0, time.UTC)

	a := newTestAPI(t, fakeDiameter{
		routes: []api.Route{
			{Realm: "epc.mnc001.mcc001.3gppnetwork.org", ApplicationID: s6c.ApplicationID, Peers: []string{"mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org"}},
			{Realm: "example.net", ApplicationID: s6c.ApplicationID},
			{ApplicationID: sgd.ApplicationID, Default: true, Peers: []string{"dea1.ipx.example.net"}},
		},
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
		Host:  "smsc.example.org",
		Realm: "example.org",
		Routes: []api.DiameterRoute{
			{Realm: "epc.mnc001.mcc001.3gppnetwork.org", Application: "s6c", Peers: []string{"mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org"}},
			{Realm: "example.net", Application: "s6c", Peers: []string{}},
			{Default: true, Application: "sgd", Peers: []string{"dea1.ipx.example.net"}},
		},
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
	if !strings.Contains(string(result), `"peers":[]`) || !strings.Contains(string(result), `"routes":[]`) {
		t.Fatalf("result = %s", result)
	}
}
