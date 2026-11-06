//go:build linux

package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/smpp"
)

var (
	loadSMPP = flag.Bool("load-smpp", false, "load the SMSC at -smsc with submit_sm from an ESME")

	smppPort     = flag.Int("smpp-port", 2775, "the TCP port of the SMSC's SMPP")
	sessions     = flag.Int("sessions", 4, "the transceiver sessions of the ESME")
	smppWindow   = flag.Int("window", smpp.DefaultWindow, "the submit_sm without a response per session")
	withReceipts = flag.Bool("receipts", false, "ask for a delivery receipt for each message")
)

const (
	esmeSystemID = "loadtest"
	esmePassword = "loadtst"
	esmeSource   = "15559990000"
)

func TestLoadSMPP(t *testing.T) {
	if !*loadSMPP {
		t.Skip("run with -load-smpp")
	}

	nodes := smscNodes()
	smsc := nodes[0].addr
	local := netip.MustParseAddr(*localAddr)
	api := nodes[0].api

	token := logIn(t, api)

	for i := range nodes {
		nodes[i].host = diameterHost(t, token, nodes[i].api)
	}

	call(t, token, http.MethodPut, api+"/operator", map[string]any{
		"mcc": "001", "mnc": "01", "service_centre_address": "+" + scAddress, "numbering": map[string]any{"country_code": "1"},
	})
	call(t, token, http.MethodPut, api+"/delivery", map[string]any{"default_validity_seconds": 3600, "retry_intervals_seconds": []int{60}})

	ensureESME(t, token, api, local)

	c := newCore(t, local, nodes)
	e := newESME(t, netip.AddrPortFrom(smsc, uint16(*smppPort)).String())

	for _, rate := range parseRates(t, *rates) {
		e.run(t, c, api, rate)
	}
}

func ensureESME(t *testing.T, token, api string, local netip.Addr) {
	t.Helper()

	pw := esmePassword
	esme := map[string]any{"password": &pw, "allowed_networks": []string{netip.PrefixFrom(local, local.BitLen()).String()}}

	if code := status(t, token, http.MethodPut, api+"/esmes/"+esmeSystemID, esme); code == http.StatusNotFound {
		esme["system_id"], esme["user"] = esmeSystemID, *email
		call(t, token, http.MethodPost, api+"/esmes", esme)
	} else if code/100 != 2 {
		t.Fatalf("PUT %s/esmes/%s: %d", api, esmeSystemID, code)
	}
}

type esme struct {
	clients []*smpp.Client

	mu       sync.Mutex
	byMsgID  map[string]int64
	receipts map[int64]time.Duration
	early    map[string]time.Time
	states   map[string]string
}

func newESME(t *testing.T, addr string) *esme {
	t.Helper()

	e := &esme{byMsgID: map[string]int64{}, receipts: map[int64]time.Duration{}, early: map[string]time.Time{}, states: map[string]string{}}

	for range *sessions {
		cl, err := smpp.NewClient(smpp.ClientConfig{
			Addr:    addr,
			Bind:    smpp.Bind{ID: smpp.CmdBindTransceiver, SystemID: esmeSystemID, Password: esmePassword},
			Session: smpp.SessionConfig{OutboundWindow: *smppWindow, InboundWindow: 64},
			Handler: smpp.HandlerFunc(e.serve),
		})
		if err != nil {
			t.Fatal(err)
		}

		cl.Start()

		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_ = cl.Close(ctx)
		})

		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		err = cl.WaitBound(ctx)

		cancel()

		if err != nil {
			t.Fatalf("bind: %v: %v", err, cl.State().LastError)
		}

		e.clients = append(e.clients, cl)
	}

	return e
}

func (e *esme) serve(_ context.Context, _ *smpp.Session, req smpp.Request) smpp.Response {
	d, ok := req.PDU.(*smpp.DeliverSM)
	if !ok {
		return smpp.Response{Status: smpp.StatusInvCmdID}
	}

	now := time.Now()

	if msgID, ok := receiptID(d.ShortMessage); ok {
		e.mu.Lock()
		e.early[msgID] = now
		e.states[msgID] = receiptState(d.ShortMessage)
		e.mu.Unlock()
	}

	return smpp.Response{Status: smpp.StatusOK, PDU: &smpp.DeliverSMResp{}}
}

func receiptState(text []byte) string {
	_, s, _ := strings.Cut(string(text), " stat:")
	state, _, _ := strings.Cut(s, " ")

	return state
}

func receiptID(text []byte) (string, bool) {
	s, ok := strings.CutPrefix(string(text), "id:")
	if !ok {
		return "", false
	}

	id, _, _ := strings.Cut(s, " ")

	return id, id != ""
}

func (e *esme) run(t *testing.T, c *core, api string, rate float64) {
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

		cl := e.clients[i%len(e.clients)]

		wg.Go(func() {
			to := rand.IntN(*subs)
			id := c.next.Add(1)
			began := time.Now()

			c.mu.Lock()
			c.sent[id] = began
			c.mu.Unlock()

			msgID, err := e.submit(cl, to, id, *withReceipts)
			if err != nil {
				submitted.fail(err)
				return
			}

			submitted.add(time.Since(began))

			e.mu.Lock()
			e.byMsgID[msgID] = id
			e.mu.Unlock()

			idsMu.Lock()

			ids = append(ids, id)

			idsMu.Unlock()
		})
	}

	wg.Wait()

	elapsed := time.Since(started)
	m := scrape(t, api)

	for deadline := time.Now().Add(*drain); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if c.deliveredOf(ids) == len(ids) && (!*withReceipts || e.receiptsOf(c, ids) == len(ids)) {
			break
		}
	}

	var delivered, receipted latencies

	c.mu.Lock()

	for _, id := range ids {
		if d, ok := c.delivered[id]; ok {
			delivered.add(d)
		}
	}

	c.mu.Unlock()

	if *withReceipts {
		e.receiptsOf(c, ids)

		e.mu.Lock()

		for _, id := range ids {
			if d, ok := e.receipts[id]; ok {
				receipted.add(d)
			}
		}

		e.mu.Unlock()
	}

	submitted.report(t, fmt.Sprintf("submit_sm at %4.0f/s", rate), n, elapsed)
	delivered.report(t, fmt.Sprintf("delivered at %4.0f/s", rate), len(ids), 0)

	if *withReceipts {
		receipted.report(t, fmt.Sprintf("receipted at %4.0f/s", rate), len(ids), 0)
	}

	t.Logf("  SMSC: %.0f%% of a CPU, %.1f MiB resident",
		100*(m["process_cpu_seconds_total"]-cpu0)/elapsed.Seconds(), m["process_resident_memory_bytes"]/(1<<20))
}

func (e *esme) receiptsOf(c *core, ids []int64) int {
	c.mu.Lock()
	sent := make(map[int64]time.Time, len(ids))

	for _, id := range ids {
		sent[id] = c.sent[id]
	}

	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	for msgID, at := range e.early {
		if id, ok := e.byMsgID[msgID]; ok {
			if _, done := e.receipts[id]; !done {
				e.receipts[id] = at.Sub(sent[id])
			}

			delete(e.early, msgID)
		}
	}

	got := 0

	for _, id := range ids {
		if _, ok := e.receipts[id]; ok {
			got++
		}
	}

	return got
}

// SMPP 3.4 section 4.4.1
func (e *esme) submit(cl *smpp.Client, to int, id int64, receipts bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var registered byte
	if receipts {
		registered = 0x01
	}

	return cl.SubmitSM(ctx, &smpp.SubmitSM{Message: smpp.Message{
		Source:             smpp.Address{TON: 1, NPI: 1, Addr: esmeSource},
		Destination:        smpp.Address{TON: 1, NPI: 1, Addr: msisdnOf(to)},
		RegisteredDelivery: registered,
		DataCoding:         0x04,
		ShortMessage:       []byte("load test #" + strconv.FormatInt(id, 10)),
	}})
}

func status(t *testing.T, token, method, url string, body any) int {
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

	_ = res.Body.Close()

	return res.StatusCode
}
