package server_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/linxGnu/gosmpp"
	"github.com/linxGnu/gosmpp/pdu"
)

func shortCode(digits string) subscriber {
	return subscriber{imsi: "000000000000001", msisdn: digits}
}

func dialled(submit []byte) []byte {
	submit[3] = 0x81

	return submit
}

func addRoutes(t *testing.T, s *smsc, routes []string) {
	t.Helper()

	if code := s.api(t, http.MethodPut, "/api/v1/esmes/"+esmeSystemID, api.UpdateESMEParams{
		AllowedNetworks: []string{"127.0.0.0/8"}, Routes: routes,
	}, nil); code != http.StatusOK {
		t.Fatalf("PUT /api/v1/esmes/%s = %d", esmeSystemID, code)
	}
}

func bindESMEAs(t *testing.T, s *smsc, connector func(gosmpp.Dialer, gosmpp.Auth) gosmpp.Connector, dialer gosmpp.Dialer,
	systemID, password string,
) *esmeClient {
	t.Helper()

	c := &esmeClient{received: make(chan pdu.PDU, 32)}

	session, err := gosmpp.NewSession(
		connector(dialer, gosmpp.Auth{SMSC: s.server.SMPPAddr().String(), SystemID: systemID, Password: password}),
		gosmpp.Settings{
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 5 * time.Second,
			OnPDU:        func(p pdu.PDU, _ bool) { c.received <- p },
		}, -1)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	c.session = session

	t.Cleanup(func() { _ = session.Close() })

	return c
}

func TestMobileOriginatedToESME(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)
	addRoutes(t, s, []string{"40000-40009"})

	receiver := bindESME(t, s, rx, gosmpp.NonTLSDialer)

	if rc := core.submitTPDU(alice, dialled(smsSubmitRequestingReport(t, shortCode("40001"), 0x2a, "to the app", nil))); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	d := next[*pdu.DeliverSM](t, receiver)

	dest, err := pdu.NewAddressWithTonNpiAddr(0, 1, "40001")
	if err != nil {
		t.Fatal(err)
	}

	if d.SourceAddr != address(t, alice.msisdn) || d.DestAddr != dest {
		t.Fatalf("deliver_sm from %+v to %+v", d.SourceAddr, d.DestAddr)
	}

	if string(shortMessage(t, d)) != "to the app" {
		t.Fatalf("deliver_sm short_message %q", shortMessage(t, d))
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	report := statusReportTo(t, core, alice)
	if report.MessageReference != 0x2a || report.Recipient.Digits != "40001" || report.Status != tpdu.StatusReceived {
		t.Fatalf("SMS-STATUS-REPORT = %+v", report)
	}

	waitForReportDelivered(t, s)

	for _, msisdn := range core.routingRequests() {
		if msisdn == "40001" {
			t.Fatalf("routing requests = %+v; the routed message must not reach the HSS", core.routingRequests())
		}
	}
}

func TestRoutedMessageWaitsForAReceiver(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)
	addRoutes(t, s, []string{"40001"})

	if rc := core.submitTPDU(alice, dialled(smsSubmitRequestingReport(t, shortCode("40001"), 0x05, "wait for me", nil))); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelegated)

	receiver := bindESME(t, s, rx, gosmpp.NonTLSDialer)

	d := next[*pdu.DeliverSM](t, receiver)
	if string(shortMessage(t, d)) != "wait for me" {
		t.Fatalf("deliver_sm short_message %q", shortMessage(t, d))
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	if report := statusReportTo(t, core, alice); report.Status != tpdu.StatusReceived {
		t.Fatalf("SMS-STATUS-REPORT = %+v", report)
	}
}

func TestAPISubmissionToARoute(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)
	addRoutes(t, s, []string{"+140001"})

	receiver := bindESME(t, s, trx, gosmpp.NonTLSDialer)

	if code := s.api(t, http.MethodPost, "/api/v1/messages", api.CreateMessageParams{
		From: "+15550001111", To: "+140001", Text: "via the API",
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/messages = %d", code)
	}

	d := next[*pdu.DeliverSM](t, receiver)
	if text, _ := d.Message.GetMessage(); text != "via the API" {
		t.Fatalf("deliver_sm text %q", text)
	}

	if d.DestAddr != address(t, "140001") {
		t.Fatalf("deliver_sm destination %+v", d.DestAddr)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	if routings := core.routingRequests(); len(routings) != 0 {
		t.Fatalf("routing requests = %+v; a routed message must not reach the HSS", routings)
	}
}

func TestAdjacentRangesGoToTheirESMEs(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)

	if code := s.api(t, http.MethodPost, "/api/v1/esmes", api.CreateESMEParams{
		SystemID: "other", User: "ota@example.com", Password: ptrTo("other1"),
		AllowedNetworks: []string{"127.0.0.0/8"}, Routes: []string{"401000-401999"},
	}, nil); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/esmes = %d", code)
	}

	addRoutes(t, s, []string{"400000-400999"})

	if rc := core.submitTPDU(alice, dialled(smsSubmit(t, "401234", "upper range"))); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	if rc := core.submitTPDU(alice, dialled(smsSubmit(t, "400234", "lower range"))); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	other := bindESMEAs(t, s, rx, gosmpp.NonTLSDialer, "other", "other1")

	d := next[*pdu.DeliverSM](t, other)
	if string(shortMessage(t, d)) != "upper range" {
		t.Fatalf("the ESME with route 401000-401999 got %q", shortMessage(t, d))
	}

	ota := bindESME(t, s, rx, gosmpp.NonTLSDialer)

	d = next[*pdu.DeliverSM](t, ota)
	if string(shortMessage(t, d)) != "lower range" {
		t.Fatalf("the ESME with route 400000-400999 got %q", shortMessage(t, d))
	}

	s.waitForStatus(t, 1, db.StatusDelivered)
	s.waitForStatus(t, 2, db.StatusDelivered)
}

func TestAbbreviatedNumberToARoute(t *testing.T) {
	core, s := startSMPPSMSC(t, nil)
	addRoutes(t, s, []string{"4000"})

	receiver := bindESME(t, s, rx, gosmpp.NonTLSDialer)

	submit := smsSubmit(t, "4000", "abbreviated")
	submit[3] = 0xe1

	if rc := core.submitTPDU(alice, submit); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	if d := next[*pdu.DeliverSM](t, receiver); string(shortMessage(t, d)) != "abbreviated" {
		t.Fatalf("deliver_sm short_message %q", shortMessage(t, d))
	}

	s.waitForStatus(t, 1, db.StatusDelivered)
}
