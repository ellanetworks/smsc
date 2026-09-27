package sgd

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tgpp"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

type fakeStore struct {
	stored []db.NewMessage
	err    error
}

func (s *fakeStore) CreateMessage(_ context.Context, m db.NewMessage) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}

	s.stored = append(s.stored, m)

	return int64(len(s.stored)), nil
}

var testNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

const testSCTS = "62907221000000"

func newTestHandler(store *fakeStore) *Handler {
	return &Handler{
		Identity:             diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org"},
		ServiceCentreAddress: "15550000000",
		Store:                store,
		Now:                  func() time.Time { return testNow },
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
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

const validSubmit = "15" + "07" + "0b91" + "5155210300f2" + "00" + "00" + "aa" + "05" + "e8329bfd06"

func baseAVPs() []diameter.AVP {
	return []diameter.AVP{
		diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, "mme.example.org;1"),
		diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
		diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, "mme.example.org"),
		diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, "example.org"),
		diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, "example.org"),
	}
}

func ofr(avps ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   CommandMOForwardShortMessage,
		ApplicationID: ApplicationID,
		HopByHopID:    1,
		EndToEndID:    2,
		AVPs:          append(baseAVPs(), avps...),
	}
}

func scAddress(t *testing.T, hexDigits string) diameter.AVP {
	return diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, hexDigits))
}

func userIdentifier(avps ...diameter.AVP) diameter.AVP {
	return diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID, avps...)
}

func msisdn(t *testing.T) diameter.AVP {
	return diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "5155210300f1"))
}

func smRPUI(t *testing.T, hexTPDU string) diameter.AVP {
	return diameter.OctetString(AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, hexTPDU))
}

func ofrWithSubmit(t *testing.T, hexTPDU string) *diameter.Message {
	return ofr(
		scAddress(t, "5155000000f0"),
		userIdentifier(diameter.UTF8String(1, diameter.AVPFlagMandatory, 0, "001010000000001"), msisdn(t)),
		smRPUI(t, hexTPDU),
	)
}

func resultCode(t *testing.T, ans *diameter.Message) uint32 {
	t.Helper()

	a, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		t.Fatal("Result-Code missing")
	}

	v, _ := a.Unsigned32()

	return v
}

func experimentalCode(t *testing.T, ans *diameter.Message) uint32 {
	t.Helper()

	if _, ok := ans.Find(diameter.AVPResultCode, 0); ok {
		t.Fatal("unexpected Result-Code alongside Experimental-Result")
	}

	er, ok := ans.Find(diameter.AVPExperimentalResult, 0)
	if !ok {
		t.Fatal("Experimental-Result missing")
	}

	inner, err := er.Grouped()
	if err != nil {
		t.Fatal(err)
	}

	code, _ := diameter.Find(inner, diameter.AVPExperimentalResultCode, 0)
	v, _ := code.Unsigned32()

	return v
}

func deliveryFailure(t *testing.T, ans *diameter.Message) (uint32, []byte) {
	t.Helper()

	if code := experimentalCode(t, ans); code != tgpp.ResultErrorSMDeliveryFailure {
		t.Fatalf("Experimental-Result-Code = %d, want %d", code, tgpp.ResultErrorSMDeliveryFailure)
	}

	cause, ok := ans.Find(AVPSMDeliveryFailureCause, tgpp.VendorID)
	if !ok {
		t.Fatal("SM-Delivery-Failure-Cause missing")
	}

	inner, err := cause.Grouped()
	if err != nil {
		t.Fatal(err)
	}

	enum, _ := diameter.Find(inner, AVPSMEnumeratedDeliveryFailureCause, tgpp.VendorID)
	c, _ := enum.Unsigned32()

	var diagnostic []byte
	if d, ok := diameter.Find(inner, AVPSMDiagnosticInfo, tgpp.VendorID); ok {
		diagnostic = d.Data
	}

	return c, diagnostic
}

func submitReport(t *testing.T, failureCause byte) []byte {
	t.Helper()

	return mustHex(t, "01"+hex.EncodeToString([]byte{failureCause})+"00"+testSCTS)
}

func failedAVP(t *testing.T, ans *diameter.Message) diameter.AVP {
	t.Helper()

	failed, ok := ans.Find(diameter.AVPFailedAVP, 0)
	if !ok {
		t.Fatal("Failed-AVP missing")
	}

	inner, err := failed.Grouped()
	if err != nil || len(inner) != 1 {
		t.Fatalf("Failed-AVP contents = %+v, %v", inner, err)
	}

	return inner[0]
}

func assertCommon(t *testing.T, ans *diameter.Message) {
	t.Helper()

	if ans.IsRequest() || ans.HopByHopID != 1 || ans.EndToEndID != 2 || ans.CommandCode != CommandMOForwardShortMessage {
		t.Fatalf("header = %+v", ans)
	}

	if ans.AVPs[0].Code != diameter.AVPSessionID {
		t.Fatalf("first AVP = %d, want Session-Id", ans.AVPs[0].Code)
	}

	state, ok := ans.Find(diameter.AVPAuthSessionState, 0)
	if !ok {
		t.Fatal("Auth-Session-State missing")
	}

	if v, _ := state.Unsigned32(); v != diameter.AuthSessionStateNoStateMaintained {
		t.Fatalf("Auth-Session-State = %d", v)
	}
}

func TestMOForwardStoresMessage(t *testing.T) {
	store := &fakeStore{}

	ans := newTestHandler(store).ServeDiameter(context.Background(), nil, ofrWithSubmit(t, validSubmit))
	assertCommon(t, ans)

	if code := resultCode(t, ans); code != diameter.ResultSuccess {
		t.Fatalf("Result-Code = %d", code)
	}

	if len(store.stored) != 1 {
		t.Fatalf("stored %d messages", len(store.stored))
	}

	got := store.stored[0]
	wantOriginator := db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1}
	wantRecipient := db.Address{Digits: "15551230002", TypeOfNumber: 1, NumberingPlan: 1}

	if got.Originator != wantOriginator || got.Recipient != wantRecipient || got.MessageReference != 7 ||
		!got.RejectDuplicates || got.Replace || got.SingleShot || !got.SubmittedAt.Equal(testNow) ||
		!got.ExpiresAt.Equal(testNow.Add(4*24*time.Hour)) || !bytes.Equal(got.TPDU, mustHex(t, validSubmit)) {
		t.Fatalf("stored = %+v", got)
	}
}

func TestMOForwardKeepsNationalNumberType(t *testing.T) {
	store := &fakeStore{}

	newTestHandler(store).ServeDiameter(context.Background(), nil,
		ofrWithSubmit(t, "01"+"00"+"04a1"+"2143"+"00"+"00"+"00"))

	if len(store.stored) != 1 || store.stored[0].Recipient != (db.Address{Digits: "1234", TypeOfNumber: 2, NumberingPlan: 1}) ||
		!store.stored[0].ExpiresAt.IsZero() {
		t.Fatalf("stored = %+v", store.stored)
	}
}

func TestMOForwardUnknownServiceCentre(t *testing.T) {
	req := ofr(scAddress(t, "5155000000f9"), userIdentifier(msisdn(t)), smRPUI(t, validSubmit))

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)
	assertCommon(t, ans)

	if cause, diag := deliveryFailure(t, ans); cause != CauseUnknownServiceCentre || diag != nil {
		t.Fatalf("cause = %d, diagnostic = %x", cause, diag)
	}
}

func TestMOForwardNoMSISDN(t *testing.T) {
	req := ofr(scAddress(t, "5155000000f0"),
		userIdentifier(diameter.UTF8String(1, diameter.AVPFlagMandatory, 0, "001010000000001")),
		smRPUI(t, validSubmit))

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)
	assertCommon(t, ans)

	if cause, _ := deliveryFailure(t, ans); cause != CauseUserNotSCUser {
		t.Fatalf("cause = %d", cause)
	}
}

func TestMOForwardSubmitRejections(t *testing.T) {
	tests := map[string]struct {
		tpdu string
		fcs  byte
	}{
		"alphanumeric destination":  {"01" + "00" + "04d0" + "c1e1" + "00" + "00" + "00", tpdu.FailureInvalidSMEAddress},
		"empty destination":         {"01" + "00" + "0091" + "00" + "00" + "00", tpdu.FailureInvalidSMEAddress},
		"sms-command":               {"02" + "00" + "00" + "00" + "00" + "0281" + "21" + "00", tpdu.FailureCommandUnsupported},
		"reserved message type":     {"03" + "00", tpdu.FailureTPDUNotSupported},
		"reserved validity format":  {"09" + "00" + "0281" + "21" + "00" + "00" + "04000000000000" + "00", tpdu.FailureValidityPeriodNotSupported},
		"truncated":                 {"01" + "00" + "0b91" + "5155", tpdu.FailureUnspecified},
		"user data too long":        {"01" + "00" + "0281" + "21" + "00" + "04" + "8d" + strings.Repeat("00", 141), tpdu.FailureUnspecified},
		"telematic interworking":    {"01" + "00" + "0281" + "21" + "22" + "00" + "00", tpdu.FailureTelematicInterworkingNotSupported},
		"invalid absolute validity": {"19" + "00" + "0281" + "21" + "00" + "00" + "62313150403000" + "00", tpdu.FailureValidityPeriodNotSupported},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}

			ans := newTestHandler(store).ServeDiameter(context.Background(), nil, ofrWithSubmit(t, tt.tpdu))
			assertCommon(t, ans)

			cause, diag := deliveryFailure(t, ans)
			if cause != CauseInvalidSMEAddress || !bytes.Equal(diag, submitReport(t, tt.fcs)) {
				t.Fatalf("cause = %d, diagnostic = %x", cause, diag)
			}

			if len(store.stored) != 0 {
				t.Fatal("rejected message was stored")
			}
		})
	}
}

func TestMOForwardDuplicate(t *testing.T) {
	ans := newTestHandler(&fakeStore{err: db.ErrDuplicate}).ServeDiameter(context.Background(), nil, ofrWithSubmit(t, validSubmit))
	assertCommon(t, ans)

	cause, diag := deliveryFailure(t, ans)
	if cause != CauseInvalidSMEAddress || !bytes.Equal(diag, submitReport(t, tpdu.FailureRejectedDuplicate)) {
		t.Fatalf("cause = %d, diagnostic = %x", cause, diag)
	}
}

func TestMOForwardStoreFailure(t *testing.T) {
	ans := newTestHandler(&fakeStore{err: errors.New("disk full")}).ServeDiameter(context.Background(), nil, ofrWithSubmit(t, validSubmit))
	assertCommon(t, ans)

	cause, diag := deliveryFailure(t, ans)
	if cause != CauseSCCongestion || !bytes.Equal(diag, submitReport(t, tpdu.FailureSCSystemFailure)) {
		t.Fatalf("cause = %d, diagnostic = %x", cause, diag)
	}
}

func TestMOForwardOversizedSMRPUI(t *testing.T) {
	big := diameter.OctetString(AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, make([]byte, maxSMRPUILength+1))
	req := ofr(scAddress(t, "5155000000f0"), userIdentifier(msisdn(t)), big)

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)

	if code := resultCode(t, ans); code != diameter.ResultInvalidAVPValue {
		t.Fatalf("Result-Code = %d", code)
	}

	if failed := failedAVP(t, ans); failed.Code != AVPSMRPUI {
		t.Fatalf("Failed-AVP holds %d", failed.Code)
	}
}

func TestMOForwardMSISDNLessDeliveryNotSupported(t *testing.T) {
	req := ofrWithSubmit(t, validSubmit)
	req.AVPs = append(req.AVPs, diameter.Grouped(AVPSMSMICorrelationID, 0, tgpp.VendorID))

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)
	assertCommon(t, ans)

	if code := experimentalCode(t, ans); code != tgpp.ResultErrorFacilityNotSupported {
		t.Fatalf("Experimental-Result-Code = %d", code)
	}
}

func TestMOForwardUnknownMandatoryAVP(t *testing.T) {
	unknown := diameter.Unsigned32(9999, diameter.AVPFlagMandatory, tgpp.VendorID, 1)
	req := ofrWithSubmit(t, validSubmit)
	req.AVPs = append(req.AVPs, unknown)

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)
	assertCommon(t, ans)

	if code := resultCode(t, ans); code != diameter.ResultAVPUnsupported {
		t.Fatalf("Result-Code = %d", code)
	}

	if failed := failedAVP(t, ans); failed.Code != 9999 || failed.VendorID != tgpp.VendorID {
		t.Fatalf("Failed-AVP = %+v", failed)
	}
}

func TestMOForwardUnknownOptionalAVPIgnored(t *testing.T) {
	req := ofrWithSubmit(t, validSubmit)
	req.AVPs = append(req.AVPs, diameter.Unsigned32(9999, 0, tgpp.VendorID, 1))

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)

	if code := resultCode(t, ans); code != diameter.ResultSuccess {
		t.Fatalf("Result-Code = %d", code)
	}
}

func TestMOForwardMissingAVPs(t *testing.T) {
	tests := map[string]struct {
		code, vendorID uint32
		minimum        int
	}{
		"Session-Id":         {diameter.AVPSessionID, 0, 0},
		"Auth-Session-State": {diameter.AVPAuthSessionState, 0, 4},
		"Origin-Host":        {diameter.AVPOriginHost, 0, 0},
		"Origin-Realm":       {diameter.AVPOriginRealm, 0, 0},
		"Destination-Realm":  {diameter.AVPDestinationRealm, 0, 0},
		"SC-Address":         {tgpp.AVPSCAddress, tgpp.VendorID, 0},
		"User-Identifier":    {tgpp.AVPUserIdentifier, tgpp.VendorID, 0},
		"SM-RP-UI":           {AVPSMRPUI, tgpp.VendorID, 0},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := ofrWithSubmit(t, validSubmit)

			var kept []diameter.AVP

			for _, a := range req.AVPs {
				if a.Code != tt.code || a.VendorID != tt.vendorID {
					kept = append(kept, a)
				}
			}

			req.AVPs = kept

			ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)

			if code := resultCode(t, ans); code != diameter.ResultMissingAVP {
				t.Fatalf("Result-Code = %d", code)
			}

			failed := failedAVP(t, ans)
			if failed.Code != tt.code || failed.VendorID != tt.vendorID || len(failed.Data) != tt.minimum {
				t.Fatalf("Failed-AVP = %+v", failed)
			}
		})
	}
}

func TestMOForwardInvalidSCAddress(t *testing.T) {
	req := ofr(scAddress(t, "5a"), userIdentifier(msisdn(t)), smRPUI(t, validSubmit))

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)

	if code := resultCode(t, ans); code != diameter.ResultInvalidAVPValue {
		t.Fatalf("Result-Code = %d", code)
	}

	if failed := failedAVP(t, ans); failed.Code != tgpp.AVPSCAddress {
		t.Fatalf("Failed-AVP holds %d", failed.Code)
	}
}

func TestUnsupportedCommand(t *testing.T) {
	req := ofrWithSubmit(t, validSubmit)
	req.CommandCode = 8388646

	ans := newTestHandler(&fakeStore{}).ServeDiameter(context.Background(), nil, req)

	if ans.Flags&diameter.FlagError == 0 || resultCode(t, ans) != diameter.ResultCommandUnsupported {
		t.Fatalf("answer = %+v", ans)
	}
}
