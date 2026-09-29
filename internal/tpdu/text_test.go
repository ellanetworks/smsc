package tpdu

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeTextSingleGSM7(t *testing.T) {
	parts, enc, err := EncodeText("hello", 0)
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}

	if enc != EncodingGSM7 || len(parts) != 1 {
		t.Fatalf("encoding = %s, parts = %d", enc, len(parts))
	}

	p := parts[0]
	if p.UserDataHeader || p.DataCodingScheme != 0x00 || p.UserDataLength != 5 || !bytes.Equal(p.UserData, mustHex(t, "e8329bfd06")) {
		t.Fatalf("part = %+v", p)
	}
}

func TestEncodeTextSingleUCS2(t *testing.T) {
	parts, enc, err := EncodeText("日本", 0)
	if err != nil {
		t.Fatalf("EncodeText: %v", err)
	}

	p := parts[0]
	if enc != EncodingUCS2 || len(parts) != 1 || p.UserDataHeader || p.DataCodingScheme != 0x08 || p.UserDataLength != 4 ||
		!bytes.Equal(p.UserData, mustHex(t, "65e5672c")) {
		t.Fatalf("encoding = %s, parts = %+v", enc, parts)
	}
}

func TestEncodeTextPartSizes(t *testing.T) {
	tests := map[string]struct {
		text     string
		encoding Encoding
		udls     []uint8
	}{
		"160 septets fit one message":      {strings.Repeat("a", 160), EncodingGSM7, []uint8{160}},
		"161 septets need two":             {strings.Repeat("a", 161), EncodingGSM7, []uint8{160, 15}},
		"extension counts two septets":     {strings.Repeat("a", 159) + "€", EncodingGSM7, []uint8{7 + 153, 7 + 8}},
		"escape pair is not split":         {strings.Repeat("a", 152) + "€bbbbbbb", EncodingGSM7, []uint8{7 + 152, 7 + 9}},
		"70 UCS-2 characters fit one":      {strings.Repeat("日", 70), EncodingUCS2, []uint8{140}},
		"71 UCS-2 characters need two":     {strings.Repeat("日", 71), EncodingUCS2, []uint8{6 + 134, 6 + 8}},
		"surrogate pair is not split":      {strings.Repeat("日", 66) + "😀" + strings.Repeat("日", 5), EncodingUCS2, []uint8{6 + 132, 6 + 14}},
		"one non-GSM character forces UCS": {strings.Repeat("a", 100) + "日", EncodingUCS2, []uint8{6 + 134, 6 + 68}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			parts, enc, err := EncodeText(tc.text, 7)
			if err != nil {
				t.Fatalf("EncodeText: %v", err)
			}

			if enc != tc.encoding || len(parts) != len(tc.udls) {
				t.Fatalf("encoding = %s, parts = %d", enc, len(parts))
			}

			for i, p := range parts {
				if p.UserDataLength != tc.udls[i] {
					t.Fatalf("part %d UDL = %d, want %d", i+1, p.UserDataLength, tc.udls[i])
				}

				if len(parts) > 1 && (!p.UserDataHeader || !bytes.Equal(p.UserData[:6], []byte{5, 0, 3, 7, byte(len(parts)), byte(i + 1)})) {
					t.Fatalf("part %d header = %x", i+1, p.UserData[:6])
				}

				if len(p.UserData) > maxUserDataOctets {
					t.Fatalf("part %d has %d octets", i+1, len(p.UserData))
				}
			}
		})
	}
}

func TestEncodeTextTooLong(t *testing.T) {
	if _, _, err := EncodeText(strings.Repeat("a", 255*153+1), 0); !errors.Is(err, ErrTextTooLong) {
		t.Fatalf("err = %v", err)
	}
}

func TestTextRoundTripsThroughSubmit(t *testing.T) {
	for _, text := range []string{
		"hello",
		"@£$¥ {}[]~|€^\\ ÄÖÑÜ§¿äöñüà",
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 8),
		"日本語のテキスト 😀",
		strings.Repeat("日本語のテキスト 😀", 10),
	} {
		parts, enc, err := EncodeText(text, 42)
		if err != nil {
			t.Fatalf("EncodeText(%q): %v", text, err)
		}

		var got strings.Builder

		for i, p := range parts {
			b, err := Submit{
				Destination:      Address{TypeOfNumber: TypeOfNumberInternational, NumberingPlan: NumberingPlanISDN, Digits: "15551230002"},
				UserDataHeader:   p.UserDataHeader,
				DataCodingScheme: p.DataCodingScheme,
				UserDataLength:   p.UserDataLength,
				UserData:         p.UserData,
			}.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			s, err := DecodeSubmit(b)
			if err != nil {
				t.Fatalf("DecodeSubmit: %v", err)
			}

			c, err := s.Content()
			if err != nil {
				t.Fatalf("Content: %v", err)
			}

			if c.Encoding != enc {
				t.Fatalf("encoding = %s, want %s", c.Encoding, enc)
			}

			if len(parts) > 1 && (c.Concatenation == nil || *c.Concatenation != Concatenation{Reference: 42, Part: uint8(i + 1), Total: uint8(len(parts))}) {
				t.Fatalf("part %d concatenation = %+v", i+1, c.Concatenation)
			}

			if len(parts) == 1 && c.Concatenation != nil {
				t.Fatalf("single message has concatenation %+v", c.Concatenation)
			}

			got.WriteString(c.Text)
		}

		if got.String() != text {
			t.Fatalf("round trip = %q, want %q", got.String(), text)
		}
	}
}

func TestContentSixteenBitConcatenation(t *testing.T) {
	ud := append([]byte{6, 0x08, 4, 0x12, 0x34, 3, 2}, 0x00, 0x68)
	s := Submit{UserDataHeader: true, DataCodingScheme: 0x08, UserDataLength: uint8(len(ud)), UserData: ud}

	c, err := s.Content()
	if err != nil {
		t.Fatalf("Content: %v", err)
	}

	if c.Encoding != EncodingUCS2 || c.Text != "h" || c.Concatenation == nil ||
		*c.Concatenation != (Concatenation{Reference: 0x1234, Part: 2, Total: 3}) {
		t.Fatalf("content = %+v, concatenation = %+v", c, c.Concatenation)
	}
}

func TestContentBinary(t *testing.T) {
	c, err := Submit{DataCodingScheme: 0x04, UserDataLength: 2, UserData: []byte{1, 2}}.Content()
	if err != nil || c.Encoding != EncodingBinary || c.Text != "" {
		t.Fatalf("content = %+v, %v", c, err)
	}
}

func TestContentTruncatedHeader(t *testing.T) {
	if _, err := (Submit{UserDataHeader: true, DataCodingScheme: 0x04, UserDataLength: 2, UserData: []byte{5, 0}}).Content(); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSubmitEncodeMatchesDecode(t *testing.T) {
	b := mustHex(t, "31"+"07"+"0b91"+"5155210300f0"+"00"+"00"+"aa"+"05"+"e8329bfd06")

	s, err := DecodeSubmit(b)
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	got, err := s.Encode()
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("Encode = %x, %v; want %x", got, err, b)
	}
}

func gsm7Submit(septets ...byte) Submit {
	p := gsm7Part(nil, septets)

	return Submit{DataCodingScheme: p.DataCodingScheme, UserDataLength: p.UserDataLength, UserData: p.UserData}
}

func TestContentGSM7Escapes(t *testing.T) {
	tests := map[string]struct {
		septets []byte
		want    string
	}{
		"extension character":        {[]byte{0x61, 0x1b, 0x65, 0x62}, "a€b"},
		"unknown extension code":     {[]byte{0x1b, 0x41}, "A"},
		"reserved second escape":     {[]byte{0x61, 0x1b, 0x1b, 0x62}, "a b"},
		"reserved escape then extra": {[]byte{0x1b, 0x1b, 0x65}, " e"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c, err := gsm7Submit(tc.septets...).Content()
			if err != nil || c.Text != tc.want {
				t.Fatalf("text = %q, %v; want %q", c.Text, err, tc.want)
			}
		})
	}
}

func TestContentConcatenationValidation(t *testing.T) {
	tests := map[string]struct {
		header []byte
		want   *Concatenation
	}{
		"valid":                    {[]byte{5, 0x00, 3, 9, 2, 1}, &Concatenation{Reference: 9, Total: 2, Part: 1}},
		"after another element":    {[]byte{9, 0x04, 2, 1, 2, 0x00, 3, 9, 2, 2}, &Concatenation{Reference: 9, Total: 2, Part: 2}},
		"zero total":               {[]byte{5, 0x00, 3, 9, 0, 1}, nil},
		"zero part":                {[]byte{5, 0x00, 3, 9, 2, 0}, nil},
		"part beyond total":        {[]byte{5, 0x00, 3, 9, 2, 3}, nil},
		"last occurrence wins":     {[]byte{10, 0x00, 3, 9, 2, 1, 0x00, 3, 7, 3, 3}, &Concatenation{Reference: 7, Total: 3, Part: 3}},
		"malformed final element":  {[]byte{8, 0x00, 3, 9, 2, 1, 0x04, 2, 1}, nil},
		"dangling identifier":      {[]byte{6, 0x00, 3, 9, 2, 1, 0x04}, nil},
		"wrong concatenation size": {[]byte{4, 0x00, 2, 9, 2}, nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ud := append(append([]byte(nil), tc.header...), 0x00, 0x68)

			c, err := Submit{UserDataHeader: true, DataCodingScheme: 0x08, UserDataLength: uint8(len(ud)), UserData: ud}.Content()
			if err != nil {
				t.Fatalf("Content: %v", err)
			}

			if !reflect.DeepEqual(c.Concatenation, tc.want) || c.Text != "h" {
				t.Fatalf("concatenation = %+v, text = %q; want %+v", c.Concatenation, c.Text, tc.want)
			}
		})
	}
}
