package tpdu

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

func TestMessageClass(t *testing.T) {
	tests := []struct {
		dcs   byte
		class uint8
		ok    bool
	}{
		{0xf6, 2, true},
		{0x16, 2, true},
		{0xf5, 1, true},
		{0xf0, 0, true},
		{0x12, 2, true},
		{0x52, 2, true},
		{0x36, 2, true},
		{0x04, 0, false},
		{0x00, 0, false},
		{0x86, 0, false},
		{0xc2, 0, false},
		{0xe2, 0, false},
	}

	for _, tt := range tests {
		if class, ok := MessageClass(tt.dcs); class != tt.class || ok != tt.ok {
			t.Errorf("MessageClass(%#02x) = %d, %v; want %d, %v", tt.dcs, class, ok, tt.class, tt.ok)
		}
	}
}

func TestValidUserDataHeader(t *testing.T) {
	tests := map[string]bool{
		"00":               true,
		"027000":           true,
		"0500030a0201":     true,
		"0770000003010201": true,
		"":                 false,
		"01":               false,
		"037000":           false,
		"027001":           false,
		"0170":             false,
	}

	for h, want := range tests {
		if got := ValidUserDataHeader(mustHex(t, h)); got != want {
			t.Errorf("ValidUserDataHeader(%s) = %v, want %v", h, got, want)
		}
	}
}

func TestSplitUserData(t *testing.T) {
	header, rest, err := SplitUserData(true, mustHex(t, "027000aabb"))
	if err != nil || !bytes.Equal(header, mustHex(t, "027000")) || !bytes.Equal(rest, mustHex(t, "aabb")) {
		t.Fatalf("SplitUserData = %x, %x, %v", header, rest, err)
	}

	header, rest, err = SplitUserData(false, mustHex(t, "027000aabb"))
	if err != nil || header != nil || !bytes.Equal(rest, mustHex(t, "027000aabb")) {
		t.Fatalf("SplitUserData without UDHI = %x, %x, %v", header, rest, err)
	}

	if _, _, err := SplitUserData(true, mustHex(t, "05aa")); err == nil {
		t.Fatal("SplitUserData of a truncated header succeeded")
	}
}

func TestDecodeDeliverReport(t *testing.T) {
	b := func(v byte) *byte { return &v }

	tests := map[string]struct {
		tpdu string
		ack  bool
		want DeliverReport
	}{
		"no optional parameters": {"0000", true, DeliverReport{}},
		"no TP-PI":               {"00", true, DeliverReport{}},
		"parameters and user data": {
			"00077ff603aabbcc", true,
			DeliverReport{ProtocolIdentifier: b(0x7f), DataCodingScheme: b(0xf6), UserDataLength: b(3), UserData: mustHex(t, "aabbcc")},
		},
		"user data header": {
			"40077ff605027100aabb", true,
			DeliverReport{
				UserDataHeader: true, ProtocolIdentifier: b(0x7f), DataCodingScheme: b(0xf6), UserDataLength: b(5),
				UserData: mustHex(t, "027100aabb"),
			},
		},
		"extension octet": {
			"008100" + "7f", true,
			DeliverReport{ProtocolIdentifier: b(0x7f)},
		},
		"user data length without DCS counts septets": {
			"000408" + "e8329bfd060101", true,
			DeliverReport{UserDataLength: b(8), UserData: mustHex(t, "e8329bfd060101")},
		},
		"empty user data": {
			"000600" + "00", true,
			DeliverReport{DataCodingScheme: b(0x00), UserDataLength: b(0), UserData: []byte{}},
		},
		"reserved bit discards trailing octets": {
			"0045" + "7f" + "01" + "aa" + "ffff", true,
			DeliverReport{ProtocolIdentifier: b(0x7f), UserDataLength: b(1), UserData: mustHex(t, "aa")},
		},
		"RP-ERROR without TP-PI":                   {"00d5", false, DeliverReport{FailureCause: b(0xd5)}},
		"RP-ERROR with unused bits is unspecified": {"a0d5077ff602cafe", false, DeliverReport{FailureCause: b(FailureUnspecified)}},
		"RP-ACK ignores unused bits": {
			"8407" + "7ff602cafe", true,
			DeliverReport{ProtocolIdentifier: b(0x7f), DataCodingScheme: b(0xf6), UserDataLength: b(2), UserData: mustHex(t, "cafe")},
		},
		"RP-ERROR with user data": {
			"00d5077ff602cafe", false,
			DeliverReport{
				FailureCause: b(0xd5), ProtocolIdentifier: b(0x7f), DataCodingScheme: b(0xf6), UserDataLength: b(2),
				UserData: mustHex(t, "cafe"),
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeDeliverReport(mustHex(t, tt.tpdu), tt.ack)
			if err != nil {
				t.Fatalf("DecodeDeliverReport: %v", err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("report = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeDeliverReportErrors(t *testing.T) {
	tests := map[string]struct {
		tpdu string
		ack  bool
	}{
		"empty":                        {"", true},
		"SMS-SUBMIT-REPORT":            {"0100", true},
		"truncated TP-PID":             {"0001", true},
		"truncated TP-DCS":             {"00037f", true},
		"truncated TP-UDL":             {"00077ff6", true},
		"truncated user data":          {"00077ff603aabb", true},
		"truncated extension":          {"0081", true},
		"trailing octets":              {"0001" + "7f" + "aa", true},
		"RP-ERROR without TP-FCS":      {"00", false},
		"RP-ERROR truncated user data": {"00d5077ff603cafe", false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if r, err := DecodeDeliverReport(mustHex(t, tt.tpdu), tt.ack); err == nil {
				t.Fatalf("DecodeDeliverReport = %+v, want an error", r)
			}
		})
	}
}

// A (U)SIM data download message keeps every TPDU parameter on its way from the SMS-SUBMIT to the SMS-DELIVER: the ME
// passes it to the (U)SIM in its entirety (TS 23.040 clause 9.2.3.9).
func TestDeliverFromSubmitKeepsSIMDataDownload(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "41"+"00"+"0b91"+"5155210300f2"+"7f"+"f6"+"08"+"027000"+"0011223344"))
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	originator := Address{TypeOfNumber: TypeOfNumberInternational, NumberingPlan: NumberingPlanISDN, Digits: "15550001111"}
	ts := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	got, err := DeliverFromSubmit(s, originator, ts).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	want := mustHex(t, "44"+"0b91"+"5155001011f1"+"7f"+"f6"+"62909201000000"+"08"+"027000"+"0011223344")
	if !bytes.Equal(got, want) {
		t.Fatalf("SMS-DELIVER = %x, want %x", got, want)
	}
}
