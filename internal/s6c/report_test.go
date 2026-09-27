package s6c

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

func userIdentifier(t *testing.T, msisdn string) diameter.AVP {
	t.Helper()

	return diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, msisdn)))
}

func reportAnswer(extra ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		CommandCode:   CommandReportSMDeliveryStatus,
		ApplicationID: ApplicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, "smsc.example.org;1;1"),
			diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
		}, extra...),
	}
}

func grouped(t *testing.T, a diameter.AVP) []diameter.AVP {
	t.Helper()

	inner, err := a.Grouped()
	if err != nil {
		t.Fatal(err)
	}

	return inner
}

func TestReportSMDeliveryStatusRequest(t *testing.T) {
	f := &fakeRequester{answer: reportAnswer()}
	diagnostic := uint32(2)

	_, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{
		MSISDN:        "15551230002",
		SingleAttempt: true,
		MME:           &DeliveryOutcome{Cause: DeliveryCauseAbsentUser, AbsentDiagnostic: &diagnostic},
		SMSF3GPP:      &DeliveryOutcome{Cause: DeliveryCauseMemoryCapacityExceeded},
	})
	if err != nil {
		t.Fatalf("ReportSMDeliveryStatus: %v", err)
	}

	req := f.req
	if f.peer != "hss.example.org" || req.CommandCode != CommandReportSMDeliveryStatus || req.ApplicationID != ApplicationID ||
		req.Flags != diameter.FlagRequest|diameter.FlagProxiable || req.AVPs[0].Code != diameter.AVPSessionID {
		t.Fatalf("request = %+v to %q", req, f.peer)
	}

	ui, ok := req.Find(tgpp.AVPUserIdentifier, tgpp.VendorID)
	if !ok || ui.Flags&diameter.AVPFlagMandatory == 0 {
		t.Fatalf("User-Identifier = %+v", ui)
	}

	if msisdn, ok := diameter.Find(grouped(t, ui), tgpp.AVPMSISDN, tgpp.VendorID); !ok || !bytes.Equal(msisdn.Data, mustHex(t, "5155210300f2")) {
		t.Fatalf("MSISDN = %+v", msisdn)
	}

	if sc, ok := req.Find(tgpp.AVPSCAddress, tgpp.VendorID); !ok || !bytes.Equal(sc.Data, mustHex(t, "5155000000f0")) {
		t.Fatalf("SC-Address = %+v", sc)
	}

	if flags, ok := req.Find(avpRDRFlags, tgpp.VendorID); !ok || unsigned(t, flags) != rdrFlagSingleAttempt || flags.Flags&diameter.AVPFlagMandatory != 0 {
		t.Fatalf("RDR-Flags = %+v", flags)
	}

	outcome, ok := req.Find(avpSMDeliveryOutcome, tgpp.VendorID)
	if !ok || outcome.Flags&diameter.AVPFlagMandatory == 0 {
		t.Fatalf("SM-Delivery-Outcome = %+v", outcome)
	}

	outcomes := grouped(t, outcome)
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v", outcomes)
	}

	mme, ok := diameter.Find(outcomes, avpMMESMDeliveryOutcome, tgpp.VendorID)
	if !ok || mme.Flags&diameter.AVPFlagMandatory == 0 {
		t.Fatalf("MME-SM-Delivery-Outcome = %+v", mme)
	}

	mmeInner := grouped(t, mme)
	cause, _ := diameter.Find(mmeInner, avpSMDeliveryCause, tgpp.VendorID)
	diag, _ := diameter.Find(mmeInner, avpAbsentUserDiagnosticSM, tgpp.VendorID)

	if unsigned(t, cause) != DeliveryCauseAbsentUser || unsigned(t, diag) != 2 {
		t.Fatalf("MME outcome = %+v", mmeInner)
	}

	smsf, ok := diameter.Find(outcomes, avpSMSF3GPPSMDeliveryOutcome, tgpp.VendorID)
	if !ok || smsf.Flags&diameter.AVPFlagMandatory != 0 {
		t.Fatalf("SMSF-3GPP-SM-Delivery-Outcome = %+v", smsf)
	}

	if _, ok := diameter.Find(grouped(t, smsf), avpAbsentUserDiagnosticSM, tgpp.VendorID); ok {
		t.Fatal("Absent-User-Diagnostic-SM sent without a diagnostic")
	}
}

func TestReportSMDeliveryStatusWithoutSingleAttempt(t *testing.T) {
	f := &fakeRequester{answer: reportAnswer()}

	_, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{
		MSISDN: "15551230002",
		SGSN:   &DeliveryOutcome{Cause: DeliveryCauseSuccessfulTransfer},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := f.req.Find(avpRDRFlags, tgpp.VendorID); ok {
		t.Fatal("RDR-Flags sent for a normal message")
	}

	outcome, _ := f.req.Find(avpSMDeliveryOutcome, tgpp.VendorID)
	if _, ok := diameter.Find(grouped(t, outcome), avpSGSNSMDeliveryOutcome, tgpp.VendorID); !ok {
		t.Fatal("SGSN-SM-Delivery-Outcome missing")
	}
}

func TestReportSMDeliveryStatusAnswer(t *testing.T) {
	f := &fakeRequester{answer: reportAnswer(userIdentifier(t, "5155999900f0"), mmeServingNode(t))}

	res, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{
		MSISDN: "15551230002",
		MME:    &DeliveryOutcome{Cause: DeliveryCauseAbsentUser},
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.AlertMSISDN != "15559999000" || res.Serving == nil || res.Serving.MME == nil || res.Serving.MME.Name != "mme.example.org" {
		t.Fatalf("result = %+v", res)
	}
}

func TestReportSMDeliveryStatusFailures(t *testing.T) {
	outcome := &DeliveryOutcome{Cause: DeliveryCauseAbsentUser}

	if _, err := newRouter(&fakeRequester{}).ReportSMDeliveryStatus(context.Background(), DeliveryReport{MSISDN: "15551230002"}); err == nil {
		t.Fatal("report without an outcome accepted")
	}

	if _, err := newRouter(&fakeRequester{}).ReportSMDeliveryStatus(context.Background(), DeliveryReport{MSISDN: "+1", MME: outcome}); err == nil {
		t.Fatal("invalid MSISDN accepted")
	}

	f := &fakeRequester{answer: experimentalAnswer(tgpp.ResultErrorMWDListFull)}
	if _, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{MSISDN: "15551230002", MME: outcome}); !IsExperimental(err, tgpp.ResultErrorMWDListFull) {
		t.Fatalf("err = %v", err)
	}

	f = &fakeRequester{err: diameter.ErrNotConnected}
	if _, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{MSISDN: "15551230002", MME: outcome}); !errors.Is(err, diameter.ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}

	f = &fakeRequester{answer: reportAnswer(diameter.OctetString(tgpp.AVPUserIdentifier, 0, tgpp.VendorID, []byte{0x01}))}
	if _, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{MSISDN: "15551230002", MME: outcome}); !errors.Is(err, ErrMalformedAnswer) {
		t.Fatalf("err = %v", err)
	}
}

func TestSendRoutingInfoForSMAlertMSISDN(t *testing.T) {
	f := &fakeRequester{answer: successAnswer(mmeServingNode(t), userIdentifier(t, "5155999900f0"))}

	routing, err := newRouter(f).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})
	if err != nil || routing.AlertMSISDN != "15559999000" {
		t.Fatalf("routing = %+v, %v", routing, err)
	}

	f = &fakeRequester{answer: experimentalAnswer(tgpp.ResultErrorAbsentUser, userIdentifier(t, "5155999900f0"))}

	_, err = newRouter(f).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})

	var re *ResultError
	if !errors.As(err, &re) || re.AlertMSISDN != "15559999000" {
		t.Fatalf("err = %v", err)
	}
}

func TestReportSMDeliveryStatusFailedNodes(t *testing.T) {
	f := &fakeRequester{answer: reportAnswer()}

	_, err := newRouter(f).ReportSMDeliveryStatus(context.Background(), DeliveryReport{
		MSISDN: "15551230002",
		MME:    &DeliveryOutcome{Cause: DeliveryCauseAbsentUser},
		Failed: ServingNodes{
			Serving:    &ServingNode{MME: &Node{Name: "mme.example.org", Realm: "example.org", Number: "15550000010"}},
			Additional: &ServingNode{SGSN: &Node{Number: "15550000020"}, MSCNumber: "15550000040"},
			SMSF3GPP:   &Node{Name: "smsf.example.org", Realm: "example.org", Number: "15550000030"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	serving, ok := f.req.Find(avpServingNode, tgpp.VendorID)
	if !ok || serving.Flags&diameter.AVPFlagMandatory == 0 {
		t.Fatalf("Serving-Node = %+v", serving)
	}

	inner := grouped(t, serving)
	name, _ := diameter.Find(inner, avpMMEName, tgpp.VendorID)
	realm, _ := diameter.Find(inner, avpMMERealm, tgpp.VendorID)
	number, _ := diameter.Find(inner, tgpp.AVPMMENumberForMTSMS, tgpp.VendorID)

	if name.String() != "mme.example.org" || realm.String() != "example.org" || !bytes.Equal(number.Data, mustHex(t, "5155000010f0")) ||
		name.Flags&diameter.AVPFlagMandatory == 0 || realm.Flags&diameter.AVPFlagMandatory != 0 || number.Flags&diameter.AVPFlagMandatory != 0 {
		t.Fatalf("Serving-Node content = %+v", inner)
	}

	additional, ok := f.req.Find(avpAdditionalServingNode, tgpp.VendorID)
	if !ok {
		t.Fatal("Additional-Serving-Node missing")
	}

	inner = grouped(t, additional)
	sgsn, _ := diameter.Find(inner, tgpp.AVPSGSNNumber, tgpp.VendorID)
	msc, _ := diameter.Find(inner, avpMSCNumber, tgpp.VendorID)

	if !bytes.Equal(sgsn.Data, mustHex(t, "5155000020f0")) || !bytes.Equal(msc.Data, mustHex(t, "5155000040f0")) || len(inner) != 2 {
		t.Fatalf("Additional-Serving-Node content = %+v", inner)
	}

	smsf, ok := f.req.Find(avpSMSF3GPPAddress, tgpp.VendorID)
	if !ok || smsf.Flags&diameter.AVPFlagMandatory != 0 || len(grouped(t, smsf)) != 3 {
		t.Fatalf("SMSF-3GPP-Address = %+v", smsf)
	}

	if _, ok := f.req.Find(avpSMSFNon3GPPAddress, tgpp.VendorID); ok {
		t.Fatal("SMSF-Non-3GPP-Address sent without a failed node")
	}

	parsed, err := servingNodes(f.req)
	if err != nil || parsed.Serving.MME.Name != "mme.example.org" || parsed.SMSF3GPP.Number != "15550000030" {
		t.Fatalf("round trip = %+v, %v", parsed, err)
	}
}

func TestReportSMDeliveryStatusInvalidFailedNode(t *testing.T) {
	_, err := newRouter(&fakeRequester{answer: reportAnswer()}).ReportSMDeliveryStatus(context.Background(), DeliveryReport{
		MSISDN: "15551230002",
		MME:    &DeliveryOutcome{Cause: DeliveryCauseAbsentUser},
		Failed: ServingNodes{Serving: &ServingNode{MME: &Node{Name: "mme.example.org", Number: "+1"}}},
	})
	if err == nil {
		t.Fatal("invalid serving node number accepted")
	}
}
