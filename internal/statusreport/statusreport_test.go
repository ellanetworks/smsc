package statusreport

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

var (
	submitted = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	discharge = submitted.Add(time.Minute)
	current   = settings.Settings{
		Operator: settings.Operator{ServiceCentreAddress: "15550000000"},
		Delivery: settings.Delivery{DefaultValidity: 24 * time.Hour},
	}
)

// submitTPDU is an SMS-SUBMIT with TP-SRR, TP-MR 7, to +15551230002.
const submitTPDU = "21" + "07" + "0b91" + "5155210300f2" + "00" + "00" + "05" + "e8329bfd06"

// permanentOnlyTPDU is an SMS-SUBMIT with TP-SRR and a Selective Status Report for permanent errors only.
const permanentOnlyTPDU = "61" + "07" + "0b91" + "5155210300f2" + "00" + "04" + "04" + "03" + "060102"

func message(t *testing.T, tpduHex string) db.Message {
	t.Helper()

	b, err := hex.DecodeString(tpduHex)
	if err != nil {
		t.Fatal(err)
	}

	return db.Message{ID: 1, Kind: db.KindMessage, Origin: db.OriginMobile, TPDU: b, SubmittedAt: submitted}
}

func TestFor(t *testing.T) {
	report, err := For(message(t, submitTPDU), tpdu.StatusValidityPeriodExpired, discharge, current)
	if err != nil || report == nil {
		t.Fatalf("For = %+v, %v", report, err)
	}

	sc := db.Address{Digits: "15550000000", TypeOfNumber: 1, NumberingPlan: 1}
	if report.Originator != sc || report.Status != 0x46 || !report.CreatedAt.Equal(discharge) ||
		!report.ExpiresAt.Equal(discharge.Add(24*time.Hour)) {
		t.Fatalf("report = %+v", report)
	}

	r, err := tpdu.DecodeStatusReport(report.TPDU)
	if err != nil {
		t.Fatal(err)
	}

	recipient := tpdu.Address{TypeOfNumber: 1, NumberingPlan: 1, Digits: "15551230002"}
	if r.MessageReference != 7 || r.Recipient != recipient || r.Status != tpdu.StatusValidityPeriodExpired ||
		!r.ServiceCentreTimeStamp.Equal(submitted) || !r.DischargeTime.Equal(discharge) || r.MoreMessagesToSend {
		t.Fatalf("SMS-STATUS-REPORT = %+v", r)
	}
}

func TestForWantsNone(t *testing.T) {
	fromAPI := message(t, submitTPDU)
	fromAPI.Origin = db.OriginAPI

	report := message(t, submitTPDU)
	report.Kind = db.KindStatusReport

	tests := map[string]struct {
		message db.Message
		status  tpdu.Status
	}{
		"not requested":           {message(t, "01"+submitTPDU[2:]), tpdu.StatusReceived},
		"from the API":            {fromAPI, tpdu.StatusReceived},
		"on a status report":      {report, tpdu.StatusReceived},
		"while still trying":      {message(t, submitTPDU), tpdu.TemporaryNoResponse.StillTrying()},
		"completion not selected": {message(t, permanentOnlyTPDU), tpdu.StatusReceived},
		"given up not selected":   {message(t, permanentOnlyTPDU), tpdu.TemporarySMEBusy.GaveUp()},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if report, err := For(tt.message, tt.status, discharge, current); err != nil || report != nil {
				t.Fatalf("For = %+v, %v; want no report", report, err)
			}
		})
	}
}

// stillTryingTPDU is an SMS-SUBMIT with TP-SRR and a Selective Status Report for every outcome, the SC still trying
// included.
const stillTryingTPDU = "61" + "07" + "0b91" + "5155210300f2" + "00" + "04" + "04" + "03" + "06010f"

func TestForSelectedOutcome(t *testing.T) {
	tests := map[string]struct {
		tpdu   string
		status tpdu.Status
	}{
		"permanent error": {permanentOnlyTPDU, tpdu.StatusNotObtainable},
		"still trying":    {stillTryingTPDU, tpdu.TemporaryNoResponse.StillTrying()},
		"completed":       {stillTryingTPDU, tpdu.StatusReceived},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			report, err := For(message(t, tt.tpdu), tt.status, discharge, current)
			if err != nil || report == nil || report.Status != uint8(tt.status) {
				t.Fatalf("For = %+v, %v; want a report with status %#x", report, err, byte(tt.status))
			}

			if r, err := tpdu.DecodeStatusReport(report.TPDU); err != nil || r.Status != tt.status || !r.DischargeTime.Equal(discharge) {
				t.Fatalf("SMS-STATUS-REPORT = %+v, %v", r, err)
			}
		})
	}
}

func TestForUndecodableMessage(t *testing.T) {
	if _, err := For(message(t, "00"), tpdu.StatusReceived, discharge, current); err == nil {
		t.Fatal("expected an error")
	}
}
