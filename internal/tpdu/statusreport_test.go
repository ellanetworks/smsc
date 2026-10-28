package tpdu

import (
	"bytes"
	"reflect"
	"testing"
	"time"
)

var (
	reportZone      = time.FixedZone("", -4*3600)
	reportTimeStamp = time.Date(2026, 9, 27, 15, 4, 5, 0, reportZone)
	reportDischarge = time.Date(2026, 9, 27, 15, 5, 10, 0, reportZone)
	reportRecipient = Address{TypeOfNumber: TypeOfNumberInternational, NumberingPlan: NumberingPlanISDN, Digits: "15551230002"}
)

const (
	reportRA        = "0b91" + "5155210300f2"
	reportSCTS      = "62907251405069"
	reportDT        = "62907251500169"
	reportMandatory = reportRA + reportSCTS + reportDT
)

func TestStatusReportEncode(t *testing.T) {
	base := StatusReport{
		MessageReference:       0x07,
		Recipient:              reportRecipient,
		ServiceCentreTimeStamp: reportTimeStamp,
		DischargeTime:          reportDischarge,
		Status:                 StatusReceived,
	}

	more := base
	more.MoreMessagesToSend = true
	more.Status = TemporaryNoResponse.GaveUp()

	tests := map[string]struct {
		report StatusReport
		want   string
	}{
		"no more messages":      {base, "06" + "07" + reportMandatory + "00"},
		"more messages to send": {more, "02" + "07" + reportMandatory + "62"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tt.report.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			if want := mustHex(t, tt.want); !bytes.Equal(got, want) {
				t.Fatalf("Encode = %x, want %x", got, want)
			}

			decoded, err := DecodeStatusReport(got)
			if err != nil {
				t.Fatalf("DecodeStatusReport: %v", err)
			}

			if !decoded.ServiceCentreTimeStamp.Equal(tt.report.ServiceCentreTimeStamp) ||
				!decoded.DischargeTime.Equal(tt.report.DischargeTime) {
				t.Fatalf("times = %v, %v", decoded.ServiceCentreTimeStamp, decoded.DischargeTime)
			}

			decoded.ServiceCentreTimeStamp, decoded.DischargeTime = tt.report.ServiceCentreTimeStamp, tt.report.DischargeTime
			if !reflect.DeepEqual(decoded, tt.report) {
				t.Fatalf("round trip = %+v, want %+v", decoded, tt.report)
			}
		})
	}
}

func TestStatusReportEncodeErrors(t *testing.T) {
	base := StatusReport{Recipient: reportRecipient, ServiceCentreTimeStamp: reportTimeStamp, DischargeTime: reportDischarge}

	alphanumeric := base
	alphanumeric.Recipient = Address{TypeOfNumber: TypeOfNumberAlphanumeric, NumberingPlan: NumberingPlanISDN, Digits: "1"}

	oddZone := base
	oddZone.DischargeTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 600))

	for name, r := range map[string]StatusReport{
		"alphanumeric with ISDN plan": alphanumeric,
		"odd time zone":               oddZone,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Encode(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSetMoreMessagesToSend(t *testing.T) {
	report := mustHex(t, "06"+"07"+reportMandatory+"00")

	more := SetMoreMessagesToSend(report, true)
	if !bytes.Equal(more, mustHex(t, "02"+"07"+reportMandatory+"00")) || report[0] != 0x06 {
		t.Fatalf("more = %x, report = %x; want TP-MMS cleared on a copy", more, report)
	}

	if last := SetMoreMessagesToSend(more, false); !bytes.Equal(last, report) {
		t.Fatalf("last = %x, want %x", last, report)
	}
}

func TestDecodeStatusReport(t *testing.T) {
	// The optional parameters that follow TP-ST are ignored.
	r, err := DecodeStatusReport(mustHex(t, "46"+"07"+reportMandatory+"43"+"07"+"00"+"04"+"02"+"abcd"))
	if err != nil || r.Status != Status(0x43) || r.MessageReference != 0x07 || r.Recipient != reportRecipient {
		t.Fatalf("report = %+v, %v", r, err)
	}

	for name, in := range map[string]string{
		"empty":              "",
		"sms-deliver":        "04" + "07" + reportMandatory + "00",
		"truncated address":  "06" + "07" + "0b91" + "5155",
		"truncated times":    "06" + "07" + reportRA + reportSCTS,
		"missing status":     "06" + "07" + reportMandatory,
		"invalid time stamp": "06" + "07" + reportRA + "629072514050ff" + reportDT + "00",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStatusReport(mustHex(t, in)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestSubmitStatusReport(t *testing.T) {
	s := Submit{StatusReportRequest: true, MessageReference: 0x07, Destination: reportRecipient}

	got := s.StatusReport(reportTimeStamp, reportDischarge, StatusValidityPeriodExpired)
	want := StatusReport{
		MessageReference:       0x07,
		Recipient:              reportRecipient,
		ServiceCentreTimeStamp: reportTimeStamp,
		DischargeTime:          reportDischarge,
		Status:                 StatusValidityPeriodExpired,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report = %+v, want %+v", got, want)
	}
}

func TestTemporaryErrorGaveUp(t *testing.T) {
	if TemporaryNoResponse.GaveUp() != 0x62 || TemporaryErrorInSME.GaveUp() != 0x65 {
		t.Fatalf("gave up = %#x, %#x", byte(TemporaryNoResponse.GaveUp()), byte(TemporaryErrorInSME.GaveUp()))
	}
}

func TestStatusNames(t *testing.T) {
	tests := map[Status]string{
		0x00: "received",
		0x01: "forwarded_unconfirmed",
		0x02: "replaced",
		0x03: "reserved",
		0x10: "sc_specific",
		0x22: "no_response",
		0x25: "error_in_sme",
		0x26: "reserved",
		0x30: "sc_specific",
		0x43: "not_obtainable",
		0x44: "quality_of_service_not_available",
		0x46: "validity_period_expired",
		0x49: "does_not_exist",
		0x4a: "reserved",
		0x5f: "sc_specific",
		0x62: "no_response",
		0x64: "quality_of_service_not_available",
		0x69: "reserved",
		0x6f: "reserved",
		0x70: "sc_specific",
		0x80: "reserved",
		0xff: "reserved",
	}

	for status, want := range tests {
		if got := status.String(); got != want {
			t.Errorf("Status(%#x).String() = %q, want %q", byte(status), got, want)
		}
	}
}
