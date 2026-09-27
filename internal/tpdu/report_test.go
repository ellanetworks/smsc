package tpdu

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestEncodeSubmitReportError(t *testing.T) {
	got, err := EncodeSubmitReportError(FailureRejectedDuplicate, time.Date(2026, 9, 27, 15, 4, 5, 0, time.FixedZone("", -4*3600)))
	if err != nil {
		t.Fatalf("EncodeSubmitReportError: %v", err)
	}

	want := mustHex(t, "01"+"c5"+"00"+"62907251405069")
	if !bytes.Equal(got, want) {
		t.Fatalf("report = %x, want %x", got, want)
	}
}

func TestEncodeSubmitReportErrorRejectsOddZone(t *testing.T) {
	if _, err := EncodeSubmitReportError(FailureUnspecified, time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 600))); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMessageType(t *testing.T) {
	if mti, ok := MessageType([]byte{0x02}); !ok || mti != MessageTypeCommand {
		t.Fatalf("MessageType = %d, %v", mti, ok)
	}

	if _, ok := MessageType(nil); ok {
		t.Fatal("expected no message type for an empty TPDU")
	}
}

func TestDecodeSubmitCommandIsUnsupportedType(t *testing.T) {
	_, err := DecodeSubmit(mustHex(t, "02"+"00"+"00"+"00"+"00"+"0281"+"21"+"00"))
	if !errors.Is(err, ErrUnsupportedMessageType) {
		t.Fatalf("err = %v, want ErrUnsupportedMessageType", err)
	}
}

func TestDecodeSubmitEnhancedValidityPeriod(t *testing.T) {
	tests := map[string]struct {
		vp      string
		wantErr bool
	}{
		"none":            {"00000000000000", false},
		"relative":        {"01aa0000000000", false},
		"seconds":         {"021e0000000000", false},
		"hh:mm:ss":        {"03001000000000", false},
		"zero seconds":    {"02000000000000", true},
		"reserved format": {"04000000000000", true},
		"extended":        {"81aa0000000000", true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeSubmit(mustHex(t, "09"+"00"+"0281"+"21"+"00"+"00"+tt.vp+"00"))
			if tt.wantErr != errors.Is(err, ErrUnsupportedValidityPeriod) || (!tt.wantErr && err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
