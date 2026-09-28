package s6c

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
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
		CommandCode:   s6c.CommandSendRoutingInfoForSM,
		ApplicationID: s6c.ApplicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, "smsc.example.org;1;1"),
			diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, "001010000000001"),
		}, extra...),
	}
}

func mmeServingNode(t *testing.T) diameter.AVP {
	return diameter.Grouped(s6c.AVPServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.UTF8String(s6c.AVPMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, "mme.example.org"),
		diameter.UTF8String(s6c.AVPMMERealm, 0, tgpp.VendorID, "example.org"),
		diameter.OctetString(tgpp.AVPMMENumberForMTSMS, 0, tgpp.VendorID, mustHex(t, "5155000010f0")),
	)
}

func experimentalAnswer(code uint32, extra ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		CommandCode:   s6c.CommandSendRoutingInfoForSM,
		ApplicationID: s6c.ApplicationID,
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

	if _, err := newRouter(f).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002", SingleAttempt: true}); err != nil {
		t.Fatalf("SendRoutingInfoForSM: %v", err)
	}

	req := f.req
	if f.peer != "hss.example.org" || req.CommandCode != s6c.CommandSendRoutingInfoForSM || req.ApplicationID != s6c.ApplicationID ||
		req.Flags != diameter.FlagRequest|diameter.FlagProxiable {
		t.Fatalf("request header = %+v to %q", req, f.peer)
	}

	if req.AVPs[0].Code != diameter.AVPSessionID || req.AVPs[0].UTF8String() != "smsc.example.org;1;1" {
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
		"SM-RP-MTI":          {s6c.AVPSMRPMTI, tgpp.VendorID, mustHex(t, "00000000")},
		"SRR-Flags":          {s6c.AVPSRRFlags, tgpp.VendorID, mustHex(t, "00000005")},
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

	if unsigned(t, listID) != 1 || unsigned(t, list) != s6c.FeatureSMSFSupport {
		t.Fatalf("Supported-Features = %+v", inner)
	}
}

func TestSendRoutingInfoForSMWithoutSingleAttempt(t *testing.T) {
	f := &fakeRequester{answer: successAnswer(mmeServingNode(t))}

	if _, err := newRouter(f).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002"}); err != nil {
		t.Fatal(err)
	}

	flags, _ := f.req.Find(s6c.AVPSRRFlags, tgpp.VendorID)
	if unsigned(t, flags) != s6c.SRRFlagGPRSIndicator {
		t.Fatalf("SRR-Flags = %d", unsigned(t, flags))
	}
}

func TestSendRoutingInfoForSMParsesAllServingNodes(t *testing.T) {
	answer := successAnswer(
		mmeServingNode(t),
		diameter.Grouped(s6c.AVPAdditionalServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(s6c.AVPSGSNName, 0, tgpp.VendorID, "sgsn.example.org"),
			diameter.UTF8String(s6c.AVPSGSNRealm, 0, tgpp.VendorID, "example.org"),
			diameter.OctetString(tgpp.AVPSGSNNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000020f0")),
		),
		diameter.Grouped(s6c.AVPSMSF3GPPAddress, 0, tgpp.VendorID,
			diameter.OctetString(s6c.AVPSMSF3GPPNumber, 0, tgpp.VendorID, mustHex(t, "5155000030f0")),
			diameter.UTF8String(s6c.AVPSMSF3GPPName, 0, tgpp.VendorID, "smsf.example.org"),
			diameter.UTF8String(s6c.AVPSMSF3GPPRealm, 0, tgpp.VendorID, "example.org"),
		),
		diameter.OctetString(s6c.AVPLMSI, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "01020304")),
		diameter.Unsigned32(s6c.AVPMWDStatus, diameter.AVPFlagMandatory, tgpp.VendorID, 2),
	)

	routing, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002"})
	if err != nil {
		t.Fatalf("SendRoutingInfoForSM: %v", err)
	}

	if routing.IMSI != "001010000000001" || routing.MWDStatus != 2 || !bytes.Equal(routing.LMSI, mustHex(t, "01020304")) {
		t.Fatalf("routing = %+v", routing)
	}

	if routing.Serving == nil || routing.Serving.SGSN != nil ||
		*routing.Serving.MME != (s6c.NodeAddress{Name: "mme.example.org", Realm: "example.org", Number: "15550000010"}) {
		t.Fatalf("Serving = %+v", routing.Serving)
	}

	if routing.Additional == nil || routing.Additional.MME != nil ||
		*routing.Additional.SGSN != (s6c.NodeAddress{Name: "sgsn.example.org", Realm: "example.org", Number: "15550000020"}) {
		t.Fatalf("Additional = %+v", routing.Additional)
	}

	if *routing.SMSF3GPP != (s6c.NodeAddress{Name: "smsf.example.org", Realm: "example.org", Number: "15550000030"}) {
		t.Fatalf("SMSF = %+v", routing.SMSF3GPP)
	}

	if routing.SMSFNon3GPP != nil || routing.Serving.IPSMGW != nil || routing.Serving.MSCNumber != "" {
		t.Fatalf("unexpected nodes in %+v", routing)
	}
}

func TestSendRoutingInfoForSMMSCAndIPSMGW(t *testing.T) {
	answer := successAnswer(diameter.Grouped(s6c.AVPServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.OctetString(s6c.AVPMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000040f0")),
		diameter.OctetString(s6c.AVPIPSMGWNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000050f0")),
		diameter.UTF8String(s6c.AVPIPSMGWName, diameter.AVPFlagMandatory, tgpp.VendorID, "ipsmgw.example.org"),
	))

	routing, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "1"})
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
		diameter.Unsigned32(s6c.AVPMWDStatus, diameter.AVPFlagMandatory, tgpp.VendorID, 2),
		diameter.Unsigned32(s6c.AVPMMEAbsentUserDiagnosticSM, diameter.AVPFlagMandatory, tgpp.VendorID, 1),
	)

	_, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002"})
	if !tgpp.IsExperimental(err, tgpp.ResultErrorAbsentUser) {
		t.Fatalf("err = %v, want absent user", err)
	}

	var re *s6c.ResultError
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
			func(err error) bool { return tgpp.IsExperimental(err, tgpp.ResultErrorUserUnknown) },
		},
		"service barred": {
			experimentalAnswer(tgpp.ResultErrorServiceBarred),
			func(err error) bool { return tgpp.IsExperimental(err, tgpp.ResultErrorServiceBarred) },
		},
		"base protocol error": {
			&diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, 3002)}},
			func(err error) bool {
				var re *s6c.ResultError
				return errors.As(err, &re) && !re.Experimental && re.Code == 3002
			},
		},
		"no result": {
			&diameter.Message{},
			func(err error) bool { return errors.Is(err, s6c.ErrMalformedAnswer) },
		},
		"no user name": {
			&diameter.Message{AVPs: []diameter.AVP{diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess)}},
			func(err error) bool { return errors.Is(err, s6c.ErrMalformedAnswer) },
		},
		"no serving node": {
			successAnswer(),
			func(err error) bool { return errors.Is(err, s6c.ErrMalformedAnswer) },
		},
		"MME without number only": {
			successAnswer(diameter.Grouped(s6c.AVPServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.UTF8String(s6c.AVPMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, "mme.example.org"),
				diameter.UTF8String(s6c.AVPMMERealm, 0, tgpp.VendorID, "example.org"))),
			func(err error) bool { return errors.Is(err, s6c.ErrMalformedAnswer) },
		},
		"other vendor's experimental result": {
			&diameter.Message{AVPs: []diameter.AVP{diameter.Grouped(diameter.AVPExperimentalResult, diameter.AVPFlagMandatory, 0,
				diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, 9999),
				diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, tgpp.ResultErrorUserUnknown))}},
			func(err error) bool {
				var re *s6c.ResultError
				return errors.As(err, &re) && re.VendorID == 9999 && !tgpp.IsExperimental(err, tgpp.ResultErrorUserUnknown)
			},
		},
		"bad serving node number": {
			successAnswer(diameter.Grouped(s6c.AVPServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.OctetString(s6c.AVPMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, []byte{0xaa}))),
			func(err error) bool { return errors.Is(err, s6c.ErrMalformedAnswer) },
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newRouter(&fakeRequester{answer: tt.answer}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002"})
			if !tt.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestSendRoutingInfoForSMTransportError(t *testing.T) {
	_, err := newRouter(&fakeRequester{err: diameter.ErrNotConnected}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "15551230002"})
	if !errors.Is(err, diameter.ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
}

func TestSendRoutingInfoForSMInvalidMSISDN(t *testing.T) {
	if _, err := newRouter(&fakeRequester{}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "+1555"}); err == nil {
		t.Fatal("expected an error for a non-digit MSISDN")
	}
}

func TestSendRoutingInfoForSMMSCWithMMEWithoutNumber(t *testing.T) {
	answer := successAnswer(diameter.Grouped(s6c.AVPServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
		diameter.OctetString(s6c.AVPMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155000040f0")),
		diameter.UTF8String(s6c.AVPMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, "mme.example.org"),
		diameter.UTF8String(s6c.AVPMMERealm, 0, tgpp.VendorID, "example.org"),
	))

	routing, err := newRouter(&fakeRequester{answer: answer}).SendRoutingInfoForSM(context.Background(), s6c.RoutingRequest{MSISDN: "1"})
	if err != nil {
		t.Fatal(err)
	}

	if routing.Serving.MSCNumber != "15550000040" || routing.Serving.MME != nil {
		t.Fatalf("Serving = %+v; an MME without MME-Number-for-MT-SMS is not a delivery target", routing.Serving)
	}
}
