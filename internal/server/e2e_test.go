package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tbcd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/core/sctp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/config"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/server"
)

const (
	coreHost             = "core.example.org"
	smscHost             = "smsc.example.org"
	realm                = "example.org"
	serviceCentreAddress = "15550000000"
	mmeNumber            = "15550000010"
	waitTimeout          = 10 * time.Second

	avpServingNode            uint32 = 2401
	avpMMEName                uint32 = 2402
	avpMMERealm               uint32 = 2408
	avpMWDStatus              uint32 = 3312
	avpSMDeliveryOutcome      uint32 = 3316
	avpMMESMDeliveryOutcome   uint32 = 3317
	avpSMDeliveryCause        uint32 = 3321
	avpAbsentUserDiagnosticSM uint32 = 3322
)

var loopback = netip.MustParseAddr("127.0.0.1")

type subscriber struct {
	imsi   string
	msisdn string
}

var (
	alice = subscriber{imsi: "001010000000001", msisdn: "15551230001"}
	bob   = subscriber{imsi: "001010000000002", msisdn: "15551230002"}
	carol = subscriber{imsi: "001010000000003", msisdn: "15551230003"}
)

type mtMessage struct {
	imsi string
	tpdu []byte
}

type deliveryReport struct {
	msisdn     string
	cause      uint32
	diagnostic *uint32
	failedNode bool
}

type fakeCore struct {
	t     *testing.T
	host  string
	realm string
	node  *diameter.Node

	busy    atomic.Bool
	dropSRR *atomic.Bool
	busied  atomic.Int32

	mu          sync.Mutex
	subscribers map[string]subscriber
	absent      map[string]bool
	mwdStatus   map[string]uint32
	answerMT    func(imsi string) *diameter.Message
	routings    []string
	delivered   []mtMessage
	reports     []deliveryReport
}

func skipIfNoSCTP(t *testing.T) {
	t.Helper()

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_SCTP)
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("SCTP not available in CI: %v", err)
		}

		t.Skipf("SCTP not available: %v", err)
	}

	_ = syscall.Close(fd)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newFakeCore(t *testing.T, subscribers ...subscriber) *fakeCore {
	t.Helper()

	return newNamedFakeCore(t, coreHost, subscribers...)
}

func newNamedFakeCore(t *testing.T, host string, subscribers ...subscriber) *fakeCore {
	t.Helper()

	return newRealmFakeCore(t, host, realm, subscribers...)
}

func newRealmFakeCore(t *testing.T, host, originRealm string, subscribers ...subscriber) *fakeCore {
	t.Helper()
	skipIfNoSCTP(t)

	c := &fakeCore{
		t:           t,
		host:        host,
		realm:       originRealm,
		subscribers: make(map[string]subscriber),
		absent:      make(map[string]bool),
		mwdStatus:   make(map[string]uint32),
	}

	for _, s := range subscribers {
		c.subscribers[s.msisdn] = s
	}

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost:      host,
			OriginRealm:     originRealm,
			HostIPAddresses: []netip.Addr{loopback},
			ProductName:     "fake-core",
		},
		Handler: diameter.HandlerFunc(c.serve),
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	c.node = node

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_ = c.node.Shutdown(ctx)
	})

	return c
}

func (c *fakeCore) serve(ctx context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
	switch req.CommandCode {
	case s6c.CommandSendRoutingInfoForSM:
		return c.routeOrFail(ctx, req)
	case sgd.CommandMTForwardShortMessage:
		return c.forwardShortMessage(req)
	case s6c.CommandReportSMDeliveryStatus:
		return c.reportDeliveryStatus(req)
	default:
		return c.answer(req, diameter.ResultCommandUnsupported)
	}
}

func (c *fakeCore) answer(req *diameter.Message, resultCode uint32) *diameter.Message {
	ans := diameter.NewAnswer(req, c.node.Identity(), resultCode)
	ans.AVPs = append(ans.AVPs, authSessionState())

	return ans
}

func (c *fakeCore) experimental(req *diameter.Message, resultCode uint32) *diameter.Message {
	ans := diameter.NewExperimentalAnswer(req, c.node.Identity(), tgpp.VendorID, resultCode)
	ans.AVPs = append(ans.AVPs, authSessionState())

	return ans
}

func (c *fakeCore) routeOrFail(ctx context.Context, req *diameter.Message) *diameter.Message {
	if c.dropSRR != nil && c.dropSRR.CompareAndSwap(true, false) {
		go func() { _ = c.node.SetPeers(nil) }()

		select {
		case <-ctx.Done():
		case <-time.After(waitTimeout):
		}

		return nil
	}

	if c.busy.Load() {
		c.busied.Add(1)

		return c.answer(req, diameter.ResultTooBusy)
	}

	return c.sendRoutingInfo(req)
}

func (c *fakeCore) sendRoutingInfo(req *diameter.Message) *diameter.Message {
	a, _ := req.Find(tgpp.AVPMSISDN, tgpp.VendorID)

	msisdn, err := tbcd.Decode(a.Data)
	if err != nil {
		return c.answer(req, diameter.ResultInvalidAVPValue)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.routings = append(c.routings, msisdn)

	sub, ok := c.subscribers[msisdn]

	switch {
	case !ok:
		return c.experimental(req, tgpp.ResultErrorUserUnknown)
	case c.absent[msisdn]:
		ans := c.experimental(req, tgpp.ResultErrorAbsentUser)
		ans.AVPs = append(ans.AVPs,
			diameter.Unsigned32(s6c.AVPMMEAbsentUserDiagnosticSM, diameter.AVPFlagMandatory, tgpp.VendorID, tgpp.AbsentUserIMSIDetached))

		return ans
	}

	ans := c.answer(req, diameter.ResultSuccess)
	ans.AVPs = append(ans.AVPs,
		diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, sub.imsi),
		diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(avpMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, c.host),
			diameter.UTF8String(avpMMERealm, 0, tgpp.VendorID, c.realm),
			diameter.OctetString(tgpp.AVPMMENumberForMTSMS, 0, tgpp.VendorID, mustTBCD(c.t, mmeNumber)),
		),
		diameter.Unsigned32(avpMWDStatus, diameter.AVPFlagMandatory, tgpp.VendorID, c.mwdStatus[msisdn]),
	)

	return ans
}

func (c *fakeCore) forwardShortMessage(req *diameter.Message) *diameter.Message {
	userName, _ := req.Find(diameter.AVPUserName, 0)
	smRPUI, _ := req.Find(sgd.AVPSMRPUI, tgpp.VendorID)
	imsi := userName.UTF8String()

	c.mu.Lock()
	answerMT := c.answerMT
	c.mu.Unlock()

	if answerMT != nil {
		if ans := answerMT(imsi); ans != nil {
			ans.CommandCode = req.CommandCode
			ans.ApplicationID = req.ApplicationID
			ans.HopByHopID = req.HopByHopID
			ans.EndToEndID = req.EndToEndID
			ans.Flags = req.Flags & diameter.FlagProxiable

			if sessionID, ok := req.Find(diameter.AVPSessionID, 0); ok {
				ans.AVPs = append([]diameter.AVP{sessionID}, ans.AVPs...)
			}

			return ans
		}
	}

	c.mu.Lock()
	c.delivered = append(c.delivered, mtMessage{imsi: imsi, tpdu: smRPUI.Data})
	c.mu.Unlock()

	return c.answer(req, diameter.ResultSuccess)
}

func (c *fakeCore) reportDeliveryStatus(req *diameter.Message) *diameter.Message {
	ui, _ := req.Find(tgpp.AVPUserIdentifier, tgpp.VendorID)
	inner, _ := ui.Grouped()
	msisdnAVP, _ := diameter.Find(inner, tgpp.AVPMSISDN, tgpp.VendorID)
	msisdn, _ := tbcd.Decode(msisdnAVP.Data)

	report := deliveryReport{msisdn: msisdn}

	outcome, _ := req.Find(avpSMDeliveryOutcome, tgpp.VendorID)
	outcomes, _ := outcome.Grouped()

	if mme, ok := diameter.Find(outcomes, avpMMESMDeliveryOutcome, tgpp.VendorID); ok {
		fields, _ := mme.Grouped()

		if cause, ok := diameter.Find(fields, avpSMDeliveryCause, tgpp.VendorID); ok {
			report.cause, _ = cause.Unsigned32()
		}

		if diag, ok := diameter.Find(fields, avpAbsentUserDiagnosticSM, tgpp.VendorID); ok {
			v, _ := diag.Unsigned32()
			report.diagnostic = &v
		}
	}

	_, report.failedNode = req.Find(avpServingNode, tgpp.VendorID)

	c.mu.Lock()
	c.reports = append(c.reports, report)
	c.mu.Unlock()

	return c.answer(req, diameter.ResultSuccess)
}

func (c *fakeCore) setAnswerMT(f func(imsi string) *diameter.Message) {
	c.mu.Lock()
	c.answerMT = f
	c.mu.Unlock()
}

func (c *fakeCore) setAbsent(msisdn string, absent bool) {
	c.mu.Lock()
	c.absent[msisdn] = absent
	c.mu.Unlock()
}

func (c *fakeCore) setMWDStatus(msisdn string, status uint32) {
	c.mu.Lock()
	c.mwdStatus[msisdn] = status
	c.mu.Unlock()
}

func (c *fakeCore) deliveredTo(imsi string) []mtMessage {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []mtMessage

	for _, m := range c.delivered {
		if m.imsi == imsi {
			out = append(out, m)
		}
	}

	return out
}

func (c *fakeCore) routingRequests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.routings...)
}

func (c *fakeCore) deliveryReports() []deliveryReport {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]deliveryReport(nil), c.reports...)
}

func (c *fakeCore) request(commandCode, applicationID uint32, avps ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   commandCode,
		ApplicationID: applicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, c.node.NewSessionID()),
			authSessionState(),
			diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, c.host),
			diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, c.realm),
			diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, smscHost),
			diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, realm),
		}, avps...),
	}
}

func (c *fakeCore) do(req *diameter.Message) *diameter.Message {
	c.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	ans, err := c.node.DoHost(ctx, smscHost, req)
	if err != nil {
		c.t.Fatalf("request %d to the SMSC: %v", req.CommandCode, err)
	}

	return ans
}

func (c *fakeCore) connect(s *smsc) {
	c.t.Helper()

	addr, ok := s.server.Addr().(*sctp.SCTPAddr)
	if !ok {
		c.t.Fatalf("unexpected SMSC address %v", s.server.Addr())
	}

	if err := c.node.SetPeers([]diameter.Peer{{
		ID:        "smsc",
		Host:      smscHost,
		Addresses: []netip.Addr{loopback},
		Port:      uint16(addr.Port),
		Applications: []diameter.Application{
			{ID: sgd.ApplicationID, VendorID: tgpp.VendorID},
			{ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
		},
	}}); err != nil {
		c.t.Fatalf("SetPeers: %v", err)
	}

	c.waitForSMSC()
}

func (c *fakeCore) waitForSMSC() {
	c.t.Helper()

	eventually(c.t, "the SMSC to connect", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := c.node.DoHost(ctx, smscHost, c.request(s6c.CommandSendRoutingInfoForSM, s6c.ApplicationID))

		return err == nil
	})
}

func (c *fakeCore) submit(from, to subscriber, text string) uint32 {
	c.t.Helper()

	ans := c.do(c.request(sgd.CommandMOForwardShortMessage, sgd.ApplicationID,
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, serviceCentreAddress)),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, from.imsi),
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, from.msisdn))),
		diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, smsSubmit(c.t, to.msisdn, text)),
	))

	return resultOf(c.t, ans)
}

func (c *fakeCore) alert(to subscriber) uint32 {
	c.t.Helper()

	return resultOf(c.t, c.do(c.request(s6c.CommandAlertServiceCentre, s6c.ApplicationID,
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, serviceCentreAddress)),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, to.msisdn))),
	)))
}

func authSessionState() diameter.AVP {
	return diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained)
}

func experimental(resultCode uint32, extra ...diameter.AVP) *diameter.Message {
	return &diameter.Message{AVPs: append([]diameter.AVP{
		diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, coreHost),
		diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, realm),
		diameter.Grouped(diameter.AVPExperimentalResult, diameter.AVPFlagMandatory, 0,
			diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
			diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, resultCode),
		),
		authSessionState(),
	}, extra...)}
}

func resultOf(t *testing.T, ans *diameter.Message) uint32 {
	t.Helper()

	rc, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		t.Fatalf("answer without Result-Code: %+v", ans)
	}

	v, err := rc.Unsigned32()
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func mustTBCD(t *testing.T, digits string) []byte {
	t.Helper()

	b, err := tbcd.Encode(digits)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func semiOctets(t *testing.T, digits string) []byte {
	t.Helper()

	out := []byte{byte(len(digits)), 0x91}

	return append(out, mustTBCD(t, digits)...)
}

func smsSubmit(t *testing.T, to, text string) []byte {
	t.Helper()

	out := []byte{0x01, 0x00}
	out = append(out, semiOctets(t, to)...)
	out = append(out, 0x00, 0x04, byte(len(text)))

	return append(out, text...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

type smsc struct {
	server *server.Server
	store  *db.DB
}

func testConfig(t *testing.T, dbPath string) config.Config {
	t.Helper()

	return config.Config{
		DB:            config.DB{Path: dbPath},
		ServiceCentre: config.ServiceCentre{Address: serviceCentreAddress},
		Diameter:      config.Diameter{OriginHost: smscHost, OriginRealm: realm, Address: loopback},
		HSS:           config.HSS{Realm: realm},
		API:           config.API{Address: loopback},
		Numbering:     config.Numbering{CountryCode: "1"},
		Delivery: config.Delivery{
			DefaultValidity: time.Hour,
			RetryIntervals:  []time.Duration{time.Hour},
			AttemptTimeout:  5 * time.Second,
			Concurrency:     4,
		},
	}
}

func startSMSC(t *testing.T, cfg config.Config) *smsc {
	t.Helper()

	srv := &server.Server{Config: cfg, Logger: discardLogger()}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	store, err := db.Open(context.Background(), cfg.DB.Path)
	if err != nil {
		t.Fatal(err)
	}

	s := &smsc{server: srv, store: store}

	t.Cleanup(func() { s.stop() })

	return s
}

func (s *smsc) stop() {
	if s.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	s.server.Shutdown(ctx)
	_ = s.store.Close()
	s.server = nil
}

func (s *smsc) waitForStatus(t *testing.T, id int64, want db.MessageStatus) {
	t.Helper()

	eventually(t, "message "+string(want), func() bool {
		m, err := s.store.GetMessage(context.Background(), id)
		return err == nil && m.Status == want
	})
}

func newSMSC(t *testing.T, subscribers ...subscriber) (*fakeCore, *smsc) {
	t.Helper()

	core := newFakeCore(t, subscribers...)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	return core, s
}

func TestMobileOriginatedMessageIsDelivered(t *testing.T) {
	core, s := newSMSC(t, alice, bob)

	if rc := core.submit(alice, bob, "hello bob"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	got := core.deliveredTo(bob.imsi)
	if len(got) != 1 {
		t.Fatalf("messages delivered to bob = %d", len(got))
	}

	tpdu := got[0].tpdu
	if tpdu[0]&0x03 != 0x00 || !bytes.Equal(tpdu[1:3+len(mustTBCD(t, alice.msisdn))], semiOctets(t, alice.msisdn)) ||
		!bytes.HasSuffix(tpdu, []byte("hello bob")) {
		t.Fatalf("SMS-DELIVER = %x", tpdu)
	}

	if routings := core.routingRequests(); len(routings) != 1 || routings[0] != bob.msisdn {
		t.Fatalf("routing requests = %v", routings)
	}

	if reports := core.deliveryReports(); len(reports) != 0 {
		t.Fatalf("unexpected delivery reports = %+v", reports)
	}
}

func TestUnknownRecipientFails(t *testing.T) {
	core, s := newSMSC(t, alice)

	if rc := core.submit(alice, bob, "anyone there?"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusFailed)

	if got := core.deliveredTo(bob.imsi); len(got) != 0 {
		t.Fatalf("delivered %d messages to an unknown user", len(got))
	}
}

func TestAbsentRecipientIsDeliveredOnAlert(t *testing.T) {
	core, s := newSMSC(t, alice, bob)

	diagnostic := uint32(1)

	core.setAnswerMT(func(imsi string) *diameter.Message {
		if imsi != bob.imsi {
			return nil
		}

		return experimental(tgpp.ResultErrorAbsentUser,
			diameter.Unsigned32(avpAbsentUserDiagnosticSM, diameter.AVPFlagMandatory, tgpp.VendorID, diagnostic))
	})

	if rc := core.submit(alice, bob, "first"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	eventually(t, "the absent-user report", func() bool { return len(core.deliveryReports()) == 1 })

	report := core.deliveryReports()[0]
	if report.msisdn != bob.msisdn || report.cause != s6c.DeliveryCauseAbsentUser ||
		report.diagnostic == nil || *report.diagnostic != diagnostic || !report.failedNode {
		t.Fatalf("report = %+v", report)
	}

	core.submit(alice, bob, "second")

	time.Sleep(300 * time.Millisecond)

	if routings := core.routingRequests(); len(routings) != 1 {
		t.Fatalf("routing requests while held = %v; the second message must wait for the alert", routings)
	}

	core.setAnswerMT(nil)
	core.setMWDStatus(bob.msisdn, s6c.MWDStatusMNRF)

	if rc := core.alert(bob); rc != diameter.ResultSuccess {
		t.Fatalf("ALA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)
	s.waitForStatus(t, 2, db.StatusDelivered)

	if got := core.deliveredTo(bob.imsi); len(got) != 2 || !bytes.HasSuffix(got[0].tpdu, []byte("first")) {
		t.Fatalf("delivered = %+v", got)
	}

	eventually(t, "the successful-transfer report", func() bool {
		reports := core.deliveryReports()
		return len(reports) >= 2 && reports[1].cause == s6c.DeliveryCauseSuccessfulTransfer && !reports[1].failedNode
	})
}

func TestStuckRecipientDoesNotBlockOthers(t *testing.T) {
	core, s := newSMSC(t, alice, bob, carol)

	release := make(chan struct{})

	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	core.setAnswerMT(func(imsi string) *diameter.Message {
		if imsi == bob.imsi {
			<-release
		}

		return nil
	})

	core.submit(alice, bob, "to bob")

	eventually(t, "delivery to bob to start", func() bool { return len(core.routingRequests()) == 1 })

	core.submit(alice, carol, "to carol")

	s.waitForStatus(t, 2, db.StatusDelivered)

	if m, err := s.store.GetMessage(context.Background(), 1); err != nil || m.Status != db.StatusPending {
		t.Fatalf("stuck message = %+v, %v", m, err)
	}

	close(release)

	s.waitForStatus(t, 1, db.StatusDelivered)
}

func TestPendingMessageIsDeliveredAfterRestart(t *testing.T) {
	core := newFakeCore(t, alice, bob)
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.Delivery.RetryIntervals = []time.Duration{500 * time.Millisecond}

	core.setAbsent(bob.msisdn, true)

	first := startSMSC(t, cfg)

	core.connect(first)

	core.submit(alice, bob, "while you were away")

	eventually(t, "a routing attempt", func() bool { return len(core.routingRequests()) >= 1 })

	first.stop()

	if got := core.deliveredTo(bob.imsi); len(got) != 0 {
		t.Fatalf("delivered %d messages to an absent user", len(got))
	}

	core.setAbsent(bob.msisdn, false)

	second := startSMSC(t, cfg)

	core.connect(second)

	second.waitForStatus(t, 1, db.StatusDelivered)

	if got := core.deliveredTo(bob.imsi); len(got) != 1 || !bytes.HasSuffix(got[0].tpdu, []byte("while you were away")) {
		t.Fatalf("delivered = %+v", got)
	}
}

func TestServerStartTwice(t *testing.T) {
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))

	if err := s.server.Start(context.Background()); !errors.Is(err, server.ErrAlreadyStarted) {
		t.Fatalf("second Start = %v, want ErrAlreadyStarted", err)
	}
}

func TestRoutingFailsOverToAnotherConnectedHSS(t *testing.T) {
	first := newNamedFakeCore(t, "node1.example.org", alice, bob)
	second := newNamedFakeCore(t, "node2.example.org", alice, bob)

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	first.connect(s)
	second.connect(s)

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()

	if err := first.node.Shutdown(ctx); err != nil {
		t.Fatalf("shut down the first node: %v", err)
	}

	if rc := second.submit(alice, bob, "hello over node 2"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	if len(first.routingRequests()) != 0 {
		t.Fatalf("the stopped node received %d routing requests", len(first.routingRequests()))
	}

	if got := second.deliveredTo(bob.imsi); len(got) != 1 {
		t.Fatalf("delivered via node 2 = %+v", got)
	}
}

func TestRoutingSpreadsAcrossConnectedHSSs(t *testing.T) {
	first := newNamedFakeCore(t, "node1.example.org", alice, bob)
	second := newNamedFakeCore(t, "node2.example.org", alice, bob)

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	first.connect(s)
	second.connect(s)

	for i := range 4 {
		if rc := first.submit(alice, bob, "hello"); rc != diameter.ResultSuccess {
			t.Fatalf("OFA %d result = %d", i, rc)
		}

		s.waitForStatus(t, int64(i+1), db.StatusDelivered)
	}

	if len(first.routingRequests()) == 0 || len(second.routingRequests()) == 0 {
		t.Fatalf("routing requests: node 1 = %d, node 2 = %d; want both used",
			len(first.routingRequests()), len(second.routingRequests()))
	}
}

func TestRoutingFailsOverWhenAnHSSIsTooBusy(t *testing.T) {
	first := newNamedFakeCore(t, "node1.example.org", alice, bob)
	second := newNamedFakeCore(t, "node2.example.org", alice, bob)

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	first.connect(s)
	second.connect(s)

	first.busy.Store(true)

	for i := range 2 {
		if rc := second.submit(alice, bob, "hello"); rc != diameter.ResultSuccess {
			t.Fatalf("OFA %d result = %d", i, rc)
		}

		s.waitForStatus(t, int64(i+1), db.StatusDelivered)
	}

	if first.busied.Load() == 0 {
		t.Fatal("the busy node was never tried; the test did not exercise failover")
	}

	if got := second.routingRequests(); len(got) != 2 {
		t.Fatalf("routing requests on node 2 = %d, want 2", len(got))
	}
}

func TestRoutingFailsOverWhenAnHSSDropsTheRequest(t *testing.T) {
	var drop atomic.Bool

	first := newNamedFakeCore(t, "node1.example.org", alice, bob)
	second := newNamedFakeCore(t, "node2.example.org", alice, bob)
	first.dropSRR, second.dropSRR = &drop, &drop

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	first.connect(s)
	second.connect(s)

	drop.Store(true)

	if rc := first.submit(alice, bob, "hello"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	s.waitForStatus(t, 1, db.StatusDelivered)

	if drop.Load() {
		t.Fatal("no node dropped the routing request; the test did not exercise failover")
	}

	if m, err := s.store.GetMessage(context.Background(), 1); err != nil || m.Retries != 0 {
		t.Fatalf("message = %+v, %v; failover must happen within the attempt", m, err)
	}
}

func TestRoutingWaitsForAnHSSToConnect(t *testing.T) {
	mme := newRealmFakeCore(t, "mme.visited.example.net", "visited.example.net", alice, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	mme.connect(s)

	if rc := mme.submit(alice, bob, "hello"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	time.Sleep(200 * time.Millisecond)

	if got := mme.routingRequests(); len(got) != 0 {
		t.Fatalf("routing requests to a peer outside the HSS realm = %v", got)
	}

	hss := newFakeCore(t, alice, bob)
	hss.connect(s)

	s.waitForStatus(t, 1, db.StatusDelivered)

	if m, err := s.store.GetMessage(context.Background(), 1); err != nil || m.Retries != 0 {
		t.Fatalf("message = %+v, %v; waiting for the HSS must not use up a retry", m, err)
	}
}

func TestRoutingSkipsHSSOutsideAllowedNetworks(t *testing.T) {
	cfg := testConfig(t, filepath.Join(t.TempDir(), "smsc.db"))
	cfg.HSS.AllowedNetworks = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	cfg.Delivery.AttemptTimeout = time.Second

	core := newFakeCore(t, alice, bob)
	s := startSMSC(t, cfg)
	core.connect(s)

	if rc := core.submit(alice, bob, "hello"); rc != diameter.ResultSuccess {
		t.Fatalf("OFA result = %d", rc)
	}

	eventually(t, "the routing lookup to time out", func() bool {
		m, err := s.store.GetMessage(context.Background(), 1)
		return err == nil && m.Retries == 1
	})

	if got := core.routingRequests(); len(got) != 0 {
		t.Fatalf("routing requests to an HSS outside the allowed networks = %v", got)
	}
}

func (s *smsc) api(t *testing.T, method, path string, body any, out any) int {
	t.Helper()

	var reader io.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}

		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, "http://"+s.server.APIAddr().String()+path, reader)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}

	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Result json.RawMessage `json:"result"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}

	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}

	return resp.StatusCode
}

func (s *smsc) send(t *testing.T, from, to, text string) []api.Message {
	t.Helper()

	var created api.CreateMessageResponse

	if code := s.api(t, http.MethodPost, "/api/v1/messages", api.CreateMessageParams{From: from, To: to, Text: text}, &created); code != http.StatusCreated {
		t.Fatalf("POST /api/v1/messages = %d", code)
	}

	return created.Items
}

func (s *smsc) message(t *testing.T, id int64) api.MessageWithAttempts {
	t.Helper()

	var m api.MessageWithAttempts

	if code := s.api(t, http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), nil, &m); code != http.StatusOK {
		t.Fatalf("GET message %d = %d", id, code)
	}

	return m
}

func (s *smsc) waitForAPIStatus(t *testing.T, id int64, want string) api.MessageWithAttempts {
	t.Helper()

	var m api.MessageWithAttempts

	eventually(t, "message "+want+" over the API", func() bool {
		m = s.message(t, id)
		return m.Status == want
	})

	return m
}

func TestAPIMessageIsDelivered(t *testing.T) {
	core := newFakeCore(t, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.send(t, "+15550001111", "+"+bob.msisdn, "hello from the API")
	if len(sent) != 1 || sent[0].Status != "pending" {
		t.Fatalf("sent = %+v", sent)
	}

	m := s.waitForAPIStatus(t, sent[0].ID, "delivered")

	if len(m.Attempts) != 2 || m.Attempts[0].Step != "routing" || m.Attempts[0].Outcome != "success" ||
		m.Attempts[1].Step != "delivery" || m.Attempts[1].Node != coreHost || m.Attempts[1].Outcome != "success" {
		t.Fatalf("attempts = %+v", m.Attempts)
	}

	got := core.deliveredTo(bob.imsi)
	if len(got) != 1 || !bytes.Contains(got[0].tpdu, semiOctets(t, "15550001111")) {
		t.Fatalf("delivered = %+v", got)
	}
}

func TestAPIConcatenatedMessageIsDelivered(t *testing.T) {
	core := newFakeCore(t, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.send(t, "+15550001111", "+"+bob.msisdn, strings.Repeat("0123456789", 20))
	if len(sent) != 2 {
		t.Fatalf("sent %d parts, want 2", len(sent))
	}

	for _, part := range sent {
		s.waitForAPIStatus(t, part.ID, "delivered")
	}

	got := core.deliveredTo(bob.imsi)
	if len(got) != 2 {
		t.Fatalf("delivered %d parts, want 2", len(got))
	}

	for i, m := range got {
		if m.tpdu[0]&0x40 == 0 {
			t.Fatalf("part %d SMS-DELIVER has no user data header indicator: %x", i+1, m.tpdu)
		}
	}
}

func TestAPIShowsAbsentUserRetry(t *testing.T) {
	core := newFakeCore(t, bob)
	core.setAbsent(bob.msisdn, true)

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.send(t, "+15550001111", "+"+bob.msisdn, "are you there?")

	var m api.MessageWithAttempts

	eventually(t, "the routing failure to be recorded", func() bool {
		m = s.message(t, sent[0].ID)
		return len(m.Attempts) > 0
	})

	a := m.Attempts[0]
	if m.Status != "pending" || m.NextAttemptAt == "" || a.Step != "routing" || a.Outcome != "absent_user" ||
		a.ResultCode == nil || *a.ResultCode != tgpp.ResultErrorAbsentUser || a.VendorID == nil || *a.VendorID != tgpp.VendorID ||
		a.AbsentDiagnostics == nil || *a.AbsentDiagnostics != (api.AbsentDiagnostics{MME: "imsi_detached"}) {
		t.Fatalf("message = %+v, attempts = %+v", m.Message, m.Attempts)
	}
}

func TestAPIShowsDeliveryFailureDetails(t *testing.T) {
	core := newFakeCore(t, bob)
	core.setAnswerMT(func(string) *diameter.Message {
		return experimental(tgpp.ResultErrorSMDeliveryFailure,
			diameter.Grouped(sgd.AVPSMDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.Unsigned32(sgd.AVPSMEnumeratedDeliveryFailureCause, diameter.AVPFlagMandatory, tgpp.VendorID, sgd.CauseEquipmentProtocolError),
				diameter.OctetString(sgd.AVPSMDiagnosticInfo, diameter.AVPFlagMandatory, tgpp.VendorID, []byte{0x00, 0xd0, 0x00}),
			))
	})

	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	sent := s.send(t, "+15550002222", "+"+bob.msisdn, "is your SIM full?")

	var m api.MessageWithAttempts

	eventually(t, "the delivery failure to be recorded", func() bool {
		m = s.message(t, sent[0].ID)
		return len(m.Attempts) > 1
	})

	a := m.Attempts[1]
	if m.Status != "pending" || a.Step != "delivery" || a.Outcome != "sm_delivery_failure" ||
		a.ResultCode == nil || *a.ResultCode != tgpp.ResultErrorSMDeliveryFailure ||
		a.FailureCause != "equipment_protocol_error" || a.TPFailureCause != "usim_sms_storage_full" ||
		a.AbsentDiagnostic != "" || a.AbsentDiagnostics != nil {
		t.Fatalf("message = %+v, attempts = %+v", m.Message, m.Attempts)
	}
}

func TestAPIFindsMobileOriginatedMessages(t *testing.T) {
	core := newFakeCore(t, alice, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))
	core.connect(s)

	core.submit(alice, bob, "hi bob")
	s.waitForStatus(t, 1, db.StatusDelivered)

	var list api.ListMessagesResponse

	if code := s.api(t, http.MethodGet, "/api/v1/messages?to=%2B"+bob.msisdn, nil, &list); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}

	if len(list.Items) != 1 || list.Items[0].From != "+"+alice.msisdn || list.Items[0].Encoding != "binary" || list.Items[0].Text != nil ||
		list.Items[0].Status != "delivered" {
		t.Fatalf("items = %+v", list.Items)
	}
}

func TestAPIDiameterStatus(t *testing.T) {
	core := newFakeCore(t, bob)
	s := startSMSC(t, testConfig(t, filepath.Join(t.TempDir(), "smsc.db")))

	var status api.DiameterStatus

	s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)

	if status.HSSAvailable || len(status.Peers) != 0 || status.Host != smscHost {
		t.Fatalf("before connect = %+v", status)
	}

	core.connect(s)

	eventually(t, "the HSS to be available", func() bool {
		s.api(t, http.MethodGet, "/api/v1/diameter", nil, &status)
		return status.HSSAvailable
	})

	if len(status.Peers) != 1 || status.Peers[0].Host != coreHost || status.Peers[0].State != "open" ||
		status.Peers[0].Address != loopback.String() || !slices.Contains(status.Peers[0].Applications, "s6c") {
		t.Fatalf("peers = %+v", status.Peers)
	}
}
