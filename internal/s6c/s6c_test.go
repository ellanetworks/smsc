package s6c

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

type fakeRequester struct {
	peer   string
	req    *diameter.Message
	answer *diameter.Message
	err    error
}

func (f *fakeRequester) Do(_ context.Context, peerHost string, req *diameter.Message) (*diameter.Message, error) {
	f.peer = peerHost
	f.req = req

	return f.answer, f.err
}

func (f *fakeRequester) NewSessionID() string {
	return "smsc.example.org;1;1"
}

func newRouter(f *fakeRequester) *Router {
	return &Router{
		Node:                 f,
		Identity:             diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org"},
		HSSHost:              "hss.example.org",
		HSSRealm:             "example.org",
		ServiceCentreAddress: "15550000000",
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func unsigned(t *testing.T, a diameter.AVP) uint32 {
	t.Helper()

	v, err := a.Unsigned32()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func successAnswer(extra ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		CommandCode:   CommandSendRoutingInfoForSM,
		ApplicationID: ApplicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, "smsc.example.org;1;1"),
			diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, "001010000000001"),
		}, extra...),
	}
}

func mmeServingNode(t *testing.T) diameter.AVP {
	return diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.UTF8String(avpMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, "mme.example.org"),
		diameter.UTF8String(avpMMERealm, 0, tgpp.VendorID, "example.org"),
		diameter.OctetString(avpMMENumberForMTSMS, 0, tgpp.VendorID, mustHex(t, "5155000010f0")),
	)
}

func experimentalAnswer(code uint32, extra ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		CommandCode:   CommandSendRoutingInfoForSM,
		ApplicationID: ApplicationID,
		AVPs: append([]diameter.AVP{
			diameter.Grouped(diameter.AVPExperimentalResult, diameter.AVPFlagMandatory, 0,
				diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
				diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, code),
			),
		}, extra...),
	}
}

func TestSendRoutingInfoForSMRequest(t *testing.T) {
	f := &fakeRequester{answer: successAnswer(mmeServingNode(t))}

	if _, err := newRouter(f).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002", SingleAttempt: true}); err != nil {
		t.Fatalf("SendRoutingInfoForSM: %v", err)
	}

	req := f.req
	if f.peer != "hss.example.org" || req.CommandCode != CommandSendRoutingInfoForSM || req.ApplicationID != ApplicationID ||
		req.Flags != diameter.FlagRequest|diameter.FlagProxiable {
		t.Fatalf("request header = %+v to %q", req, f.peer)
	}

	if req.AVPs[0].Code != diameter.AVPSessionID || req.AVPs[0].String() != "smsc.example.org;1;1" {
		t.Fatalf("first AVP = %+v", req.AVPs[0])
	}

	checks := map[string]struct {
		code, vendor uint32
		want         []byte
	}{
		"Destination-Host":   {diameter.AVPDestinationHost, 0, []byte("hss.example.org")},
		"Destination-Realm":  {diameter.AVPDestinationRealm, 0, []byte("example.org")},
		"MSISDN":             {tgpp.AVPMSISDN, tgpp.VendorID, mustHex(t, "5155210300f2")},
		"SC-Address":         {tgpp.AVPSCAddress, tgpp.VendorID, mustHex(t, "5155000000f0")},
		"SM-RP-MTI":          {avpSMRPMTI, tgpp.VendorID, mustHex(t, "00000000")},
		"SRR-Flags":          {avpSRRFlags, tgpp.VendorID, mustHex(t, "00000005")},
		"Auth-Session-State": {diameter.AVPAuthSessionState, 0, mustHex(t, "00000001")},
	}

	for name, c := range checks {
		a, ok := req.Find(c.code, c.vendor)
		if !ok || !bytes.Equal(a.Data, c.want) {
			t.Errorf("%s = %x (present %v), want %x", name, a.Data, ok, c.want)
		}
	}

	features, ok := req.Find(tgpp.AVPSupportedFeatures, tgpp.VendorID)
	if !ok {
		t.Fatal("Supported-Features missing")
	}

	inner, err := features.Grouped()
	if err != nil {
		t.Fatal(err)
	}

	listID, _ := diameter.Find(inner, tgpp.AVPFeatureListID, tgpp.VendorID)
	list, _ := diameter.Find(inner, tgpp.AVPFeatureList, tgpp.VendorID)

	if unsigned(t, listID) != 1 || unsigned(t, list) != featureSMSFSupport {
		t.Fatalf("Supported-Features = %+v", inner)
	}
}

func TestSendRoutingInfoForSMWithoutSingleAttempt(t *testing.T) {
	f := &fakeRequester{answer: successAnswer(mmeServingNode(t))}

	if _, err := newRouter(f).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"}); err != nil {
		t.Fatal(err)
	}

	flags, _ := f.req.Find(avpSRRFlags, tgpp.VendorID)
	if unsigned(t, flags) != srrFlagGPRSIndicator {
		t.Fatalf("SRR-Flags = %d", unsigned(t, flags))
	}
}

func TestSendRoutingInfoForSMParsesAllServingNodes(t *testing.T) {
	answer := successAnswer(
		mmeServingNode(t),
		diameter.Grouped(avpAdditionalServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(avpSGSNName, 0, tgpp.VendorID, "sgsn.example.org"),
			diameter.UTF8String(avpSGSNRealm, 0, tgpp.VendorID, "example.org"),
			diameter.OctetString(avpSGSNNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000020f0")),
		),
		diameter.Grouped(avpSMSF3GPPAddress, 0, tgpp.VendorID,
			diameter.OctetString(avpSMSF3GPPNumber, 0, tgpp.VendorID, mustHex(t, "5155000030f0")),
			diameter.UTF8String(avpSMSF3GPPName, 0, tgpp.VendorID, "smsf.example.org"),
			diameter.UTF8String(avpSMSF3GPPRealm, 0, tgpp.VendorID, "example.org"),
		),
		diameter.OctetString(avpLMSI, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "01020304")),
		diameter.Unsigned32(avpMWDStatus, diameter.AVPFlagMandatory, tgpp.VendorID, 2),
	)

	routing, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})
	if err != nil {
		t.Fatalf("SendRoutingInfoForSM: %v", err)
	}

	if routing.IMSI != "001010000000001" || routing.MWDStatus != 2 || !bytes.Equal(routing.LMSI, mustHex(t, "01020304")) {
		t.Fatalf("routing = %+v", routing)
	}

	if routing.Serving == nil || routing.Serving.SGSN != nil ||
		*routing.Serving.MME != (Node{Name: "mme.example.org", Realm: "example.org", Number: "15550000010"}) {
		t.Fatalf("Serving = %+v", routing.Serving)
	}

	if routing.Additional == nil || routing.Additional.MME != nil ||
		*routing.Additional.SGSN != (Node{Name: "sgsn.example.org", Realm: "example.org", Number: "15550000020"}) {
		t.Fatalf("Additional = %+v", routing.Additional)
	}

	if *routing.SMSF3GPP != (Node{Name: "smsf.example.org", Realm: "example.org", Number: "15550000030"}) {
		t.Fatalf("SMSF = %+v", routing.SMSF3GPP)
	}

	if routing.SMSFNon3GPP != nil || routing.Serving.IPSMGW != nil || routing.Serving.MSCNumber != "" {
		t.Fatalf("unexpected nodes in %+v", routing)
	}
}

func TestSendRoutingInfoForSMMSCAndIPSMGW(t *testing.T) {
	answer := successAnswer(diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.OctetString(avpMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000040f0")),
		diameter.OctetString(avpIPSMGWNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000050f0")),
		diameter.UTF8String(avpIPSMGWName, diameter.AVPFlagMandatory, tgpp.VendorID, "ipsmgw.example.org"),
	))

	routing, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "1"})
	if err != nil {
		t.Fatal(err)
	}

	if routing.Serving.MSCNumber != "15550000040" || routing.Serving.IPSMGW == nil ||
		routing.Serving.IPSMGW.Number != "15550000050" || routing.Serving.IPSMGW.Name != "ipsmgw.example.org" {
		t.Fatalf("routing = %+v", routing)
	}
}

func TestSendRoutingInfoForSMAbsentUser(t *testing.T) {
	answer := experimentalAnswer(tgpp.ResultErrorAbsentUser,
		diameter.Unsigned32(avpMWDStatus, diameter.AVPFlagMandatory, tgpp.VendorID, 2),
		diameter.Unsigned32(avpMMEAbsentUserDiagnosticSM, diameter.AVPFlagMandatory, tgpp.VendorID, 1),
	)

	_, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})
	if !IsExperimental(err, tgpp.ResultErrorAbsentUser) {
		t.Fatalf("err = %v, want absent user", err)
	}

	var re *ResultError
	if !errors.As(err, &re) || re.MWDStatus != 2 || re.Absent.MME == nil || *re.Absent.MME != 1 || re.Absent.SGSN != nil {
		t.Fatalf("result error = %+v", re)
	}
}

func TestSendRoutingInfoForSMFailures(t *testing.T) {
	tests := map[string]struct {
		answer *diameter.Message
		check  func(error) bool
	}{
		"unknown user": {
			experimentalAnswer(tgpp.ResultErrorUserUnknown),
			func(err error) bool { return IsExperimental(err, tgpp.ResultErrorUserUnknown) },
		},
		"service barred": {
			experimentalAnswer(tgpp.ResultErrorServiceBarred),
			func(err error) bool { return IsExperimental(err, tgpp.ResultErrorServiceBarred) },
		},
		"base protocol error": {
			&diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, 3002)}},
			func(err error) bool {
				var re *ResultError
				return errors.As(err, &re) && !re.Experimental && re.ResultCode == 3002
			},
		},
		"no result": {
			&diameter.Message{},
			func(err error) bool { return errors.Is(err, ErrMalformedAnswer) },
		},
		"no user name": {
			&diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess)}},
			func(err error) bool { return errors.Is(err, ErrMalformedAnswer) },
		},
		"no serving node": {
			successAnswer(),
			func(err error) bool { return errors.Is(err, ErrMalformedAnswer) },
		},
		"MME without number only": {
			successAnswer(diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.UTF8String(avpMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, "mme.example.org"),
				diameter.UTF8String(avpMMERealm, 0, tgpp.VendorID, "example.org"))),
			func(err error) bool { return errors.Is(err, ErrMalformedAnswer) },
		},
		"other vendor's experimental result": {
			&diameter.Message{AVPs: []diameter.AVP{diameter.Grouped(diameter.AVPExperimentalResult, diameter.AVPFlagMandatory, 0,
				diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, 9999),
				diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, tgpp.ResultErrorUserUnknown))}},
			func(err error) bool {
				var re *ResultError
				return errors.As(err, &re) && re.VendorID == 9999 && !IsExperimental(err, tgpp.ResultErrorUserUnknown)
			},
		},
		"bad serving node number": {
			successAnswer(diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.OctetString(avpMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, []byte{0xaa}))),
			func(err error) bool { return errors.Is(err, ErrMalformedAnswer) },
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newRouter(&fakeRequester{answer: tt.answer}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})
			if !tt.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestSendRoutingInfoForSMTransportError(t *testing.T) {
	_, err := newRouter(&fakeRequester{err: diameter.ErrNotConnected}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "15551230002"})
	if !errors.Is(err, diameter.ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

func TestSendRoutingInfoForSMInvalidMSISDN(t *testing.T) {
	if _, err := newRouter(&fakeRequester{}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "+1555"}); err == nil {
		t.Fatal("expected an error for a non-digit MSISDN")
	}
}

func TestSendRoutingInfoForSMMSCWithMMEWithoutNumber(t *testing.T) {
	answer := successAnswer(diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.OctetString(avpMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000040f0")),
		diameter.UTF8String(avpMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, "mme.example.org"),
		diameter.UTF8String(avpMMERealm, 0, tgpp.VendorID, "example.org"),
	))

	routing, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), Request{MSISDN: "1"})
	if err != nil {
		t.Fatal(err)
	}

	if routing.Serving.MSCNumber != "15550000040" || routing.Serving.MME != nil {
		t.Fatalf("Serving = %+v; an MME without MME-Number-for-MT-SMS is not a delivery target", routing.Serving)
	}
}
