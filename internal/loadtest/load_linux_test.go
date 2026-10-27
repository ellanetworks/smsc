//go:build linux

// Package loadtest loads a running Ella SMSC from another host, as the core network of its subscribers: the MME that
// submits their short messages and takes those for them over SGd, and the HSS that routes them over S6c (TS 29.338).
// It measures the submission of the messages, from the MO-Forward-Short-Message request to its answer, and their
// delivery, from that request to the MT-Forward-Short-Message request of their delivery.
//
// Build it with go test -c ./internal/loadtest, and run it with -load.
package loadtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"slices"
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

var (
	load = flag.Bool("load", false, "load the SMSC at -smsc")

	smscAddr     = flag.String("smsc", "", "the address of the SMSC, for Diameter and the API")
	diameterPort = flag.Int("diameter-port", 3868, "the SCTP port of the SMSC's Diameter")
	apiPort      = flag.Int("api-port", 5010, "the TCP port of the SMSC's API")
	localAddr    = flag.String("local", "", "the address of this host, the core's")
	email        = flag.String("email", "admin@example.com", "the admin of the SMSC, which the test creates if it has none")
	password     = flag.String("password", "loadtest password", "the password of -email")

	subs  = flag.Int("subs", 10000, "the subscribers, each sending and receiving")
	rates = flag.String("rates", "50,100,200", "the short messages per second to submit, each for -step")
	step  = flag.Duration("step", time.Minute, "how long each rate lasts")
	drain = flag.Duration("drain", time.Minute, "the most to wait, after a rate, for its messages to be delivered")
)

const (
	coreHost  = "core.epc.mnc001.mcc001.3gppnetwork.org"
	realm     = "epc.mnc001.mcc001.3gppnetwork.org"
	smscHost  = "smsc.node.epc.mnc001.mcc001.3gppnetwork.org"
	scAddress = "15550000000"
	mmeNumber = "15550000010"

	// TS 29.338 §7.3.
	avpServingNode uint32 = 2401
	avpMMEName     uint32 = 2402
	avpMMERealm    uint32 = 2408
	avpMWDStatus   uint32 = 3312
)

func TestLoad(t *testing.T) {
	if !*load {
		t.Skip("run with -load")
	}

	smsc := netip.MustParseAddr(*smscAddr)
	api := fmt.Sprintf("http://%s:%d/api/v1", smsc, *apiPort)

	token := logIn(t, api)

	call(t, token, http.MethodPut, api+"/operator", map[string]any{
		"mcc": "001", "mnc": "01", "service_centre_address": "+" + scAddress, "numbering": map[string]any{"country_code": "1"},
	})
	call(t, token, http.MethodPut, api+"/delivery", map[string]any{"default_validity_seconds": 3600, "retry_intervals_seconds": []int{60}})

	c := newCore(t, netip.MustParseAddr(*localAddr), smsc)

	for _, rate := range parseRates(t, *rates) {
		c.run(t, api, rate)
	}
}

// core is the MME and the HSS of the subscribers: subscriber i has the IMSI and MSISDN of index i.
type core struct {
	node *diameter.Node

	next atomic.Int64

	mu        sync.Mutex
	sent      map[int64]time.Time
	delivered map[int64]time.Duration
}

func newCore(t *testing.T, local, smsc netip.Addr) *core {
	t.Helper()

	c := &core{sent: map[int64]time.Time{}, delivered: map[int64]time.Duration{}}

	node, err := diameter.New(diameter.Config{
		Identity: diameter.Identity{
			OriginHost: coreHost, OriginRealm: realm, HostIPAddresses: []netip.Addr{local}, ProductName: "loadtest",
		},
		Handler:           diameter.HandlerFunc(c.serve),
		ReconnectInterval: 100 * time.Millisecond,
		Logger:            slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_ = node.Shutdown(ctx)
	})

	c.node = node

	if err := node.SetPeers([]diameter.Peer{{
		ID: "smsc", Addresses: []netip.Addr{smsc}, Transports: []diameter.Transport{diameter.TransportSCTP},
		Dial: &diameter.Dial{Port: uint16(*diameterPort)},
		Applications: []diameter.Application{
			{ID: sgd.ApplicationID, VendorID: tgpp.VendorID}, {ID: s6c.ApplicationID, VendorID: tgpp.VendorID},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := node.Send(ctx, c.request(s6c.CommandSendRoutingInfoForSM, s6c.ApplicationID))

		cancel()

		if err == nil {
			return c
		}

		if time.Now().After(deadline) {
			t.Fatalf("the SMSC did not connect: %v", err)
		}
	}
}

// run submits short messages at rate a second for -step, between subscribers at random, and reports their submission
// and their delivery.
func (c *core) run(t *testing.T, api string, rate float64) {
	t.Helper()

	n := int(rate * step.Seconds())

	var (
		submitted latencies
		ids       = make([]int64, 0, n)
		idsMu     sync.Mutex
		wg        sync.WaitGroup
		cpu0      = scrape(t, api)["process_cpu_seconds_total"]
		started   = time.Now()
	)

	for i := range n {
		if d := time.Until(started.Add(time.Duration(float64(i) / rate * float64(time.Second)))); d > 0 {
			time.Sleep(d)
		}

		wg.Go(func() {
			from := rand.IntN(*subs)
			to := (from + 1 + rand.IntN(*subs-1)) % *subs
			id := c.next.Add(1)

			c.mu.Lock()
			c.sent[id] = time.Now()
			c.mu.Unlock()

			began := time.Now()

			if err := c.submit(from, to, id); err != nil {
				submitted.fail(err)
				return
			}

			submitted.add(time.Since(began))

			idsMu.Lock()

			ids = append(ids, id)

			idsMu.Unlock()
		})
	}

	wg.Wait()

	elapsed := time.Since(started)
	m := scrape(t, api)

	// The deliveries of the accepted messages, waited for at most -drain after the last submission.
	for deadline := time.Now().Add(*drain); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if c.deliveredOf(ids) == len(ids) {
			break
		}
	}

	var delivered latencies

	c.mu.Lock()

	for _, id := range ids {
		if d, ok := c.delivered[id]; ok {
			delivered.add(d)
		}
	}

	c.mu.Unlock()

	submitted.report(t, fmt.Sprintf("submitted at %4.0f/s", rate), n, elapsed)
	delivered.report(t, fmt.Sprintf("delivered at %4.0f/s", rate), len(ids), 0)
	t.Logf("  SMSC: %.0f%% of a CPU, %.1f MiB resident",
		100*(m["process_cpu_seconds_total"]-cpu0)/elapsed.Seconds(), m["process_resident_memory_bytes"]/(1<<20))
}

func (c *core) deliveredOf(ids []int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := 0

	for _, id := range ids {
		if _, ok := c.delivered[id]; ok {
			n++
		}
	}

	return n
}

// submit sends the short message id from subscriber from to subscriber to (TS 29.338 §6.2.1).
func (c *core) submit(from, to int, id int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ans, err := c.node.Send(ctx, c.request(sgd.CommandMOForwardShortMessage, sgd.ApplicationID,
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(scAddress)),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, imsiOf(from)),
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(msisdnOf(from)))),
		diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, smsSubmit(msisdnOf(to), id)),
	))
	if err != nil {
		return err
	}

	if rc := resultOf(ans); rc != diameter.ResultSuccess {
		return fmt.Errorf("result code %d", rc)
	}

	return nil
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

// route answers that the recipient is registered at this MME (TS 29.338 §5.3.2.3).
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
	if err != nil || !strings.HasPrefix(msisdn, "1555") || i >= *subs {
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

// deliver takes a short message for a subscriber, and records when it came (TS 29.338 §6.2.2).
func (c *core) deliver(req *diameter.Message) *diameter.Message {
	if ui, ok := req.Find(sgd.AVPSMRPUI, tgpp.VendorID); ok {
		if i := bytes.LastIndexByte(ui.Data, '#'); i >= 0 {
			if id, err := strconv.ParseInt(string(ui.Data[i+1:]), 10, 64); err == nil {
				c.mu.Lock()
				if sent, ok := c.sent[id]; ok {
					if _, done := c.delivered[id]; !done {
						c.delivered[id] = time.Since(sent)
					}
				}
				c.mu.Unlock()
			}
		}
	}

	return c.answer(req, diameter.ResultSuccess)
}

func (c *core) answer(req *diameter.Message, resultCode uint32) *diameter.Message {
	ans := diameter.NewAnswer(req, c.node.Identity(), resultCode)
	ans.AVPs = append(ans.AVPs, authSessionState())

	return ans
}

func (c *core) request(commandCode, applicationID uint32, avps ...diameter.AVP) *diameter.Message {
	return &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   commandCode,
		ApplicationID: applicationID,
		AVPs: append([]diameter.AVP{
			diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, c.node.NewSessionID()),
			authSessionState(),
			diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, coreHost),
			diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, realm),
			diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, smscHost),
			diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, realm),
		}, avps...),
	}
}

func authSessionState() diameter.AVP {
	return diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained)
}

func resultOf(ans *diameter.Message) uint32 {
	rc, ok := ans.Find(diameter.AVPResultCode, 0)
	if !ok {
		return 0
	}

	v, _ := rc.Unsigned32()

	return v
}

func imsiOf(i int) string   { return fmt.Sprintf("00101%010d", i) }
func msisdnOf(i int) string { return fmt.Sprintf("1555%07d", i) }

func mustTBCD(digits string) []byte {
	b, err := tbcd.Encode(digits)
	if err != nil {
		panic(err)
	}

	return b
}

// smsSubmit is an SMS-SUBMIT to the international number to, its 8-bit text ending with "#id" (TS 23.040 §9.2.2.2).
func smsSubmit(to string, id int64) []byte {
	text := "load test #" + strconv.FormatInt(id, 10)

	out := []byte{0x01, 0x00, byte(len(to)), 0x91}
	out = append(out, mustTBCD(to)...)
	out = append(out, 0x00, 0x04, byte(len(text)))

	return append(out, text...)
}

// logIn creates the admin of the SMSC if it has no user yet, logs it in, and returns its access token.
func logIn(t *testing.T, api string) string {
	t.Helper()

	credentials := map[string]string{"email": *email, "password": *password}

	if code := post(t, api+"/init", credentials, nil); code != http.StatusCreated && code != http.StatusForbidden {
		t.Fatalf("POST %s/init: %d", api, code)
	}

	var resp struct {
		Result struct {
			Token string `json:"token"`
		} `json:"result"`
	}

	if code := post(t, api+"/auth/login", credentials, &resp); code != http.StatusOK {
		t.Fatalf("POST %s/auth/login: %d", api, code)
	}

	return resp.Result.Token
}

func post(t *testing.T, url string, body, out any) int {
	t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	if out != nil && res.StatusCode/100 == 2 {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}

	return res.StatusCode
}

func call(t *testing.T, token, method, url string, body any) {
	t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode/100 != 2 {
		out, _ := io.ReadAll(res.Body)
		t.Fatalf("%s %s: %s: %s", method, url, res.Status, out)
	}
}

// scrape is the SMSC's metrics without labels, and the sums of those with.
func scrape(t *testing.T, api string) map[string]float64 {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, api+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Accept", "text/plain")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	m := map[string]float64{}

	for s := bufio.NewScanner(res.Body); s.Scan(); {
		line := s.Text()
		if line == "" || line[0] == '#' {
			continue
		}

		name, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}

		if v, err := strconv.ParseFloat(strings.Fields(value)[0], 64); err == nil {
			m[name] += v
		}
	}

	return m
}

func parseRates(t *testing.T, s string) []float64 {
	t.Helper()

	var out []float64

	for f := range strings.SplitSeq(s, ",") {
		v, err := strconv.ParseFloat(strings.TrimSpace(f), 64)
		if err != nil || v <= 0 {
			t.Fatalf("bad rate %q", f)
		}

		out = append(out, v)
	}

	return out
}

// latencies are the durations of the operations that succeeded, and the errors of those that failed.
type latencies struct {
	mu     sync.Mutex
	d      []time.Duration
	errors map[string]int
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.d = append(l.d, d)
}

func (l *latencies) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.errors == nil {
		l.errors = map[string]int{}
	}

	l.errors[err.Error()]++
}

func (l *latencies) at(q float64) time.Duration {
	if len(l.d) == 0 {
		return 0
	}

	return l.d[min(len(l.d)-1, int(q*float64(len(l.d))))]
}

// report logs the operations: n attempted over elapsed, those that failed, and the latencies of the others.
func (l *latencies) report(t *testing.T, what string, n int, elapsed time.Duration) {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()

	slices.Sort(l.d)

	var sum time.Duration
	for _, d := range l.d {
		sum += d
	}

	rate := ""
	if elapsed > 0 {
		rate = fmt.Sprintf(" in %s (%.1f/s)", elapsed.Round(time.Second), float64(n)/elapsed.Seconds())
	}

	failed := n - len(l.d)

	t.Logf("%s: %d%s, %d failed (%.3f%%), mean %s p50 %s p99 %s max %s", what, n, rate, failed,
		100*float64(failed)/float64(max(1, n)), (sum / time.Duration(max(1, len(l.d)))).Round(time.Millisecond),
		l.at(0.5).Round(time.Millisecond), l.at(0.99).Round(time.Millisecond), l.at(1).Round(time.Millisecond))

	for err, c := range l.errors {
		t.Logf("  %d× %s", c, err)
	}
}
