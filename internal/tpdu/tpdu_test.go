package tpdu

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestDecodeSubmitRelativeValidity(t *testing.T) {
	b := mustHex(t, "31"+"07"+"0b91"+"5155210300f0"+"00"+"00"+"aa"+"05"+"e8329bfd06")

	s, err := DecodeSubmit(b)
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	if s.MessageReference != 0x07 || !s.StatusReportRequest || s.RejectDuplicates || s.UserDataHeader || s.ReplyPath {
		t.Fatalf("flags = %+v", s)
	}

	if s.ValidityPeriodFormat != ValidityPeriodRelative || !bytes.Equal(s.ValidityPeriod, []byte{0xaa}) {
		t.Fatalf("validity = %d %x", s.ValidityPeriodFormat, s.ValidityPeriod)
	}

	want := Address{TypeOfNumber: 0x1, NumberingPlan: 0x1, Digits: "15551230000"}
	if s.Destination != want {
		t.Fatalf("destination = %+v, want %+v", s.Destination, want)
	}

	if s.UserDataLength != 5 || !bytes.Equal(s.UserData, mustHex(t, "e8329bfd06")) {
		t.Fatalf("user data = %d %x", s.UserDataLength, s.UserData)
	}
}

func TestDecodeSubmitWithoutValidity(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "01"+"00"+"0481"+"2143"+"00"+"04"+"03"+"010203"))
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	want := Address{TypeOfNumber: 0x0, NumberingPlan: 0x1, Digits: "1234"}
	if s.Destination != want || s.ValidityPeriodFormat != ValidityPeriodAbsent || s.ValidityPeriod != nil {
		t.Fatalf("submit = %+v", s)
	}

	if s.DataCodingScheme != 0x04 || !bytes.Equal(s.UserData, []byte{1, 2, 3}) {
		t.Fatalf("user data = %x", s.UserData)
	}
}

func TestDecodeSubmitAbsoluteValidity(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "19"+"00"+"0381"+"21f3"+"00"+"08"+"62907221000000"+"02"+"0041"))
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	if s.ValidityPeriodFormat != ValidityPeriodAbsolute || len(s.ValidityPeriod) != 7 {
		t.Fatalf("validity = %d %x", s.ValidityPeriodFormat, s.ValidityPeriod)
	}

	if s.Destination.Digits != "123" || !bytes.Equal(s.UserData, []byte{0x00, 0x41}) {
		t.Fatalf("submit = %+v", s)
	}
}

func TestDecodeSubmitEmptyUserData(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "01"+"00"+"0281"+"21"+"00"+"00"+"00"))
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	if s.UserDataLength != 0 || s.UserData != nil {
		t.Fatalf("user data = %d %x", s.UserDataLength, s.UserData)
	}
}

func TestDecodeSubmitErrors(t *testing.T) {
	tests := map[string]string{
		"empty":             "",
		"deliver type":      "00" + "00" + "0281" + "21" + "00" + "00" + "00",
		"truncated address": "01" + "00" + "0b91" + "5155",
		"truncated vp":      "11" + "00" + "0281" + "21" + "00" + "00",
		"truncated ud":      "01" + "00" + "0281" + "21" + "00" + "00" + "05" + "e832",
		"trailing octets":   "01" + "00" + "0281" + "21" + "00" + "04" + "01" + "0102",
		"invalid digit":     "01" + "00" + "0281" + "f1" + "00" + "00" + "00",
	}

	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSubmit(mustHex(t, in)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestDecodeSubmitAlphanumericDestination(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "01"+"00"+"07d0"+"45363b0c"+"00"+"00"+"00"))
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	want := Address{TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "Ella"}
	if s.Destination != want {
		t.Fatalf("destination = %+v, want %+v", s.Destination, want)
	}
}

func TestUserDataOctets(t *testing.T) {
	tests := []struct {
		dcs, udl byte
		want     int
	}{
		{0x00, 160, 140},
		{0x00, 5, 5},
		{0x00, 8, 7},
		{0x04, 140, 140},
		{0x08, 70, 70},
		{0x0c, 8, 7},
		{0x10, 8, 7},
		{0x24, 10, 10},
		{0x40, 8, 7},
		{0x80, 8, 7},
		{0xc0, 8, 7},
		{0xd8, 8, 7},
		{0xe0, 8, 8},
		{0xf0, 8, 7},
		{0xf4, 8, 8},
	}

	for _, tt := range tests {
		if got := userDataOctets(tt.dcs, tt.udl); got != tt.want {
			t.Errorf("userDataOctets(0x%02x, %d) = %d, want %d", tt.dcs, tt.udl, got, tt.want)
		}
	}
}

func TestDeliverFromSubmitEncode(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "31"+"07"+"0b91"+"5155210300f0"+"00"+"00"+"aa"+"05"+"e8329bfd06"))
	if err != nil {
		t.Fatal(err)
	}

	originator := Address{TypeOfNumber: 0x1, NumberingPlan: 0x1, Digits: "15551230001"}
	receivedAt := time.Date(2026, 9, 27, 15, 4, 5, 0, time.FixedZone("", -4*3600))

	got, err := DeliverFromSubmit(s, originator, receivedAt).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	want := mustHex(t, "04"+"0b91"+"5155210300f1"+"00"+"00"+"62907251405069"+"05"+"e8329bfd06")
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = %x, want %x", got, want)
	}
}

func TestDeliverEncodeFlags(t *testing.T) {
	d := Deliver{
		MoreMessagesToSend: true,
		UserDataHeader:     true,
		ReplyPath:          true,
		Originator:         Address{TypeOfNumber: 0x2, NumberingPlan: 0x1, Digits: "123"},
		DataCodingScheme:   0x04,
		ServiceCentreTimeStamp: time.Date(2026, 1, 2, 3, 4, 5, 0,
			time.FixedZone("", 5*3600+45*60)),
		UserDataLength: 2,
		UserData:       []byte{0xab, 0xcd},
	}

	got, err := d.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	want := mustHex(t, "c0"+"03a1"+"21f3"+"00"+"04"+"62102030405032"+"02"+"abcd")
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = %x, want %x", got, want)
	}
}

func TestDeliverEncodeErrors(t *testing.T) {
	base := Deliver{
		Originator:             Address{TypeOfNumber: 0x1, NumberingPlan: 0x1, Digits: "1"},
		ServiceCentreTimeStamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	lengthMismatch := base
	lengthMismatch.UserDataLength = 5
	lengthMismatch.UserData = []byte{0x01}

	alphanumeric := base
	alphanumeric.Originator = Address{TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "EllaNetworks"}

	invalidDigit := base
	invalidDigit.Originator.Digits = "12x"

	oddZone := base
	oddZone.ServiceCentreTimeStamp = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 600))

	tests := map[string]Deliver{
		"length mismatch": lengthMismatch,
		"alphanumeric":    alphanumeric,
		"invalid digit":   invalidDigit,
		"odd time zone":   oddZone,
	}

	for name, d := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := d.Encode(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAddressRoundTrip(t *testing.T) {
	for _, digits := range []string{"", "1", "12", "123*#abc", "15551230001"} {
		in := Address{TypeOfNumber: 0x1, NumberingPlan: 0x1, Digits: digits}

		b, err := in.Encode()
		if err != nil {
			t.Fatalf("encode %q: %v", digits, err)
		}

		out, n, err := decodeAddress(b)
		if err != nil {
			t.Fatalf("decode %q: %v", digits, err)
		}

		if out != in || n != len(b) {
			t.Fatalf("round trip %q = %+v (%d octets), want %+v (%d octets)", digits, out, n, in, len(b))
		}
	}
}

func TestDeliverFromSubmitLeavesStatusReportIndicationToTheSC(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "31"+"07"+"0b91"+"5155210300f0"+"00"+"00"+"aa"+"05"+"e8329bfd06"))
	if err != nil || !s.StatusReportRequest {
		t.Fatalf("DecodeSubmit = %+v, %v", s, err)
	}

	b, err := DeliverFromSubmit(s, Address{TypeOfNumber: 0x1, NumberingPlan: 0x1, Digits: "15551230001"}, time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)).Encode()
	if err != nil {
		t.Fatal(err)
	}

	if b[0]&0x20 != 0 {
		t.Fatalf("TP-SRI set (first octet %02x): whether the SC returns a status report is for the SC to tell", b[0])
	}
}

var alphanumericVectors = []struct {
	name, field string
}{
	{"A", "02" + "d0" + "41"},
	{"Ella", "07" + "d0" + "45363b0c"},
	{"EllaSMSC", "0e" + "d0" + "45363b3c6d4e87"},
	{"ACME Ltd", "0e" + "d0" + "c161b30862d2c9"},
	{"EllaNetwork", "14" + "d0" + "45363bec2cd3ef6ff91a"},
	{"@@@@@@@", "0d" + "d0" + "00000000000000"},
	{"Ella@", "09" + "d0" + "45363b0c00"},
	{"Design@Home", "14" + "d0" + "c4f23c7d760390ef7619"},
}

func TestEncodeAlphanumericAddress(t *testing.T) {
	for _, tt := range alphanumericVectors {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Address{TypeOfNumber: TypeOfNumberAlphanumeric, Digits: tt.name}.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			if want := mustHex(t, tt.field); !bytes.Equal(got, want) {
				t.Fatalf("Encode = %x, want %x", got, want)
			}
		})
	}
}

func TestDecodeAlphanumericAddress(t *testing.T) {
	for _, tt := range alphanumericVectors {
		t.Run(tt.name, func(t *testing.T) {
			b := mustHex(t, tt.field)

			got, n, err := decodeAddress(b)
			if err != nil {
				t.Fatalf("decodeAddress: %v", err)
			}

			want := Address{TypeOfNumber: TypeOfNumberAlphanumeric, NumberingPlan: 0, Digits: tt.name}
			if got != want || n != len(b) {
				t.Fatalf("decodeAddress = %+v (%d octets), want %+v (%d octets)", got, n, want, len(b))
			}
		})
	}
}

func TestAlphanumericAddressLengths(t *testing.T) {
	for n := 1; n <= 11; n++ {
		in := Address{TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "EllaNetwork"[:n]}

		b, err := in.Encode()
		if err != nil {
			t.Fatalf("encode %q: %v", in.Digits, err)
		}

		semiOctets := (7*n + 3) / 4
		if wantLen := 2 + (semiOctets+1)/2; len(b) != wantLen || len(b) > 12 || int(b[0]) != semiOctets {
			t.Fatalf("encode %q = %x: %d octets, length %d; want %d octets, length %d", in.Digits, b, len(b), b[0], wantLen, semiOctets)
		}

		out, consumed, err := decodeAddress(b)
		if err != nil {
			t.Fatalf("decode %q: %v", in.Digits, err)
		}

		if out != in || consumed != len(b) {
			t.Fatalf("round trip %q = %+v (%d octets), want %+v (%d octets)", in.Digits, out, consumed, in, len(b))
		}
	}
}

func TestEncodeAlphanumericAddressErrors(t *testing.T) {
	tests := map[string]Address{
		"12 characters":      {TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "EllaNetworks"},
		"empty":              {TypeOfNumber: TypeOfNumberAlphanumeric, Digits: ""},
		"extension table":    {TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "Ella€"},
		"escape":             {TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "Ella\x1b"},
		"numbering plan set": {TypeOfNumber: TypeOfNumberAlphanumeric, NumberingPlan: NumberingPlanISDN, Digits: "Ella"},
	}

	for name, a := range tests {
		t.Run(name, func(t *testing.T) {
			if b, err := a.Encode(); err == nil {
				t.Fatalf("Encode = %x, expected an error", b)
			}
		})
	}
}

func TestDecodeAlphanumericAddressErrors(t *testing.T) {
	tests := map[string]struct {
		field string
		want  error
	}{
		"length 21": {"15" + "d0" + "45363bec2cd3ef6ff91a00", ErrUnsupportedAddress},
		"truncated": {"14" + "d0" + "45363bec2cd3ef6ff9", errTruncated},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeAddress(mustHex(t, tt.field)); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDeliverFromSubmitAlphanumericOriginator(t *testing.T) {
	s, err := DecodeSubmit(mustHex(t, "01"+"00"+"0b91"+"5155210300f2"+"00"+"00"+"02"+"e834"))
	if err != nil {
		t.Fatal(err)
	}

	originator := Address{TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "EllaSMSC"}
	receivedAt := time.Date(2026, 9, 27, 15, 4, 5, 0, time.FixedZone("", -4*3600))

	got, err := DeliverFromSubmit(s, originator, receivedAt).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	want := mustHex(t, "04"+"0ed045363b3c6d4e87"+"00"+"00"+"62907251405069"+"02"+"e834")
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode = %x, want %x", got, want)
	}
}
