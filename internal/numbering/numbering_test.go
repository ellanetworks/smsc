package numbering

import (
	"errors"
	"testing"
)

func TestInternational(t *testing.T) {
	plan := Plan{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"}

	tests := []struct {
		name   string
		ton    uint8
		digits string
		want   string
	}{
		{"international", 0x1, "33612345678", "33612345678"},
		{"national", 0x2, "612345678", "33612345678"},
		{"unknown with national prefix", 0x0, "0612345678", "33612345678"},
		{"unknown with international prefix", 0x0, "0015551230002", "15551230002"},
		{"unknown without prefix", 0x0, "612345678", "33612345678"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := plan.International(tt.ton, tt.digits)
			if err != nil || got != tt.want {
				t.Fatalf("International = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestInternationalPrefersTheLongerPrefix(t *testing.T) {
	tests := []struct {
		name   string
		plan   Plan
		digits string
		want   string
	}{
		{"international prefix extends the national one", Plan{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"}, "0044201234567", "44201234567"},
		{"national prefix only", Plan{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"}, "0612345678", "33612345678"},
		{"north american plan, national", Plan{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"}, "15551230002", "15551230002"},
		{"north american plan, international", Plan{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"}, "011447700900123", "447700900123"},
		{"north american plan, no prefix", Plan{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"}, "5551230002", "15551230002"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.plan.International(0x0, tt.digits)
			if err != nil || got != tt.want {
				t.Fatalf("International = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestInternationalWithoutPrefixes(t *testing.T) {
	got, err := Plan{CountryCode: "1"}.International(0x0, "5551230002")
	if err != nil || got != "15551230002" {
		t.Fatalf("International = %q, %v", got, err)
	}
}

func TestInternationalUnsupportedType(t *testing.T) {
	for _, ton := range []uint8{0x3, 0x4, 0x6} {
		if _, err := (Plan{CountryCode: "1"}).International(ton, "1234"); !errors.Is(err, ErrUnsupportedNumber) {
			t.Errorf("type %d: err = %v, want ErrUnsupportedNumber", ton, err)
		}
	}
}
