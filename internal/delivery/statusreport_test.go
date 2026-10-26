package delivery

import (
	"bytes"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

// reportingSubmitTPDU is submitTPDU with TP-SRR set.
const reportingSubmitTPDU = "31" + "07" + "0b91" + "5155210300f2" + "00" + "00" + "aa" + "05" + "e8329bfd06"

func reportingMessage(t *testing.T) db.Message {
	t.Helper()

	m := pendingMessage(t)
	m.TPDU = mustHex(t, reportingSubmitTPDU)

	return m
}

func TestStatusReportOnFinalOutcomes(t *testing.T) {
	mscOnly := s6c.Routing{IMSI: "001010000000002", ServingNodes: s6c.ServingNodes{Serving: &s6c.ServingNode{MSCNumber: "15550000040"}}}
	routingError := func(code uint32) error { return &s6c.ResultError{Result: tgpp.Experimental(code)} }

	tests := map[string]struct {
		routing    *s6c.Routing
		routingErr error
		answer     *diameter.Message
		sendErr    error
		expired    bool
		singleShot bool
		wantStatus db.MessageStatus
		want       tpdu.Status
	}{
		"delivered":                       {answer: success(), wantStatus: db.StatusDelivered, want: 0x00},
		"expired":                         {expired: true, wantStatus: db.StatusExpired, want: 0x46},
		"unknown to the HSS":              {routingErr: routingError(tgpp.ResultErrorUserUnknown), wantStatus: db.StatusFailed, want: 0x43},
		"barred, single attempt":          {routingErr: routingError(tgpp.ResultErrorServiceBarred), singleShot: true, wantStatus: db.StatusFailed, want: 0x63},
		"not subscribed":                  {routingErr: routingError(tgpp.ResultErrorServiceNotSubscribed), wantStatus: db.StatusFailed, want: 0x42},
		"no SMS in the VPLMN (HSS)":       {routingErr: routingError(tgpp.ResultErrorFacilityNotSupported), singleShot: true, wantStatus: db.StatusFailed, want: 0x63},
		"absent (HSS), single attempt":    {routingErr: routingError(tgpp.ResultErrorAbsentUser), singleShot: true, wantStatus: db.StatusFailed, want: 0x62},
		"HSS unreachable, single attempt": {routingErr: diameter.ErrNotConnected, singleShot: true, wantStatus: db.StatusFailed, want: 0x60},
		"no node over SGd":                {routing: &mscOnly, wantStatus: db.StatusFailed, want: 0x45},
		"absent, single attempt":          {answer: experimental(tgpp.ResultErrorAbsentUser), singleShot: true, wantStatus: db.StatusFailed, want: 0x62},
		"busy, single attempt":            {answer: experimental(tgpp.ResultErrorUserBusyForMTSMS), singleShot: true, wantStatus: db.StatusFailed, want: 0x61},
		"illegal user":                    {answer: experimental(tgpp.ResultErrorIllegalUser), wantStatus: db.StatusFailed, want: 0x42},
		"illegal equipment":               {answer: experimental(tgpp.ResultErrorIllegalEquipment), wantStatus: db.StatusFailed, want: 0x42},
		"not SM equipped, single attempt": {answer: smDeliveryFailure(sgd.CauseEquipmentNotSMEquipped), singleShot: true, wantStatus: db.StatusFailed, want: 0x63},
		"memory exceeded, single attempt": {answer: smDeliveryFailure(sgd.CauseMemoryCapacityExceeded), singleShot: true, wantStatus: db.StatusFailed, want: 0x65},
		"message refused, single attempt": {answer: protocolError(mustHex(t, "00"+"91"+"00")), singleShot: true, wantStatus: db.StatusFailed, want: 0x63},
		"storage full, single attempt":    {answer: protocolError(mustHex(t, "00"+"d0"+"00")), singleShot: true, wantStatus: db.StatusFailed, want: 0x65},
		"facility not supported (MME)":    {answer: experimental(tgpp.ResultErrorFacilityNotSupported), singleShot: true, wantStatus: db.StatusFailed, want: 0x63},
		"transport error, single attempt": {sendErr: diameter.ErrNotConnected, singleShot: true, wantStatus: db.StatusFailed, want: 0x60},
		"base permanent error":            {answer: &diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, 5004)}}, wantStatus: db.StatusFailed, want: 0x60},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m := reportingMessage(t)
			m.SingleShot = tt.singleShot

			if tt.expired {
				m.ExpiresAt = testNow
			}

			router := &fakeRouter{routing: mmeRouting(), err: tt.routingErr}
			if tt.routing != nil {
				router.routing = *tt.routing
			}

			store := newFakeStore(m)
			sender := &fakeSender{
				answers: map[string]*diameter.Message{"mme.example.org": tt.answer},
				errs:    map[string]error{"mme.example.org": tt.sendErr},
			}

			process(t, newDeliverer(store, router, sender))

			if got := store.messages[1].Status; got != tt.wantStatus {
				t.Fatalf("status = %s, want %s", got, tt.wantStatus)
			}

			report := store.reports[1]
			if report == nil {
				t.Fatal("no status report")
			}

			if report.Status != uint8(tt.want) || report.Originator.Digits != "15550000000" || !report.CreatedAt.Equal(testNow) ||
				!report.ExpiresAt.Equal(testNow.Add(24*time.Hour)) {
				t.Fatalf("report = %+v, want status %#x", report, byte(tt.want))
			}

			r, err := tpdu.DecodeStatusReport(report.TPDU)
			if err != nil {
				t.Fatalf("DecodeStatusReport: %v", err)
			}

			recipient := tpdu.Address{TypeOfNumber: 1, NumberingPlan: 1, Digits: "15551230002"}
			if r.MessageReference != 7 || r.Recipient != recipient || r.Status != tt.want || r.MoreMessagesToSend ||
				!r.ServiceCentreTimeStamp.Equal(m.SubmittedAt) || !r.DischargeTime.Equal(testNow) {
				t.Fatalf("SMS-STATUS-REPORT = %+v", r)
			}
		})
	}
}

func TestNoStatusReport(t *testing.T) {
	fromAPI := reportingMessage(t)
	fromAPI.Origin = db.OriginAPI

	report := reportingMessage(t)
	report.Kind = db.KindStatusReport
	report.TPDU = mustHex(t, "06"+"07"+"0b91"+"5155210300f2"+"62907211950000"+"62907211950000"+"00")

	tests := map[string]struct {
		message db.Message
		answer  *diameter.Message
	}{
		"not requested":      {pendingMessage(t), success()},
		"from the API":       {fromAPI, success()},
		"on a status report": {report, success()},
		"while still trying": {reportingMessage(t), experimental(tgpp.ResultErrorAbsentUser)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore(tt.message)
			sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": tt.answer}}

			process(t, newDeliverer(store, &fakeRouter{routing: mmeRouting()}, sender))

			if len(sender.requests) != 1 {
				t.Fatalf("TFRs = %d", len(sender.requests))
			}

			if r := store.reports[1]; r != nil {
				t.Fatalf("report = %+v, want none", r)
			}
		})
	}
}

func TestDeliverIndicatesStatusReport(t *testing.T) {
	fromAPI := reportingMessage(t)
	fromAPI.Origin = db.OriginAPI

	tests := map[string]struct {
		message db.Message
		want    bool
	}{
		"requested by an MS":     {reportingMessage(t), true},
		"not requested":          {pendingMessage(t), false},
		"requested from the API": {fromAPI, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}

			process(t, newDeliverer(newFakeStore(tt.message), &fakeRouter{routing: mmeRouting()}, sender))

			deliver := avpData(t, sender.requests[0], sgd.AVPSMRPUI, tgpp.VendorID)
			if got := deliver[0]&0x20 != 0; got != tt.want {
				t.Fatalf("TP-SRI = %v, want %v (SMS-DELIVER %x)", got, tt.want, deliver)
			}
		})
	}
}

func TestDeliverStatusReport(t *testing.T) {
	stored := "06" + "07" + "0b91" + "5155210300f2" + "62907211950000" + "62907212950000" + "00"

	report := pendingMessage(t)
	report.Kind = db.KindStatusReport
	report.ReportFor = 9
	report.Originator = db.Address{Digits: "15550000000", TypeOfNumber: 1, NumberingPlan: 1}
	report.MSISDN = "15551230001"
	report.TPDU = mustHex(t, stored)

	next := pendingMessage(t)
	next.ID = 2
	next.MSISDN = "15551230001"

	for name, tt := range map[string]struct {
		others []db.Message
		want   string
	}{
		"last for the MS":        {nil, stored},
		"more for the MS behind": {[]db.Message{next}, "02" + stored[2:]},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore(append([]db.Message{report}, tt.others...)...)
			router := &fakeRouter{routing: mmeRouting()}
			sender := &fakeSender{answers: map[string]*diameter.Message{"mme.example.org": success()}}
			d := newDeliverer(store, router, sender)

			r, _ := d.deliver(t.Context(), report, testNow, nil)
			if err := d.settle(t.Context(), report, r); err != nil {
				t.Fatal(err)
			}

			// The SC originates the report (TS 29.338 clause 5.2.1.1).
			if req := router.requests[0]; req.MSISDN != "15551230001" || req.MTI != s6c.SMRPMTIStatusReport ||
				!bytes.Equal(req.SMEA, mustHex(t, "0b91"+"5155000000f0")) {
				t.Fatalf("routing request = %+v", req)
			}

			if got := avpData(t, sender.requests[0], sgd.AVPSMRPUI, tgpp.VendorID); !bytes.Equal(got, mustHex(t, tt.want)) {
				t.Fatalf("SM-RP-UI = %x, want %s", got, tt.want)
			}

			if store.messages[1].Status != db.StatusDelivered || store.reports[1] != nil {
				t.Fatalf("status = %s, report = %+v", store.messages[1].Status, store.reports[1])
			}
		})
	}
}

func TestStatusReportTellsTheFirstNodesError(t *testing.T) {
	routing := mmeRouting()
	routing.Additional = &s6c.ServingNode{SGSN: &s6c.NodeAddress{Name: "sgsn.example.org", Realm: "epc.example.org", Number: "15550000020"}}

	tests := map[string]struct {
		sgsn       *diameter.Message
		wantStatus db.MessageStatus
		want       tpdu.Status
	}{
		"temporary errors at both":           {experimental(tgpp.ResultErrorUserBusyForMTSMS), db.StatusFailed, 0x62},
		"permanent error at the second node": {experimental(tgpp.ResultErrorIllegalEquipment), db.StatusFailed, 0x42},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			m := reportingMessage(t)
			m.SingleShot = true

			store := newFakeStore(m)
			sender := &fakeSender{answers: map[string]*diameter.Message{
				"mme.example.org":  experimental(tgpp.ResultErrorAbsentUser),
				"sgsn.example.org": tt.sgsn,
			}}

			process(t, newDeliverer(store, &fakeRouter{routing: routing}, sender))

			if len(sender.requests) != 2 || store.messages[1].Status != tt.wantStatus {
				t.Fatalf("TFRs = %d, status = %s", len(sender.requests), store.messages[1].Status)
			}

			if r := store.reports[1]; r == nil || r.Status != uint8(tt.want) {
				t.Fatalf("report = %+v, want status %#x", r, byte(tt.want))
			}
		})
	}
}
