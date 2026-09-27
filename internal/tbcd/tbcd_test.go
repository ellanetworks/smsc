package tbcd

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestDecode(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"5155210300f1", "15551230001", false},
		{"21436587", "12345678", false},
		{"f1", "1", false},
		{"1f21", "", true},
		{"", "", true},
		{"a1", "", true},
	}

	for _, tt := range tests {
		b, _ := hex.DecodeString(tt.in)

		got, err := Decode(b)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Decode(%s) = %q, %v", tt.in, got, err)
		}
	}
}

func TestEncode(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"15551230001", "5155210300f1", false},
		{"12345678", "21436587", false},
		{"1", "f1", false},
		{"", "", true},
		{"12a", "", true},
		{"+1", "", true},
	}

	for _, tt := range tests {
		got, err := Encode(tt.in)
		want, _ := hex.DecodeString(tt.want)

		if (err != nil) != tt.wantErr || !bytes.Equal(got, want) {
			t.Errorf("Encode(%q) = %x, %v", tt.in, got, err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, digits := range []string{"1", "12", "15551230001", "001010000000001"} {
		b, err := Encode(digits)
		if err != nil {
			t.Fatal(err)
		}

		got, err := Decode(b)
		if err != nil || got != digits {
			t.Fatalf("round trip %q = %q, %v", digits, got, err)
		}
	}
}
