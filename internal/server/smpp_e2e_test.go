package server_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/data"
	"github.com/linxGnu/gosmpp/pdu"
)

// The ESME of these tests, a gosmpp client (Apache-2.0, a test-only dependency).
const (
	esmeSystemID = "ota"
	esmePassword = "secret"
)

// startSMPPSMSC starts an SMSC with an SMPP listener, connected to a core that serves bob, and an ESME owned by a
// Submitter user.
func startSMPPSMSC(t *testing.T, edit func(*smscConfig)) (*fakeCore, *smsc) {
	t.Helper()

	core := newFakeCore(t, alice, bob)
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.SMPP = &config.SMPP{Address: loopback}

	if edit != nil {
		edit(&cfg)
	}

	s := startSMSC(t, cfg)
	core.connect(s)

	if code := s.api(t, http.MethodPost, "/api/v1/users", api.CreateUserParams{
		Email: "ota@example.com", Password: "correct horse", RoleID: db.RoleSubmitter,
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/users = %d", code)
	}

	esme := api.CreateESMEParams{
		SystemID: esmeSystemID, User: "ota@example.com", Password: ptrTo(esmePassword),
		AllowedNetworks: []string{"127.0.0.0/8"},
	}

	if code := s.api(t, http.MethodPost, "/api/v1/esmes", esme, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/esmes = %d", code)
	}

	return core, s
}

func ptrTo[T any](v T) *T { return &v }

// esmeClient is a bound gosmpp session and the PDUs it receives.
type esmeClient struct {
	session  *gosmpp.Session
	received chan pdu.PDU
}

func bindESME(t *testing.T, s *smsc, connector func(gosmpp.Dialer, gosmpp.Auth) gosmpp.Connector, dialer gosmpp.Dialer) *esmeClient {
	t.Helper()

	c := &esmeClient{received: make(chan pdu.PDU, 32)}

	session, err := gosmpp.NewSession(
		connector(dialer, gosmpp.Auth{SMSC: s.server.SMPPAddr().String(), SystemID: esmeSystemID, Password: esmePassword}),
		gosmpp.Settings{
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 5 * time.Second,
			// gosmpp answers deliver_sm itself with ESME_ROK.
			OnPDU: func(p pdu.PDU, _ bool) { c.received <- p },
		}, -1)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	c.session = session

	t.Cleanup(func() { _ = session.Close() })

	return c
}

func trx(d gosmpp.Dialer, a gosmpp.Auth) gosmpp.Connector { return gosmpp.TRXConnector(d, a) }

func tx(d gosmpp.Dialer, a gosmpp.Auth) gosmpp.Connector { return gosmpp.TXConnector(d, a) }

func rx(d gosmpp.Dialer, a gosmpp.Auth) gosmpp.Connector { return gosmpp.RXConnector(d, a) }

// next returns the next PDU of type T, skipping others.
func next[T pdu.PDU](t *testing.T, c *esmeClient) T {
	t.Helper()

	timeout := time.After(waitTimeout)

	for {
		select {
		case p := <-c.received:
			if v, ok := p.(T); ok {
				return v
			}
		case <-timeout:
			var zero T

			t.Fatalf("no %T received", zero)

			return zero
		}
	}
}

func shortMessage(t *testing.T, d *pdu.DeliverSM) []byte {
	t.Helper()

	ud, err := d.Message.GetMessageData()
	if err != nil {
		t.Fatal(err)
	}

	udh, err := d.Message.UDH().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	return append(udh, ud...)
}

// otaSubmit is a submit_sm of a (U)SIM data download message to bob, as smpp-ota-tool.py sends it: esm_class 0x40,
// PID 0x7F, data_coding 0x02, and a command packet after the UDH 027000 (TS 31.115 clause 4.1).
func otaSubmit(t *testing.T, registeredDelivery byte) *pdu.SubmitSM {
	t.Helper()

	sm := pdu.NewSubmitSM().(*pdu.SubmitSM)
	sm.SourceAddr = address(t, "15550001000")
	sm.DestAddr = address(t, bob.msisdn)
	sm.EsmClass, sm.ProtocolID, sm.RegisteredDelivery = 0x40, 0x7f, registeredDelivery

	msg, err := pdu.NewBinaryShortMessageWithEncoding([]byte{0x00, 0x11, 0x22, 0x33}, data.BINARY8BIT1)
	if err != nil {
		t.Fatal(err)
	}

	msg.SetUDH(pdu.UDH{{ID: 0x70, Data: []byte{}}})
	sm.Message = msg

	return sm
}

func address(t *testing.T, digits string) pdu.Address {
	t.Helper()

	a, err := pdu.NewAddressWithTonNpiAddr(1, 1, digits)
	if err != nil {
		t.Fatal(err)
	}

	return a
}

func submitted(t *testing.T, c *esmeClient, sm *pdu.SubmitSM) int64 {
	t.Helper()

	if err := c.session.Transmitter().Submit(sm); err != nil {
		t.Fatal(err)
	}

	resp := next[*pdu.SubmitSMResp](t, c)
	if resp.CommandStatus != data.ESME_ROK {
		t.Fatalf("submit_sm_resp %s", resp.CommandStatus)
	}

	id, err := strconv.ParseInt(resp.MessageID, 10, 64)
	if err != nil {
		t.Fatalf("message_id %q", resp.MessageID)
	}

	return id
}

func receiptText(id int64) string { return "id:" + strconv.FormatInt(id, 10) + " sub:001 " }

// TestSMPPSIMDataDownload binds a transceiver, submits a (U)SIM data download message that asks for a receipt and a
// user acknowledgement (registered_delivery 0x09), and gets the (U)SIM's answer from the RP-ACK, then the receipt.
func TestSMPPSIMDataDownload(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)

	// The Response Packet on an RP-ACK, without TP-UDHI (TS 31.115 clause 4.1).
	core.setAnswerMT(func(string) *diameter.Message {
		return &diameter.Message{AVPs: []diameter.AVP{
			diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
			diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "00077ff60702710003aabbcc")),
		}}
	})

	esme := bindESME(t, s, trx, gosmpp.NonTLSDialer)
	id := submitted(t, esme, otaSubmit(t, 0x09))

	// The SMS-DELIVER: TP-UDHI, TP-PID 0x7F, TP-DCS 0xF6 and the TP-UD as submitted.
	eventually(t, "the SMS-DELIVER", func() bool { return len(core.forwardedTo(bob.imsi)) == 1 })

	deliver := core.forwardedTo(bob.imsi)[0].tpdu
	if deliver[0]&0x40 == 0 || !bytes.Contains(deliver, mustHex(t, "7ff6")) ||
		!bytes.HasSuffix(deliver, mustHex(t, "07027000"+"00112233")) {
		t.Fatalf("SMS-DELIVER %x", deliver)
	}

	answer := next[*pdu.DeliverSM](t, esme)
	if answer.EsmClass != 0x50 || answer.ProtocolID != 0x7f || answer.Message.Encoding().DataCoding() != 0x02 ||
		hex.EncodeToString(shortMessage(t, answer)) != "02710003aabbcc" ||
		!bytes.Equal(answer.OptionalParameters[pdu.TagReceiptedMessageID].Data, []byte(strconv.FormatInt(id, 10)+"\x00")) {
		t.Fatalf("answer esm_class %#x pid %#x data_coding %#x short_message %x", answer.EsmClass, answer.ProtocolID,
			answer.Message.Encoding().DataCoding(), shortMessage(t, answer))
	}

	receipt := next[*pdu.DeliverSM](t, esme)
	text, _ := receipt.Message.GetMessage()

	if receipt.EsmClass != 0x04 || !strings.HasPrefix(text, receiptText(id)+"dlvrd:001 ") ||
		!strings.Contains(text, " stat:DELIVRD err:000 text:") {
		t.Fatalf("receipt esm_class %#x %q", receipt.EsmClass, text)
	}
}

// TestSMPPSIMDataDownloadError answers RP-ERROR with TP-FCS 0xD5 and TP-UD: the answer is forwarded, and the receipt
// is UNDELIV err:066 after one attempt.
func TestSMPPSIMDataDownloadError(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)

	core.setAnswerMT(func(string) *diameter.Message {
		return experimental(tgpp.ResultErrorSMDeliveryFailure,
			diameter.Grouped(sgd.AVPSMDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.Unsigned32(sgd.AVPSMEnumeratedDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID,
					uint32(sgd.CauseEquipmentProtocolError)),
				diameter.OctetString(sgd.AVPSMDiagnosticInfo, diameter.AVPFlagMandatory, tgpp.VendorID, mustHex(t, "00d5077ff6060271000102ff")),
			))
	})

	esme := bindESME(t, s, trx, gosmpp.NonTLSDialer)
	id := submitted(t, esme, otaSubmit(t, 0x09))

	answer := next[*pdu.DeliverSM](t, esme)
	if answer.EsmClass != 0x50 || hex.EncodeToString(shortMessage(t, answer)) != "0271000102ff" {
		t.Fatalf("answer esm_class %#x short_message %x", answer.EsmClass, shortMessage(t, answer))
	}

	receipt := next[*pdu.DeliverSM](t, esme)
	if text, _ := receipt.Message.GetMessage(); !strings.HasPrefix(text, receiptText(id)+"dlvrd:000 ") ||
		!strings.Contains(text, " stat:UNDELIV err:066 text:") {
		t.Fatalf("receipt %q", text)
	}

	if n := len(core.forwardedTo(bob.imsi)); n != 1 {
		t.Fatalf("%d MT forwards, want 1", n)
	}
}

// TestSMPPNotificationsWaitForAReceiver submits on a transmitter: the receipt waits until a receiver binds.
func TestSMPPNotificationsWaitForAReceiver(t *testing.T) {
	_, s := startSMPPSMSC(t, nil)

	transmitter := bindESME(t, s, tx, gosmpp.NonTLSDialer)
	id := submitted(t, transmitter, otaSubmit(t, 0x01))
	s.waitForStatus(t, id, db.StatusDelivered)

	receiver := bindESME(t, s, rx, gosmpp.NonTLSDialer)

	receipt := next[*pdu.DeliverSM](t, receiver)
	if text, _ := receipt.Message.GetMessage(); !strings.HasPrefix(text, receiptText(id)) {
		t.Fatalf("receipt %q", text)
	}
}

// TestSMPPOverTLS binds over TLS with a self-signed certificate.
func TestSMPPOverTLS(t *testing.T) {
	cert, key := writeCertificate(t, t.TempDir())

	_, s := startSMPPSMSC(t, func(c *smscConfig) { c.SMPP.TLS = &config.TLS{Cert: cert, Key: key} })

	pem, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)

	dialer := func(addr string) (net.Conn, error) {
		d := tls.Dialer{Config: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
		return d.DialContext(context.Background(), "tcp", addr)
	}

	esme := bindESME(t, s, trx, dialer)
	submitted(t, esme, otaSubmit(t, 0))

	// A plain TCP bind to the TLS listener fails.
	if _, err := gosmpp.NewSession(gosmpp.TXConnector(gosmpp.NonTLSDialer, gosmpp.Auth{
		SMSC: s.server.SMPPAddr().String(), SystemID: esmeSystemID, Password: esmePassword,
	}), gosmpp.Settings{ReadTimeout: time.Second, WriteTimeout: time.Second}, -1); err == nil {
		t.Fatal("a plain TCP bind to the TLS listener succeeded")
	}
}

func TestSMPPListenerIsOptional(t *testing.T) {
	_, s := newSMSC(t)

	if addr := s.server.SMPPAddr(); addr != nil {
		t.Fatalf("SMPP listens on %s without an smpp block", addr)
	}
}

// TestSMPPRevocation checks that deleting the ESME closes its session.
func TestSMPPRevocation(t *testing.T) {
	_, s := startSMPPSMSC(t, nil)
	esme := bindESME(t, s, trx, gosmpp.NonTLSDialer)

	if code := s.api(t, http.MethodDelete, "/api/v1/esmes/"+esmeSystemID, nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE = %d", code)
	}

	eventually(t, "the session to close", func() bool {
		return esme.session.Transmitter().Submit(otaSubmit(t, 0)) != nil
	})
}

// TestSMPPAndAPIParity submits the same PID, DCS, UDH and user data over the API and as submit_sm: the stored
// SMS-SUBMITs are the same octets.
func TestSMPPAndAPIParity(t *testing.T) {
	_, s := startSMPPSMSC(t, nil)
	esme := bindESME(t, s, trx, gosmpp.NonTLSDialer)

	pid, dcs := uint8(0x7f), uint8(0xf6)
	sent := s.sendBinary(t, api.CreateMessageParams{From: "+15550001000", To: "+" + bob.msisdn, Binary: &api.BinaryParams{
		ProtocolIdentifier: &pid, DataCodingScheme: &dcs,
		UserDataHeader: api.Hex{0x02, 0x70, 0x00}, UserData: api.Hex{0x00, 0x11, 0x22, 0x33},
	}})

	// data_coding 0x02 with PID 0x7F is TP-DCS 0xF6 (spec_smpp.md §5.2).
	id := submitted(t, esme, otaSubmit(t, 0))

	viaAPI, err := s.store.GetMessage(context.Background(), sent[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	viaSMPP, err := s.store.GetMessage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(viaAPI.TPDU, viaSMPP.TPDU) {
		t.Fatalf("SMS-SUBMIT over the API %x, over SMPP %x", viaAPI.TPDU, viaSMPP.TPDU)
	}

	var m api.Message
	if code := s.api(t, http.MethodGet, "/api/v1/messages/"+strconv.FormatInt(id, 10), nil, &m); code != http.StatusOK ||
		m.SystemID != esmeSystemID || m.SubmittedBy != "ota@example.com" {
		t.Fatalf("GET message = %d %+v", code, m)
	}
}
