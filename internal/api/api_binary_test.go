package api_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
)

// simDataDownload is a (U)SIM data download message (TS 23.040 clause 9.2.3.9): class 2 8-bit data, whose user data
// header has a (U)SIM Toolkit Security Header IEI (clause 9.2.3.24.7). The user data is placeholder octets.
const simDataDownload = `{"from": "+15550001111", "to": "+15551230002", "binary": {"protocol_identifier": 127, ` +
	`"data_coding_scheme": 246, "user_data_header": "027000", "user_data": "0011223344"}}`

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func (a *testAPI) createBinary(t *testing.T, body string) []api.Message {
	t.Helper()

	code, result, errMsg := a.do(t, http.MethodPost, "/api/v1/messages", body)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %q", code, errMsg)
	}

	return decode[api.CreateMessageResponse](t, result).Items
}

func TestCreateBinaryMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	got := a.createBinary(t, simDataDownload)

	want := []api.Message{{
		ID:            1,
		From:          "+15550001111",
		To:            "+15551230002",
		Encoding:      "binary",
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

	wantTPDU := mustHex(t, "41 00 0b91 5155210300f2 7f f6 08 027000 0011223344")
	if !bytes.Equal(m.TPDU, wantTPDU) || m.ProtocolIdentifier != 0x7f || m.Origin != db.OriginAPI {
		t.Fatalf("stored = %+v (TPDU %x), want TPDU %x", m, m.TPDU, wantTPDU)
	}

	wantContent := api.MessageContent{Binary: &api.BinaryContent{
		ProtocolIdentifier: 0x7f, DataCodingScheme: 0xf6,
		UserDataHeader: mustHex(t, "027000"), UserData: mustHex(t, "0011223344"),
	}}
	if got := a.content(t, a.token, 1); !reflect.DeepEqual(got, wantContent) {
		t.Fatalf("content = %+v, want %+v", got.Binary, wantContent.Binary)
	}
}

func TestCreateBinaryMessageVariants(t *testing.T) {
	tests := map[string]struct {
		binary   string
		tpdu     string
		encoding string
	}{
		"no header, default protocol identifier": {
			`{"data_coding_scheme": 4, "user_data": "cafe"}`, "01 00 0b91 5155210300f2 00 04 02 cafe", "binary",
		},
		"null header": {
			`{"data_coding_scheme": 4, "user_data_header": null, "user_data": "cafe"}`, "01 00 0b91 5155210300f2 00 04 02 cafe", "binary",
		},
		"empty user data": {
			`{"protocol_identifier": 127, "data_coding_scheme": 22, "user_data": ""}`, "01 00 0b91 5155210300f2 7f 16 00", "binary",
		},
		"140 octets": {
			`{"data_coding_scheme": 4, "user_data_header": "027000", "user_data": "` + strings.Repeat("ab", 137) + `"}`,
			"41 00 0b91 5155210300f2 00 04 8c 027000" + strings.Repeat("ab", 137), "binary",
		},
		"device trigger with an application port": {
			`{"protocol_identifier": 72, "data_coding_scheme": 4, "user_data_header": "0605040b8423f0", "user_data": "00"}`,
			"41 00 0b91 5155210300f2 48 04 08 0605040b8423f0 00", "binary",
		},
		"class 2 in group 01xx for (U)SIM data download": {
			`{"protocol_identifier": 127, "data_coding_scheme": 86, "user_data": "00"}`, "01 00 0b91 5155210300f2 7f 56 01 00", "binary",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			a := newTestAPI(t, fakeDiameter{})

			got := a.createBinary(t, `{"from": "+15550001111", "to": "+15551230002", "binary": `+tt.binary+`}`)
			if len(got) != 1 || got[0].Encoding != tt.encoding {
				t.Fatalf("items = %+v", got)
			}

			m, err := a.store.GetMessage(context.Background(), got[0].ID)
			if err != nil {
				t.Fatal(err)
			}

			if want := mustHex(t, tt.tpdu); !bytes.Equal(m.TPDU, want) {
				t.Fatalf("TPDU = %x, want %x", m.TPDU, want)
			}
		})
	}
}

func TestCreateBinaryMessageRejectsInvalidInput(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	const (
		class2  = "binary.data_coding_scheme must be message class 2 for this protocol_identifier"
		notText = "binary.data_coding_scheme must not code GSM 7-bit or UCS2; send text instead"
		noPort  = "binary.user_data_header must have an application port for this protocol_identifier"
	)

	tests := map[string]struct {
		body, err string
	}{
		"text and binary": {
			`{"from": "+15550001111", "to": "+15551230002", "text": "hi", "binary": {"data_coding_scheme": 4, "user_data": "00"}}`,
			"text and binary are mutually exclusive",
		},
		"neither":                       {`{"from": "+15550001111", "to": "+15551230002", "binary": null}`, "text or binary is required"},
		"missing data coding scheme":    {`{"user_data": "00"}`, "binary.data_coding_scheme is required"},
		"null user data":                {`{"data_coding_scheme": 4, "user_data": null}`, "binary.user_data is required"},
		"missing user data":             {`{"data_coding_scheme": 4}`, "binary.user_data is required"},
		"GSM 7-bit":                     {`{"data_coding_scheme": 0, "user_data": "00"}`, notText},
		"GSM 7-bit class 2":             {`{"protocol_identifier": 127, "data_coding_scheme": 242, "user_data": "00"}`, notText},
		"reserved coding group":         {`{"data_coding_scheme": 132, "user_data": "00"}`, notText},
		"UCS2":                          {`{"data_coding_scheme": 8, "user_data": "00410042"}`, notText},
		"UCS2 class 2":                  {`{"data_coding_scheme": 26, "user_data": "0041"}`, notText},
		"device trigger without header": {`{"protocol_identifier": 72, "data_coding_scheme": 4, "user_data": "00"}`, noPort},
		"device trigger without port":   {`{"protocol_identifier": 72, "data_coding_scheme": 4, "user_data_header": "027000", "user_data": "00"}`, noPort},
		"telematic interworking":        {`{"protocol_identifier": 33, "data_coding_scheme": 4, "user_data": "00"}`, "binary.protocol_identifier requests telematic interworking, which is not supported"},
		"SIM data download no class":    {`{"protocol_identifier": 127, "data_coding_scheme": 4, "user_data": "00"}`, class2},
		"SIM data download class 1":     {`{"protocol_identifier": 127, "data_coding_scheme": 245, "user_data": "00"}`, class2},
		"ANSI-136 R-DATA not class 2":   {`{"protocol_identifier": 124, "data_coding_scheme": 4, "user_data": "00"}`, class2},
		"empty header":                  {`{"data_coding_scheme": 4, "user_data_header": "", "user_data": "00"}`, "binary.user_data_header is malformed"},
		"header length mismatch":        {`{"data_coding_scheme": 4, "user_data_header": "037000", "user_data": "00"}`, "binary.user_data_header is malformed"},
		"truncated information element": {`{"data_coding_scheme": 4, "user_data_header": "027001", "user_data": "00"}`, "binary.user_data_header is malformed"},
		"over 140 octets":               {`{"data_coding_scheme": 4, "user_data_header": "027000", "user_data": "` + strings.Repeat("ab", 138) + `"}`, "binary user data exceeds 140 octets"},
		"invalid hex":                   {`{"data_coding_scheme": 4, "user_data": "zz"}`, "Invalid request data"},
		"odd hex":                       {`{"data_coding_scheme": 4, "user_data": "abc"}`, "Invalid request data"},
		"protocol identifier over 255":  {`{"protocol_identifier": 256, "data_coding_scheme": 4, "user_data": "00"}`, "Invalid request data"},
		"negative data coding scheme":   {`{"data_coding_scheme": -1, "user_data": "00"}`, "Invalid request data"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			body := tc.body
			if !strings.Contains(body, `"from"`) {
				body = `{"from": "+15550001111", "to": "+15551230002", "binary": ` + body + `}`
			}

			if code, _, errMsg := a.do(t, http.MethodPost, "/api/v1/messages", body); code != http.StatusBadRequest || errMsg != tc.err {
				t.Fatalf("code = %d, error = %q; want %q", code, errMsg, tc.err)
			}
		})
	}

	if _, total, _ := a.store.ListMessages(context.Background(), db.MessageFilter{}, 1, 10); total != 0 || a.notified.Load() != 0 {
		t.Fatalf("stored %d messages after rejected requests", total)
	}
}

func TestCreateBinaryReplaceTypeMessage(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	const replaceType1 = `{"from": "+15551230001", "to": "+15551230002", "binary": {"protocol_identifier": 65, ` +
		`"data_coding_scheme": 4, "user_data": "0%d"}}`

	first := a.createBinary(t, fmt.Sprintf(replaceType1, 1))[0].ID
	second := a.createBinary(t, fmt.Sprintf(replaceType1, 2))[0].ID

	if m, err := a.store.GetMessage(ctx, first); err != nil || m.Status != db.StatusReplaced {
		t.Fatalf("first = %+v, %v; want replaced", m, err)
	}

	if m, err := a.store.GetMessage(ctx, second); err != nil || m.Status != db.StatusPending || m.ProtocolIdentifier != 0x41 {
		t.Fatalf("second = %+v, %v; want pending", m, err)
	}

	// The API replaces only the messages of the same user: not one from a phone with the same originator and replace
	// type, whose originator the network vouches for.
	phone := createMobileMessage(t, a, true, 0x41, true)
	a.createBinary(t, fmt.Sprintf(replaceType1, 3))

	if m, err := a.store.GetMessage(ctx, phone.ID); err != nil || m.Status != db.StatusPending {
		t.Fatalf("phone message = %+v, %v; want pending", m, err)
	}

	if m, err := a.store.GetMessage(ctx, second); err != nil || m.Status != db.StatusReplaced {
		t.Fatalf("second = %+v, %v; want replaced", m, err)
	}
}

func TestGetMessageContentDeliverReports(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})
	ctx := context.Background()

	id := a.createBinary(t, simDataDownload)[0].ID

	code, vendor := uint32(5555), uint32(10415)

	for _, at := range []db.DeliveryAttempt{
		{MessageID: id, StartedAt: testNow, CompletedAt: testNow, Step: db.StepRouting, Outcome: "success"},
		{
			MessageID: id, StartedAt: testNow, CompletedAt: testNow, Step: db.StepDelivery, NodeType: db.NodeTypeMME,
			Outcome: "sm_delivery_failure", ResultCode: &code, VendorID: &vendor, TPFailureCause: "usim_data_download_error",
			// SMS-DELIVER-REPORT for RP-ERROR: TP-FCS D5, TP-PI with PID, DCS and UDL.
			DeliverReport: mustHex(t, "00 d5 07 7f f6 02 cafe"),
		},
		{
			MessageID: id, StartedAt: testNow, CompletedAt: testNow, Step: db.StepDelivery, NodeType: db.NodeTypeMME,
			Outcome: "success",
			// SMS-DELIVER-REPORT for RP-ACK with TP-UDHI.
			DeliverReport: mustHex(t, "40 07 7f f6 05 027100 aabb"),
		},
		{
			MessageID: id, StartedAt: testNow, CompletedAt: testNow, Step: db.StepDelivery, NodeType: db.NodeTypeMME,
			Outcome: "success", DeliverReport: mustHex(t, "01"),
		},
		{
			MessageID: id, StartedAt: testNow, CompletedAt: testNow, Step: db.StepDelivery, NodeType: db.NodeTypeMME,
			Outcome: "success", DeliverReport: mustHex(t, "00 00"),
		},
	} {
		if _, err := a.store.CreateDeliveryAttempt(ctx, at); err != nil {
			t.Fatal(err)
		}
	}

	pid, dcs := uint8(0x7f), uint8(0xf6)

	want := []api.DeliverReport{
		{AttemptID: 2, TPDU: mustHex(t, "00d5077ff602cafe"), ProtocolIdentifier: &pid, DataCodingScheme: &dcs, UserData: mustHex(t, "cafe")},
		{
			AttemptID: 3, TPDU: mustHex(t, "40077ff605027100aabb"), ProtocolIdentifier: &pid, DataCodingScheme: &dcs,
			UserDataHeader: mustHex(t, "027100"), UserData: mustHex(t, "aabb"),
		},
		{AttemptID: 4, TPDU: mustHex(t, "01")},
		{AttemptID: 5, TPDU: mustHex(t, "0000")},
	}

	if got := a.content(t, a.token, id).DeliverReports; !reflect.DeepEqual(got, want) {
		t.Fatalf("deliver reports = %+v, want %+v", got, want)
	}

	_, result, _ := a.do(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/content", id), "")
	if !strings.Contains(string(result), `{"attempt_id":3,"tpdu":"40077ff605027100aabb","protocol_identifier":127,`+
		`"data_coding_scheme":246,"user_data_header":"027100","user_data":"aabb"}`) {
		t.Fatalf("content = %s", result)
	}

	// The attempts leave the deliver reports out: they are content.
	_, result, _ = a.do(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/attempts", id), "")
	if strings.Contains(string(result), "cafe") || strings.Contains(string(result), "deliver_report") {
		t.Fatalf("attempts carry the deliver reports: %s", result)
	}
}

func TestCreateMessageValidity(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	body := strings.Replace(simDataDownload, `"to": "+15551230002",`, `"to": "+15551230002", "validity_seconds": 3600,`, 1)
	if got := a.createBinary(t, body); got[0].ExpiresAt != "2026-09-29T11:00:00.000Z" {
		t.Fatalf("expires_at = %s", got[0].ExpiresAt)
	}

	// 10 years is the most (SRV-L8); a longer one overflowed the expiry beyond 2262.
	body = strings.Replace(simDataDownload, `"to": "+15551230002",`, `"to": "+15551230002", "validity_seconds": 315360000,`, 1)
	if got := a.createBinary(t, body); got[0].ExpiresAt != "2036-09-26T10:00:00.000Z" {
		t.Fatalf("expires_at of 10 years = %s", got[0].ExpiresAt)
	}

	for _, v := range []string{"0", "-1", "315360001", "9223372036"} {
		body := strings.Replace(simDataDownload, `"to": "+15551230002",`, `"to": "+15551230002", "validity_seconds": `+v+`,`, 1)
		if code, _, errMsg := a.do(t, http.MethodPost, "/api/v1/messages", body); code != http.StatusBadRequest ||
			errMsg != "validity_seconds must be between 1 and 315360000" {
			t.Fatalf("validity_seconds %s = %d %q", v, code, errMsg)
		}
	}
}
