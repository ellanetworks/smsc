package server_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
)

// smsSubmitRequestingReport returns an SMS-SUBMIT of 8-bit text with TP-SRR set, and the validity period, if any, in
// the enhanced format.
func smsSubmitRequestingReport(t testing.TB, to subscriber, reference byte, text string, enhancedValidity []byte) []byte {
	t.Helper()

	first := byte(0x21)
	if enhancedValidity != nil {
		first |= 0x08
	}

	out := []byte{first, reference}
	out = append(out, semiOctets(t, to.msisdn)...)
	out = append(out, 0x00, 0x04)
	out = append(out, enhancedValidity...)
	out = append(out, byte(len(text)))

	return append(out, text...)
}

// statusReportTo waits for the SMS-STATUS-REPORT that the SC delivers to a subscriber.
func statusReportTo(t *testing.T, core *fakeCore, to subscriber) tpdu.StatusReport {
	t.Helper()

	eventually(t, "a status report to "+to.msisdn, func() bool { return len(core.deliveredTo(to.imsi)) == 1 })

	report, err := tpdu.DecodeStatusReport(core.deliveredTo(to.imsi)[0].tpdu)
	if err != nil {
		t.Fatalf("DecodeStatusReport: %v", err)
	}

	return report
}

// waitForReportDelivered waits for the status report on the first message to be delivered.
func waitForReportDelivered(t *testing.T, s *smsc) {
	t.Helper()

	eventually(t, "the status report delivered", func() bool {
		reports, err := s.store.ListStatusReports(context.Background(), 1)
		return err == nil && len(reports) == 1 && reports[0].Status == db.StatusDelivered
	})
}

func TestStatusReportIsReturnedToTheOriginator(t *testing.T) {
	core, s := newSMSC(t, alice, bob)

	if rc := core.submitTPDU(alice, smsSubmitRequestingReport(t, bob, 0x2a, "tell me", nil)); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	if got := core.deliveredTo(bob.imsi); len(got) != 1 || got[0].tpdu[0]&0x20 == 0 {
		t.Fatalf("SMS-DELIVER to bob = %+v; want TP-SRI set", got)
	}

	report := statusReportTo(t, core, alice)
	if report.MessageReference != 0x2a || report.Recipient.Digits != bob.msisdn || report.Status != tpdu.StatusReceived {
		t.Fatalf("SMS-STATUS-REPORT = %+v", report)
	}

	waitForReportDelivered(t, s)

	routings := core.routingDetails()
	if len(routings) != 2 {
		t.Fatalf("routing requests = %+v", routings)
	}

	if r := routings[0]; r.msisdn != bob.msisdn || r.mti != s6c.SMRPMTIDeliver || !bytes.Equal(r.smea, semiOctets(t, alice.msisdn)) {
		t.Fatalf("routing request for the message = %+v", r)
	}

	if r := routings[1]; r.msisdn != alice.msisdn || r.mti != s6c.SMRPMTIStatusReport || r.smea != nil {
		t.Fatalf("routing request for the report = %+v", r)
	}

	if messages, total, err := s.store.ListMessages(context.Background(), db.MessageFilter{}, 1, 10); err != nil || total != 1 {
		t.Fatalf("messages = %+v, %d, %v; want only the short message", messages, total, err)
	}

	// The API shows the report under the message, with the attempts to deliver it to alice.
	status := s.message(t, 1).StatusReport
	if !status.Requested || len(status.Reports) != 1 || status.Reports[0].TPStatusName != "received" ||
		status.Reports[0].Status != "delivered" {
		t.Fatalf("status_report = %+v", status)
	}

	var attempts api.ListAttemptsResponse

	path := fmt.Sprintf("/api/v1/messages/1/status_reports/%d/attempts", status.Reports[0].ID)
	if code := s.api(t, http.MethodGet, path, nil, &attempts); code != http.StatusOK || attempts.TotalCount != 2 {
		t.Fatalf("GET %s = %d, %+v; want the routing lookup and the delivery", path, code, attempts)
	}
}

func TestExpiredMessageIsReported(t *testing.T) {
	core, s := newSMSC(t, alice, bob)
	core.setAbsent(bob.msisdn, true)

	// Valid for one second, in the enhanced format.
	validity := []byte{0x02, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00}

	if rc := core.submitTPDU(alice, smsSubmitRequestingReport(t, bob, 0x05, "too late", validity)); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusExpired)

	report := statusReportTo(t, core, alice)
	if report.MessageReference != 0x05 || report.Status != tpdu.StatusValidityPeriodExpired {
		t.Fatalf("SMS-STATUS-REPORT = %+v", report)
	}

	if got := core.deliveredTo(bob.imsi); len(got) != 0 {
		t.Fatalf("delivered %d messages to an absent subscriber", len(got))
	}

	waitForReportDelivered(t, s)
}

func TestUnknownRecipientIsReported(t *testing.T) {
	core, s := newSMSC(t, alice)

	if rc := core.submitTPDU(alice, smsSubmitRequestingReport(t, carol, 0x06, "anyone?", nil)); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusFailed)

	if report := statusReportTo(t, core, alice); report.Status != tpdu.StatusNotObtainable {
		t.Fatalf("SMS-STATUS-REPORT = %+v", report)
	}

	waitForReportDelivered(t, s)
}

func TestInterimStatusReportWhileTheRecipientIsAbsent(t *testing.T) {
	core, s := newSMSC(t, alice, bob)
	core.setAbsent(bob.msisdn, true)

	// TP-SRR and TP-UDHI, with a Selective Status Report for every outcome, the SC still trying included.
	submit := []byte{0x61, 0x09}
	submit = append(submit, semiOctets(t, bob.msisdn)...)
	submit = append(submit, 0x00, 0x04, 0x07, 0x03, 0x06, 0x01, 0x0f, 'l', 'a', 't')

	if rc := core.submitTPDU(alice, submit); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	interim := statusReportTo(t, core, alice)
	if interim.MessageReference != 0x09 || interim.Status != tpdu.TemporaryNoResponse.StillTrying() {
		t.Fatalf("interim SMS-STATUS-REPORT = %+v", interim)
	}

	eventually(t, "the interim report delivered", func() bool {
		reports, err := s.store.ListStatusReports(context.Background(), 1)
		return err == nil && len(reports) == 1 && reports[0].Status == db.StatusDelivered
	})

	core.setAbsent(bob.msisdn, false)

	if rc := core.alert(bob); rc != diameter.ResultSuccess {
		t.Fatalf("ALA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	eventually(t, "the final status report to alice", func() bool { return len(core.deliveredTo(alice.imsi)) == 2 })

	final, err := tpdu.DecodeStatusReport(core.deliveredTo(alice.imsi)[1].tpdu)
	if err != nil || final.MessageReference != 0x09 || final.Status != tpdu.StatusReceived {
		t.Fatalf("final SMS-STATUS-REPORT = %+v, %v", final, err)
	}
}
