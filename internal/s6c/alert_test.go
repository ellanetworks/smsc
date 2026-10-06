package s6c

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/settings"
)

var testTime = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

type alerts struct {
	msisdns []string
	err     error
}

func (a *alerts) alert(_ context.Context, msisdn string) error {
	a.msisdns = append(a.msisdns, msisdn)
	return a.err
}

func newAlertHandler(a *alerts) *AlertHandler {
	return &AlertHandler{
		Identity: func() diameter.Identity {
			return diameter.Identity{OriginHost: "smsc.example.org", OriginRealm: "example.org"}
		},
		Settings: func() settings.Settings {
			return settings.Settings{Operator: settings.Operator{ServiceCentreAddress: "15550000000"}}
		},
		Alert:  a.alert,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func alr(t *testing.T, extra ...diameter.AVP) *diameter.Message {
	t.Helper()

	return &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   s6c.CommandAlertServiceCentre,
		ApplicationID: s6c.ApplicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, "hss.example.org;1;1"),
			diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
			diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, "hss.example.org"),
			diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, "example.org"),
			diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, "example.org"),
		}, extra...),
	}
}

func scAddressAVP(t *testing.T, hexDigits string) diameter.AVP {
	t.Helper()

	return diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, hexDigits))
}

func answerResult(t *testing.T, ans *diameter.Message) uint32 {
	t.Helper()

	if ans.CommandCode != s6c.CommandAlertServiceCentre || ans.Flags&diameter.FlagRequest != 0 || ans.AVPs[0].Code != diameter.AVPSessionID {
		t.Fatalf("answer header = %+v", ans)
	}

	if state, ok := ans.Find(diameter.AVPAuthSessionState, 0); !ok || unsigned(t, state) != diameter.AuthSessionStateNoStateMaintained {
		t.Fatal("Auth-Session-State missing")
	}

	rc, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		t.Fatal("Result-Code missing")
	}

	return unsigned(t, rc)
}

func TestAlertServiceCentre(t *testing.T) {
	a := &alerts{}
	ans := newAlertHandler(a).ServeDiameter(context.Background(), nil,
		alr(t, scAddressAVP(t, "5155000000f0"), userIdentifier(t, "5155210300f2"),
			diameter.Time(s6c.AVPMaximumUEAvailabilityTime, 0, tgpp.VendorID, testTime)))

	if code := answerResult(t, ans); code != diameter.ResultSuccess || len(a.msisdns) != 1 || a.msisdns[0] != "15551230002" {
		t.Fatalf("result = %d, alerts = %v", code, a.msisdns)
	}
}

func TestAlertServiceCentreIgnored(t *testing.T) {
	tests := map[string][]diameter.AVP{
		"another service centre": {scAddressAVP(t, "5155000001f0"), userIdentifier(t, "5155210300f2")},
		"no MSISDN": {scAddressAVP(t, "5155000000f0"), diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, "001010000000002"))},
	}

	for name, avps := range tests {
		t.Run(name, func(t *testing.T) {
			a := &alerts{}

			ans := newAlertHandler(a).ServeDiameter(context.Background(), nil, alr(t, avps...))
			if code := answerResult(t, ans); code != diameter.ResultSuccess || len(a.msisdns) != 0 {
				t.Fatalf("result = %d, alerts = %v", code, a.msisdns)
			}
		})
	}
}

func TestAlertServiceCentreErrors(t *testing.T) {
	tests := map[string]struct {
		avps  []diameter.AVP
		store error
		want  uint32
	}{
		"missing SC-Address":      {[]diameter.AVP{userIdentifier(t, "5155210300f2")}, nil, diameter.ResultMissingAVP},
		"missing User-Identifier": {[]diameter.AVP{scAddressAVP(t, "5155000000f0")}, nil, diameter.ResultMissingAVP},
		"unknown mandatory AVP":   {[]diameter.AVP{scAddressAVP(t, "5155000000f0"), userIdentifier(t, "5155210300f2"), diameter.Unsigned32(9999, diameter.AVPFlagMandatory, tgpp.VendorID, 1)}, nil, diameter.ResultAVPUnsupported},
		"invalid SC-Address":      {[]diameter.AVP{scAddressAVP(t, "5a"), userIdentifier(t, "5155210300f2")}, nil, diameter.ResultInvalidAVPValue},
		"invalid User-Identifier": {[]diameter.AVP{scAddressAVP(t, "5155000000f0"), userIdentifier(t, "5a")}, nil, diameter.ResultInvalidAVPValue},
		"store failure":           {[]diameter.AVP{scAddressAVP(t, "5155000000f0"), userIdentifier(t, "5155210300f2")}, errors.New("disk full"), diameter.ResultUnableToComply},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ans := newAlertHandler(&alerts{err: tt.store}).ServeDiameter(context.Background(), nil, alr(t, tt.avps...))
			if code := answerResult(t, ans); code != tt.want {
				t.Fatalf("result = %d, want %d", code, tt.want)
			}

			if tt.want != diameter.ResultUnableToComply {
				if _, ok := ans.Find(diameter.AVPFailedAVP, 0); !ok {
					t.Fatal("Failed-AVP missing")
				}
			}
		})
	}
}
