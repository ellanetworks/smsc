package tpdu

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func submitWithVP(t *testing.T, first, vp string) Submit {
	t.Helper()

	s, err := DecodeSubmit(mustHex(t, first+"00"+"0281"+"21"+"00"+"00"+vp+"00"))
	if err != nil {
		t.Fatalf("DecodeSubmit: %v", err)
	}

	return s
}

func TestExpiry(t *testing.T) {
	received := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		first, vp string
		want      time.Time
		ok        bool
	}{
		"absent":             {"01", "", time.Time{}, false},
		"relative 5 minutes": {"11", "00", received.Add(5 * time.Minute), true},
		"relative 12 hours":  {"11", "8f", received.Add(12 * time.Hour), true},
		"relative 13 hours":  {"11", "91", received.Add(13 * time.Hour), true},
		"relative 2 days":    {"11", "a8", received.Add(48 * time.Hour), true},
		"relative 5 weeks":   {"11", "c5", received.Add(5 * 7 * 24 * time.Hour), true},
		"absolute":           {"19", "62103150403069", time.Date(2026, 1, 13, 5, 4, 3, 0, time.FixedZone("", -16*15*60)), true},
		"enhanced none":      {"09", "00000000000000", time.Time{}, false},
		"enhanced relative":  {"09", "01000000000000", received.Add(5 * time.Minute), true},
		"enhanced seconds":   {"09", "021e0000000000", received.Add(30 * time.Second), true},
		"enhanced hh:mm:ss":  {"09", "03102030000000", received.Add(time.Hour + 2*time.Minute + 3*time.Second), true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok, err := submitWithVP(t, tt.first, tt.vp).Expiry(received)
			if err != nil || ok != tt.ok || !got.Equal(tt.want) {
				t.Fatalf("Expiry = %v, %v, %v; want %v, %v", got, ok, err, tt.want, tt.ok)
			}
		})
	}
}

func TestExpiryInvalidAbsolute(t *testing.T) {
	_, _, err := submitWithVP(t, "19", "62313150403000").Expiry(time.Now())
	if !errors.Is(err, ErrUnsupportedValidityPeriod) {
		t.Fatalf("err = %v, want ErrUnsupportedValidityPeriod", err)
	}
}

func TestSingleShot(t *testing.T) {
	if !submitWithVP(t, "09", "40000000000000").SingleShot() {
		t.Fatal("single-shot bit not reported")
	}

	if submitWithVP(t, "09", "00000000000000").SingleShot() || submitWithVP(t, "11", "aa").SingleShot() {
		t.Fatal("single-shot reported without the bit")
	}
}

func TestDecodeSubmitUserDataLimit(t *testing.T) {
	ok := "01" + "00" + "0281" + "21" + "00" + "04" + "8c" + strings.Repeat("00", 140)
	if _, err := DecodeSubmit(mustHex(t, ok)); err != nil {
		t.Fatalf("140 octets rejected: %v", err)
	}

	tooLong := "01" + "00" + "0281" + "21" + "00" + "04" + "8d" + strings.Repeat("00", 141)
	if _, err := DecodeSubmit(mustHex(t, tooLong)); !errors.Is(err, ErrUserDataTooLong) {
		t.Fatalf("err = %v, want ErrUserDataTooLong", err)
	}

	septets := "01" + "00" + "0281" + "21" + "00" + "00" + "a1" + strings.Repeat("00", 141)
	if _, err := DecodeSubmit(mustHex(t, septets)); !errors.Is(err, ErrUserDataTooLong) {
		t.Fatalf("161 septets: err = %v, want ErrUserDataTooLong", err)
	}
}

func TestProtocolIdentifierHelpers(t *testing.T) {
	for _, pid := range []byte{0x41, 0x44, 0x47} {
		if !IsReplaceType(pid) {
			t.Errorf("IsReplaceType(0x%02x) = false", pid)
		}
	}

	for _, pid := range []byte{0x00, 0x40, 0x48, 0x7f} {
		if IsReplaceType(pid) {
			t.Errorf("IsReplaceType(0x%02x) = true", pid)
		}
	}

	for _, pid := range []byte{0x21, 0x22, 0x3e} {
		if !RequestsTelematicInterworking(pid) {
			t.Errorf("RequestsTelematicInterworking(0x%02x) = false", pid)
		}
	}

	for _, pid := range []byte{0x00, 0x1f, 0x3f, 0x41} {
		if RequestsTelematicInterworking(pid) {
			t.Errorf("RequestsTelematicInterworking(0x%02x) = true", pid)
		}
	}
}
