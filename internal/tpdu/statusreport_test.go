package tpdu

import (
	"bytes"
	"encoding/hex"
	"errors"
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

func ptr[T any](v T) *T {
	return &v
}

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

	withPID := base
	withPID.ProtocolIdentifier = ptr(byte(0x41))

	withUserData := base
	withUserData.ProtocolIdentifier = ptr(byte(0x00))
	withUserData.DataCodingScheme = ptr(byte(dcs8Bit))
	withUserData.UserData = &UserData{Header: true, Length: 6, Data: mustHex(t, "05"+"070101"+"0400")}

	gsm7 := base
	gsm7.UserData = &UserData{Length: 5, Data: mustHex(t, "e8329bfd06")}

	tests := map[string]struct {
		report StatusReport
		want   string
	}{
		"mandatory only":         {base, "06" + "07" + reportMandatory + "00"},
		"more messages to send":  {more, "02" + "07" + reportMandatory + "62"},
		"protocol identifier":    {withPID, "06" + "07" + reportMandatory + "00" + "01" + "41"},
		"user data header":       {withUserData, "46" + "07" + reportMandatory + "00" + "07" + "00" + "04" + "06" + "05070101" + "0400"},
		"default alphabet in UD": {gsm7, "06" + "07" + reportMandatory + "00" + "04" + "05" + "e8329bfd06"},
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

	mismatch := base
	mismatch.DataCodingScheme = ptr(byte(dcs8Bit))
	mismatch.UserData = &UserData{Length: 5, Data: []byte{1}}

	tooLong := base
	tooLong.DataCodingScheme = ptr(byte(dcs8Bit))
	tooLong.UserData = &UserData{Length: 140, Data: make([]byte, 140)}

	alphanumeric := base
	alphanumeric.Recipient = Address{TypeOfNumber: TypeOfNumberAlphanumeric, Digits: "1"}

	oddZone := base
	oddZone.DischargeTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 600))

	for name, r := range map[string]StatusReport{
		"user data length mismatch": mismatch,
		"alphanumeric recipient":    alphanumeric,
		"odd time zone":             oddZone,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Encode(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	if _, err := tooLong.Encode(); !errors.Is(err, ErrStatusReportTooLong) {
		t.Fatalf("err = %v, want ErrStatusReportTooLong", err)
	}
}

func TestDecodeStatusReport(t *testing.T) {
	t.Run("extension octet of TP-PI is skipped", func(t *testing.T) {
		r, err := DecodeStatusReport(mustHex(t, "06"+"07"+reportMandatory+"43"+"81"+"00"+"41"+"00"))
		if err != nil {
			t.Fatalf("DecodeStatusReport: %v", err)
		}

		if r.Status != Status(0x43) || r.ProtocolIdentifier == nil || *r.ProtocolIdentifier != 0x41 || r.UserData != nil {
			t.Fatalf("report = %+v", r)
		}
	})

	t.Run("octets after TP-UD are discarded", func(t *testing.T) {
		r, err := DecodeStatusReport(mustHex(t, "06"+"07"+reportMandatory+"00"+"06"+"04"+"02"+"abcd"+"ff"))
		if err != nil || r.UserData == nil || !bytes.Equal(r.UserData.Data, []byte{0xab, 0xcd}) {
			t.Fatalf("report = %+v, %v", r, err)
		}
	})

	for name, in := range map[string]string{
		"empty":               "",
		"sms-deliver":         "04" + "07" + reportMandatory + "00",
		"truncated address":   "06" + "07" + "0b91" + "5155",
		"truncated times":     "06" + "07" + reportRA + reportSCTS,
		"truncated extension": "06" + "07" + reportMandatory + "00" + "80",
		"missing TP-PID":      "06" + "07" + reportMandatory + "00" + "01",
		"truncated user data": "06" + "07" + reportMandatory + "00" + "06" + "04" + "05" + "abcd",
		"invalid time stamp":  "06" + "07" + reportRA + "629072514050ff" + reportDT + "00",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeStatusReport(mustHex(t, in)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestStatusClasses(t *testing.T) {
	tests := map[Status]StatusClass{
		StatusReceived:                    ClassCompleted,
		StatusReplaced:                    ClassCompleted,
		TemporaryCongestion.StillTrying(): ClassStillTrying,
		TemporaryErrorInSME.StillTrying(): ClassStillTrying,
		StatusRemoteProcedureError:        ClassPermanent,
		StatusValidityPeriodExpired:       ClassPermanent,
		TemporarySMEBusy.GaveUp():         ClassGaveUp,
		Status(0x80):                      ClassGaveUp,
	}

	for status, want := range tests {
		if got := status.Class(); got != want {
			t.Errorf("Status(%#x).Class() = %d, want %d", byte(status), got, want)
		}
	}

	if TemporaryNoResponse.StillTrying() != 0x22 || TemporaryNoResponse.GaveUp() != 0x62 {
		t.Fatalf("no response = %#x, %#x", byte(TemporaryNoResponse.StillTrying()), byte(TemporaryNoResponse.GaveUp()))
	}
}

func TestStatusStillTrying(t *testing.T) {
	tests := map[Status]Status{
		TemporaryCongestion.GaveUp():   0x20,
		TemporaryNoResponse.GaveUp():   0x22,
		TemporaryErrorInSME.GaveUp():   0x25,
		Status(0x6f):                   0x2f,
		StatusReceived:                 StatusReceived,
		StatusValidityPeriodExpired:    StatusValidityPeriodExpired,
		TemporarySMEBusy.StillTrying(): 0x21,
		Status(0xe1):                   0xe1,
	}

	for status, want := range tests {
		if got := status.StillTrying(); got != want {
			t.Errorf("Status(%#x).StillTrying() = %#x, want %#x", byte(status), byte(got), byte(want))
		}
	}
}

// submitWithHeader returns an SMS-SUBMIT of 8-bit data whose user data header holds the information elements.
func submitWithHeader(srr bool, ies string) Submit {
	header := append([]byte{byte(len(ies) / 2)}, mustDecodeHex(ies)...)
	ud := append(header, 0xab, 0xcd)

	return Submit{
		StatusReportRequest: srr,
		UserDataHeader:      true,
		MessageReference:    0x07,
		Destination:         reportRecipient,
		ProtocolIdentifier:  0x00,
		DataCodingScheme:    dcs8Bit,
		UserDataLength:      uint8(len(ud)),
		UserData:            ud,
	}
}

func TestStatusReportsWanted(t *testing.T) {
	statuses := []Status{StatusReceived, StatusValidityPeriodExpired, TemporarySMEBusy.GaveUp(), TemporarySMEBusy.StillTrying()}

	tests := map[string]struct {
		submit Submit
		want   []bool
		udh    bool
	}{
		"no request":                         {Submit{}, []bool{false, false, false, false}, false},
		"request without selection":          {Submit{StatusReportRequest: true}, []bool{true, true, true, false}, false},
		"selection without request":          {submitWithHeader(false, "06018f"), []bool{false, false, false, false}, false},
		"completed only":                     {submitWithHeader(true, "060101"), []bool{true, false, false, false}, false},
		"permanent errors only":              {submitWithHeader(true, "060102"), []bool{false, true, false, false}, false},
		"given up only":                      {submitWithHeader(true, "060104"), []bool{false, false, true, false}, false},
		"still trying only":                  {submitWithHeader(true, "060108"), []bool{false, false, false, true}, false},
		"nothing, with the header":           {submitWithHeader(true, "060180"), []bool{false, false, false, false}, true},
		"selection after another element":    {submitWithHeader(true, "00032a0201"+"06010f"), []bool{true, true, true, true}, false},
		"malformed header ignores selection": {submitWithHeader(true, "060101"+"04"), []bool{true, true, true, false}, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r := tt.submit.StatusReports()

			for i, s := range statuses {
				if got := r.Wants(s); got != tt.want[i] {
					t.Errorf("Wants(%#x) = %v, want %v", byte(s), got, tt.want[i])
				}
			}

			if got := r.IncludesUserDataHeader(); got != tt.udh {
				t.Errorf("IncludesUserDataHeader = %v, want %v", got, tt.udh)
			}
		})
	}
}

func TestSubmitStatusReport(t *testing.T) {
	t.Run("without the header", func(t *testing.T) {
		s := submitWithHeader(true, "00032a0201")

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
	})

	t.Run("with the header", func(t *testing.T) {
		s := submitWithHeader(true, "00032a0201"+"060181")
		s.ProtocolIdentifier = 0x41

		b, err := s.StatusReport(reportTimeStamp, reportDischarge, StatusReceived).Encode()
		if err != nil {
			t.Fatal(err)
		}

		want := mustHex(t, "46"+"07"+reportMandatory+"00"+"07"+"41"+"04"+"0c"+"0b"+"070101"+"00032a0201"+"060181")
		if !bytes.Equal(b, want) {
			t.Fatalf("report = %x, want %x", b, want)
		}
	})

	t.Run("elements that do not fit are left out", func(t *testing.T) {
		// 131 octets of room for the original elements: these take 3, 102, then 32.
		ies := "060180" + "7064" + repeatHex("aa", 100) + "711e" + repeatHex("bb", 30)
		s := submitWithHeader(true, ies)

		r := s.StatusReport(reportTimeStamp, reportDischarge, StatusReceived)
		if r.UserData == nil || r.UserData.Length != 109 || r.UserData.Data[0] != 108 ||
			!bytes.Equal(r.UserData.Data[4:], mustDecodeHex("060180"+"7064"+repeatHex("aa", 100))) {
			t.Fatalf("user data = %+v", r.UserData)
		}

		if _, err := r.Encode(); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	})

	t.Run("elements fill the longest report", func(t *testing.T) {
		exact := submitWithHeader(true, "060180"+"707e"+repeatHex("aa", 126))

		r := exact.StatusReport(reportTimeStamp, reportDischarge, StatusReceived)
		if b, err := r.Encode(); err != nil || len(b) != maxStatusReportOctets {
			t.Fatalf("report of %d octets, %v; want %d", len(b), err, maxStatusReportOctets)
		}

		over := submitWithHeader(true, "060180"+"707f"+repeatHex("aa", 127))

		if r := over.StatusReport(reportTimeStamp, reportDischarge, StatusReceived); r.UserData == nil || r.UserData.Length != 7 {
			t.Fatalf("user data = %+v; want only the first element", r.UserData)
		}
	})

	t.Run("no report header when no element fits", func(t *testing.T) {
		s := submitWithHeader(true, "7084"+repeatHex("aa", 132)+"060180")

		r := s.StatusReport(reportTimeStamp, reportDischarge, StatusReceived)
		if r.UserData != nil || r.ProtocolIdentifier != nil || r.DataCodingScheme != nil {
			t.Fatalf("report = %+v", r)
		}
	})
}

func repeatHex(octet string, n int) string {
	return string(bytes.Repeat([]byte(octet), n))
}

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}

	return b
}

func TestStatusReportClasses(t *testing.T) {
	tests := map[string]struct {
		submit Submit
		want   []StatusClass
	}{
		"no request":                {Submit{}, nil},
		"request without selection": {Submit{StatusReportRequest: true}, []StatusClass{ClassCompleted, ClassPermanent, ClassGaveUp}},
		"every outcome":             {submitWithHeader(true, "06010f"), []StatusClass{ClassStillTrying, ClassCompleted, ClassPermanent, ClassGaveUp}},
		"permanent errors only":     {submitWithHeader(true, "060102"), []StatusClass{ClassPermanent}},
		"nothing":                   {submitWithHeader(true, "060100"), nil},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tt.submit.StatusReports().Classes(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Classes() = %v, want %v", got, tt.want)
			}
		})
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

	for class, want := range map[StatusClass]string{
		ClassCompleted: "completed", ClassStillTrying: "still_trying", ClassPermanent: "permanent", ClassGaveUp: "gave_up",
	} {
		if got := class.String(); got != want {
			t.Errorf("StatusClass(%d).String() = %q, want %q", class, got, want)
		}
	}
}
