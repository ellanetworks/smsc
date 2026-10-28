package server_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// simDataDownload is a (U)SIM data download message (TS 23.040 clause 9.2.3.9) to bob, whose user data is placeholder
// octets after a (U)SIM Toolkit Security Header IEI.
func simDataDownload() api.CreateMessageParams {
	pid, dcs := uint8(0x7f), uint8(0xf6)

	return api.CreateMessageParams{From: "+15550001111", To: "+" + bob.msisdn, Binary: &api.BinaryParams{
		ProtocolIdentifier: &pid, DataCodingScheme: &dcs,
		UserDataHeader: api.Hex{0x02, 0x70, 0x00}, UserData: api.Hex{0x00, 0x11, 0x22, 0x33, 0x44},
	}}
}

func (s *smsc) sendBinary(t *testing.T, params api.CreateMessageParams) []api.Message {
	t.Helper()

	var created api.CreateMessageResponse

	if code := s.api(t, http.MethodPost, "/api/v1/messages", params, &created); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/messages = %d", code)
	}

	return created.Items
}

func (s *smsc) content(t *testing.T, id int64) api.MessageContent {
	t.Helper()

	var c api.MessageContent

	if code := s.api(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/content", id), nil, &c); code != http.StatusOK {
		t.Fatalf("GET content of message %d = %d", id, code)
	}

	return c
}

// TestAPIBinaryMessageIsDelivered sends a (U)SIM data download message over the API, which reaches the phone with
// its TP-PID, TP-DCS, TP-UDH and TP-UD unchanged.
func TestAPIBinaryMessageIsDelivered(t *testing.T) {
	core := newFakeCore(t, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.sendBinary(t, simDataDownload())
	if len(sent) != 1 || sent[0].Encoding != "binary" {
		t.Fatalf("sent = %+v", sent)
	}

	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	got := core.deliveredTo(bob.imsi)
	if len(got) != 1 {
		t.Fatalf("delivered = %+v", got)
	}

	// TP-MTI SMS-DELIVER with TP-UDHI, TP-OA of 11 digits, TP-PID, TP-DCS, TP-SCTS, TP-UDL and TP-UD.
	tpdu := got[0].tpdu
	if len(tpdu) != 27 || tpdu[0] != 0x44 || !bytes.Equal(tpdu[1:9], mustHex(t, "0b915155001011f1")) ||
		!bytes.Equal(tpdu[9:11], mustHex(t, "7ff6")) || !bytes.Equal(tpdu[18:], mustHex(t, "080270000011223344")) {
		t.Fatalf("SMS-DELIVER = %x", tpdu)
	}

	c := s.content(t, sent[0].ID)
	if c.Binary == nil || c.Binary.ProtocolIdentifier != 0x7f || c.Binary.DataCodingScheme != 0xf6 ||
		!bytes.Equal(c.Binary.UserDataHeader, mustHex(t, "027000")) || !bytes.Equal(c.Binary.UserData, mustHex(t, "0011223344")) ||
		c.DeliverReports != nil {
		t.Fatalf("content = %+v", c)
	}
}

// TestSIMDataDownloadAnswerIsKept keeps the SMS-DELIVER-REPORT for RP-ACK that the serving node passes on, which
// carries what the (U)SIM answered (TS 29.338 clause 6.3.2.6).
func TestSIMDataDownloadAnswerIsKept(t *testing.T) {
	core := newFakeCore(t, bob)
	core.setAnswerMT(func(string) *diameter.Message {
		return &diameter.Message{AVPs: []diameter.AVP{
			diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
			diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "00077ff602cafe")),
		}}
	})

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.sendBinary(t, simDataDownload())
	s.waitForAPIStatus(t, sent[0].ID, "delivered")

	reports := s.content(t, sent[0].ID).DeliverReports
	if len(reports) != 1 || !bytes.Equal(reports[0].TPDU, mustHex(t, "00077ff602cafe")) ||
		!bytes.Equal(reports[0].UserData, mustHex(t, "cafe")) || reports[0].AttemptID != s.attempts(t, sent[0].ID)[1].ID {
		t.Fatalf("deliver reports = %+v", reports)
	}
}

// TestSIMDataDownloadErrorIsNotRetried fails a message that the (U)SIM refused with a (U)SIM data download error
// (TS 23.040 clause 9.2.3.22): sending it again would get the same answer.
func TestSIMDataDownloadErrorIsNotRetried(t *testing.T) {
	core := newFakeCore(t, bob)
	core.setAnswerMT(func(string) *diameter.Message {
		return experimental(tgpp.ResultErrorSMDeliveryFailure,
			diameter.Grouped(sgd.AVPSMDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.Unsigned32(sgd.AVPSMEnumeratedDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID, uint32(sgd.CauseEquipmentProtocolError)),
				diameter.OctetString(sgd.AVPSMDiagnosticInfo, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "00d5077ff602cafe")),
			))
	})

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.sendBinary(t, simDataDownload())
	s.waitForAPIStatus(t, sent[0].ID, "failed")

	attempts := s.attempts(t, sent[0].ID)
	if len(attempts) != 2 || attempts[1].Outcome != "sm_delivery_failure" || attempts[1].TPFailureCause != "usim_data_download_error" {
		t.Fatalf("attempts = %+v", attempts)
	}

	reports := s.content(t, sent[0].ID).DeliverReports
	if len(reports) != 1 || reports[0].AttemptID != attempts[1].ID || !bytes.Equal(reports[0].UserData, mustHex(t, "cafe")) {
		t.Fatalf("deliver reports = %+v", reports)
	}
}

// TestMobileOriginatedBinaryMessageIsDelivered passes a (U)SIM data download message from a phone on unchanged.
func TestMobileOriginatedBinaryMessageIsDelivered(t *testing.T) {
	core, s := newSMSC(t, alice, bob)

	submit := mustHex(t, "41"+"00"+"0b91"+"5155210300f2"+"7f"+"f6"+"08"+"027000"+"0011223344")
	if rc := core.submitTPDU(alice, submit); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	got := core.deliveredTo(bob.imsi)
	if len(got) != 1 {
		t.Fatalf("delivered = %+v", got)
	}

	tpdu := got[0].tpdu
	if tpdu[0] != 0x44 || !bytes.Equal(tpdu[1:9], mustHex(t, "0b915155210300f1")) || !bytes.Equal(tpdu[9:11], mustHex(t, "7ff6")) ||
		!bytes.Equal(tpdu[18:], mustHex(t, "080270000011223344")) {
		t.Fatalf("SMS-DELIVER = %x", tpdu)
	}
}
