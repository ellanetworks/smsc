//go:build integration && linux

package integration

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tbcd"
	"github.com/ellanetworks/core/diameter/tgpp"
)

const (
	coreHost  = "core.epc.mnc001.mcc001.3gppnetwork.org"
	realm     = "epc.mnc001.mcc001.3gppnetwork.org"
	scAddress = "15550000000"
	mmeNumber = "15550000010"

	// TS 29.338 clause 7.3
	avpServingNode uint32 = 2401
	avpMMEName     uint32 = 2402
	avpMMERealm    uint32 = 2408
	avpMWDStatus   uint32 = 3312
)

var loopback = netip.MustParseAddr("127.0.0.1")

type core struct {
	t     *testing.T
	node  *diameter.Node
	hosts map[string]string
	delay atomic.Int64
	next  atomic.Int64

	mu         sync.Mutex
	delivered  map[int64]int
	inflight   map[string]int
	overlapped []string
	servedBy   map[string]time.Time
}

func newCore(t *testing.T, nodes []*node) *core {
	t.Helper()

	c := &core{
		t: t, hosts: map[string]string{}, delivered: map[int64]int{}, inflight: map[string]int{},
		servedBy: map[string]time.Time{},
	}

	n, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost: coreHost, OriginRealm: realm, HostIPAddresses: []netip.Addr{loopback}, ProductName: "integration",
		},
		Handler:           diameter.HandlerFunc(c.serve),
		ReconnectInterval: 100 * time.Millisecond,
		WatchdogInterval:  6 * time.Second,
		Logger:            slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_ = n.Shutdown(ctx)
	})

	c.node = n

	var peers []diameter.Peer

	for _, smsc := range nodes {
		c.hosts[smsc.name] = smsc.diameterHost()

		peers = append(peers, diameter.Peer{
			ID: smsc.name, Host: c.hosts[smsc.name], Addresses: []netip.Addr{loopback},
			Transports: []diameter.Transport{diameter.TransportSCTP}, Dial: &diameter.Dial{Port: uint16(smsc.diameterPort)},
			Applications: []diameter.Application{
				{ID: sgd.ApplicationID, VendorID: tgpp.VendorID}, {ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
			},
		})
	}

	if err := n.SetPeers(peers); err != nil {
		t.Fatal(err)
	}

	for _, smsc := range nodes {
		c.waitOpen(smsc)
	}

	return c
}

func (c *core) waitOpen(smsc *node) {
	c.t.Helper()

	host := c.hosts[smsc.name]

	eventually(c.t, "the core connected to "+smsc.name, func() error {
		for _, p := range c.node.Peers() {
			if strings.EqualFold(p.Host, host) && p.State == diameter.PeerOpen {
				return nil
			}
		}

		return fmt.Errorf("not connected to %s", host)
	})
}

// TS 29.338 clause 6.2.1
func (c *core) submit(via *node, from, to int) (int64, error) {
	id := c.next.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ans, err := c.node.Do(ctx, via.name, c.request(c.hosts[via.name], sgd.CommandMOForwardShortMessage, sgd.ApplicationID,
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(scAddress)),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, imsiOf(from)),
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(msisdnOf(from)))),
		diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, smsSubmit(msisdnOf(to), id)),
	))
	if err != nil {
		return 0, err
	}

	if rc := resultOf(ans); rc != diameter.ResultSuccess {
		return 0, fmt.Errorf("result code %d", rc)
	}

	return id, nil
}

func (c *core) serve(_ context.Context, _ *diameter.Conn, req *diameter.Message) *diameter.Message {
	switch req.CommandCode {
	case s6c.CommandSendRoutingInfoForSM:
		return c.route(req)
	case sgd.CommandMTForwardShortMessage:
		return c.deliver(req)
	case s6c.CommandReportSMDeliveryStatus:
		return c.answer(req, diameter.ResultSuccess)
	default:
		return c.answer(req, diameter.ResultCommandUnsupported)
	}
}

// TS 29.338 clause 5.3.2.3
func (c *core) route(req *diameter.Message) *diameter.Message {
	a, ok := req.Find(tgpp.AVPMSISDN, tgpp.VendorID)
	if !ok {
		return c.answer(req, diameter.ResultMissingAVP)
	}

	msisdn, err := tbcd.Decode(a.Data)
	if err != nil {
		return c.answer(req, diameter.ResultInvalidAVPValue)
	}

	i, err := strconv.Atoi(strings.TrimPrefix(msisdn, "1555"))
	if err != nil {
		return c.answer(req, diameter.ResultInvalidAVPValue)
	}

	ans := c.answer(req, diameter.ResultSuccess)
	ans.AVPs = append(ans.AVPs,
		diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, imsiOf(i)),
		diameter.Grouped(avpServingNode, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(avpMMEName, diameter.AVPFlagMandatory, tgpp.VendorID, coreHost),
			diameter.UTF8String(avpMMERealm, 0, tgpp.VendorID, realm),
			diameter.OctetString(tgpp.AVPMMENumberForMTSMS, 0, tgpp.VendorID, mustTBCD(mmeNumber)),
		),
		diameter.Unsigned32(avpMWDStatus, diameter.AVPFlagMandatory, tgpp.VendorID, 0),
	)

	return ans
}

// TS 23.040 clause 6.2
func (c *core) deliver(req *diameter.Message) *diameter.Message {
	imsi := ""
	if a, ok := req.Find(diameter.AVPUserName, 0); ok {
		imsi = a.UTF8String()
	}

	host := ""
	if a, ok := req.Find(diameter.AVPOriginHost, 0); ok {
		host = strings.ToLower(a.UTF8String())
	}

	c.mu.Lock()
	if _, ok := c.servedBy[host+"/"+imsi]; !ok {
		c.servedBy[host+"/"+imsi] = time.Now()
	}

	c.inflight[imsi]++

	if c.inflight[imsi] > 1 {
		c.overlapped = append(c.overlapped, imsi)
	}
	c.mu.Unlock()

	time.Sleep(time.Duration(c.delay.Load()))

	if ui, ok := req.Find(sgd.AVPSMRPUI, tgpp.VendorID); ok {
		if i := bytes.LastIndexByte(ui.Data, '#'); i >= 0 {
			if id, err := strconv.ParseInt(string(ui.Data[i+1:]), 10, 64); err == nil {
				c.mu.Lock()
				c.delivered[id]++
				c.mu.Unlock()
			}
		}
	}

	c.mu.Lock()
	c.inflight[imsi]--
	c.mu.Unlock()

	return c.answer(req, diameter.ResultSuccess)
}

func (c *core) deliveredAll(ids []int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	missing := 0

	for _, id := range ids {
		if c.delivered[id] == 0 {
			missing++
		}
	}

	if missing > 0 {
		return fmt.Errorf("%d of %d accepted messages not delivered", missing, len(ids))
	}

	return nil
}

func (c *core) firstDelivery(by *node, to int) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	at, ok := c.servedBy[strings.ToLower(c.hosts[by.name])+"/"+imsiOf(to)]

	return at, ok
}

func (c *core) overlaps() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.overlapped...)
}

func (c *core) answer(req *diameter.Message, resultCode uint32) *diameter.Message {
	ans := diameter.NewAnswer(req, c.node.Identity(), resultCode)
	ans.AVPs = append(ans.AVPs, authSessionState())

	return ans
}

func (c *core) request(host string, commandCode, applicationID uint32, avps ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   commandCode,
		ApplicationID: applicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, c.node.NewSessionID()),
			authSessionState(),
			diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, coreHost),
			diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, realm),
			diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, host),
			diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, realm),
		}, avps...),
	}
}

func authSessionState() diameter.AVP {
	return diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0,
		diameter.AuthSessionStateNoStateMaintained)
}

func resultOf(ans *diameter.Message) uint32 {
	rc, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		return 0
	}

	v, _ := rc.Unsigned32()

	return v
}

func imsiOf(i int) string { return fmt.Sprintf("00101%010d", i) }

func msisdnOf(i int) string { return fmt.Sprintf("1555%07d", i) }

func mustTBCD(digits string) []byte {
	b, err := tbcd.Encode(digits)
	if err != nil {
		panic(err)
	}

	return b
}

// TS 23.040 clause 9.2.2.2
func smsSubmit(to string, id int64) []byte {
	text := "integration #" + strconv.FormatInt(id, 10)

	out := []byte{0x01, 0x00, byte(len(to)), 0x91}
	out = append(out, mustTBCD(to)...)
	out = append(out, 0x00, 0x04, byte(len(text)))

	return append(out, text...)
}
