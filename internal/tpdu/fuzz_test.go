package tpdu

import (
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

func seedHex(f *testing.F, s string) {
	f.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		f.Fatal(err)
	}

	f.Add(b)
}

// TS 23.040 clause 9.2.2.2
func FuzzDecodeSubmit(f *testing.F) {
	seedHex(f, "31"+"07"+"0b91"+"5155210300f0"+"00"+"00"+"aa"+"05"+"e8329bfd06")
	seedHex(f, "01"+"00"+"0481"+"2143"+"00"+"04"+"03"+"010203")
	seedHex(f, "19"+"00"+"0381"+"21f3"+"00"+"08"+"62907221000000"+"02"+"0041")
	seedHex(f, "09"+"00"+"0381"+"21f3"+"00"+"00"+"42800000000000"+"01"+"41")
	seedHex(f, "41"+"00"+"0381"+"21f3"+"00"+"00"+"0a"+"050003cc0201"+"00")
	seedHex(f, "41"+"00"+"0381"+"21f3"+"00"+"08"+"0a"+"0605040b8423f0"+"0041")
	seedHex(f, "01"+"00"+"0ad0"+"c8329bfd06"+"00"+"00"+"00")
	seedHex(f, "02"+"00")

	received := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := DecodeSubmit(b)
		if err != nil {
			return
		}

		_, _ = s.Content()
		_, _, _ = s.Expiry(received)
		_ = s.SingleShot()

		if header, _, err := SplitUserData(s.UserDataHeader, s.UserData); err == nil {
			_ = ValidUserDataHeader(header)
			_ = HasApplicationPort(header)

			if IsSeptetCoded(s.DataCodingScheme) {
				_ = UnpackSeptets(s.UserData, len(header), int(s.UserDataLength))
			}
		}

		out, err := s.Encode()
		if err != nil {
			t.Fatalf("Encode of a decoded SMS-SUBMIT %x: %v", b, err)
		}

		again, err := DecodeSubmit(out)
		if err != nil {
			t.Fatalf("DecodeSubmit(%x), re-encoded from %x: %v", out, b, err)
		}

		if !reflect.DeepEqual(s, again) {
			t.Fatalf("round trip of %x:\n got %+v\nwant %+v", b, again, s)
		}
	})
}

// TS 23.040 clause 9.2.2.1a
func FuzzDecodeDeliverReport(f *testing.F) {
	for _, s := range []string{
		"00" + "00",
		"00" + "07" + "7f" + "f6" + "03" + "010203",
		"40" + "07" + "7f" + "f6" + "06" + "0270000001",
		"00" + "d4" + "07" + "7f" + "f6" + "03" + "010203",
		"00" + "d4" + "04" + "05" + "e8329bfd06",
		"00" + "d4" + "84" + "80" + "00",
		"04" + "d4",
	} {
		b, err := hex.DecodeString(s)
		if err != nil {
			f.Fatal(err)
		}

		f.Add(b, true)
		f.Add(b, false)
	}

	f.Fuzz(func(t *testing.T, b []byte, ack bool) {
		_, _ = DeliverReportFailureCause(b)

		r, err := DecodeDeliverReport(b, ack)
		if err != nil {
			return
		}

		if !ack && r.FailureCause == nil {
			t.Fatalf("negative SMS-DELIVER-REPORT %x decoded without a failure cause", b)
		}

		if r.UserDataLength == nil {
			return
		}

		header, _, err := SplitUserData(r.UserDataHeader, r.UserData)
		if err != nil {
			return
		}

		var dcs byte
		if r.DataCodingScheme != nil {
			dcs = *r.DataCodingScheme
		}

		if IsSeptetCoded(dcs) {
			_ = UnpackSeptets(r.UserData, len(header), int(*r.UserDataLength))
		}
	})
}

// TS 23.040 clause 9.2.2.3
func FuzzDecodeStatusReport(f *testing.F) {
	seedHex(f, "06"+"07"+"0b91"+"5155210300f0"+"62112021000000"+"62112021000000"+"00")
	seedHex(f, "02"+"00"+"00"+"80"+"62112021000000"+"62112021000000"+"40")

	f.Fuzz(func(t *testing.T, b []byte) {
		_ = SetMoreMessagesToSend(b, true)

		r, err := DecodeStatusReport(b)
		if err != nil {
			return
		}

		_ = r.Status.String()
	})
}

// TS 23.038 clause 6.1.2.1.1
func FuzzSeptets(f *testing.F) {
	f.Add([]byte{0xe8, 0x32, 0x9b, 0xfd, 0x06}, 0, 5)
	f.Add([]byte{0x05, 0x00, 0x03, 0xcc, 0x02, 0x01, 0x00}, 6, 8)
	f.Add([]byte{0x1b, 0x1b}, 0, 3)
	f.Add([]byte{}, 0, 255)

	f.Fuzz(func(t *testing.T, ud []byte, headerOctets, udl int) {
		if headerOctets < 0 || headerOctets > 140 || udl < 0 || udl > 255 {
			return
		}

		septets := UnpackSeptets(ud, headerOctets, udl)
		_ = SeptetsText(septets)

		_, packed := PackSeptets(nil, septets)
		if again := UnpackSeptets(packed, 0, len(septets)); !reflect.DeepEqual(again, septets) {
			t.Fatalf("septets %x packed to %x unpack to %x", septets, packed, again)
		}
	})
}
