package server_test

// Performance benchmarks that run the SMSC and a fake core in the same process, over SCTP on the loopback.
//
// Run them all with a fixed number of messages, so that the percentiles of each run compare:
//
//	go test ./internal/server -run '^$' -bench . -benchtime 2000x
//
// Each benchmark reports its throughput in msg/s and, where it applies, latency percentiles in milliseconds.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/api"
	"github.com/ellanetworks/smsc/internal/db"
)

const perfTimeout = 2 * time.Minute

// population returns n subscribers with distinct IMSIs and MSISDNs.
func population(n int) []subscriber {
	subs := make([]subscriber, n)
	for i := range subs {
		subs[i] = subscriber{imsi: fmt.Sprintf("00101%010d", i), msisdn: fmt.Sprintf("1555%07d", i)}
	}

	return subs
}

func newPerfSMSC(b *testing.B, subs []subscriber) (*fakeCore, *smsc) {
	b.Helper()

	core := newFakeCore(b, subs...)
	s := startSMSC(b, testConfig(b, filepath.Join(b.TempDir(), "smsc.db")))
	core.connect(s)

	return core, s
}

// submitRequests builds n MO-Forward-Short-Message requests, spread round robin over the recipients, ahead of the
// timed section.
func (c *fakeCore) submitRequests(n int, from subscriber, to []subscriber) []*diameter.Message {
	reqs := make([]*diameter.Message, n)
	for i := range reqs {
		recipient := to[i%len(to)]
		reqs[i] = c.request(sgd.CommandMOForwardShortMessage, sgd.ApplicationID,
			diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, serviceCentreAddress)),
			diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
				diameter.UTF8String(diameter.AVPUserName, diameter.AVPFlagMandatory, 0, from.imsi),
				diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, from.msisdn))),
			diameter.OctetString(sgd.AVPSMRPUI, diameter.AVPFlagMandatory, tgpp.VendorID, smsSubmit(c.t, recipient.msisdn, fmt.Sprintf("perf %d", i))),
		)
	}

	return reqs
}

func (c *fakeCore) alertRequest(to subscriber) *diameter.Message {
	return c.request(s6c.CommandAlertServiceCentre, s6c.ApplicationID,
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, serviceCentreAddress)),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(c.t, to.msisdn))),
	)
}

// sendAll sends the requests to the SMSC from the given number of concurrent workers, and returns the latency of each.
func (c *fakeCore) sendAll(reqs []*diameter.Message, workers int) ([]time.Duration, error) {
	return runWorkers(len(reqs), workers, func(i int) error {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()

		ans, err := c.node.Send(ctx, reqs[i])
		if err != nil {
			return err
		}

		rc, ok := ans.Find(diameter.AVPResultCode, 0)
		if v, _ := rc.Unsigned32(); !ok || v != diameter.ResultSuccess {
			return fmt.Errorf("request %d: result code %d", i, v)
		}

		return nil
	})
}

func (c *fakeCore) deliveredCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.delivered)
}

// runWorkers calls f for each index below n from the given number of concurrent workers, and returns how long each
// call took. It stops at the first error.
func runWorkers(n, workers int, f func(i int) error) ([]time.Duration, error) {
	var (
		next     atomic.Int64
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		failed   atomic.Bool
	)

	took := make([]time.Duration, n)

	for range min(workers, n) {
		wg.Go(func() {
			for !failed.Load() {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}

				start := time.Now()

				if err := f(i); err != nil {
					errOnce.Do(func() { firstErr = err })
					failed.Store(true)

					return
				}

				took[i] = time.Since(start)
			}
		})
	}

	wg.Wait()

	return took, firstErr
}

func waitFor(b *testing.B, what string, cond func() bool) {
	b.Helper()

	deadline := time.Now().Add(perfTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			b.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(time.Millisecond)
	}
}

// waitForDelivered waits for the fake core to receive n messages, then for the SMSC to store them as delivered, and
// returns the messages.
func (s *smsc) waitForDelivered(b *testing.B, core *fakeCore, n int) []db.Message {
	b.Helper()

	waitFor(b, fmt.Sprintf("%d messages at the core", n), func() bool { return core.deliveredCount() >= n })

	var messages []db.Message

	waitFor(b, fmt.Sprintf("%d messages stored as delivered", n), func() bool {
		var (
			total int
			err   error
		)

		messages, total, err = s.store.ListMessages(context.Background(), db.MessageFilter{Status: db.StatusDelivered}, 1, n)
		if err != nil {
			b.Fatal(err)
		}

		return total >= n
	})

	return messages
}

// endToEnd returns how long each message took from its submission to its delivery, as the SMSC stored them.
func endToEnd(messages []db.Message) []time.Duration {
	out := make([]time.Duration, len(messages))
	for i, m := range messages {
		out[i] = m.UpdatedAt.Sub(m.SubmittedAt)
	}

	return out
}

func reportThroughput(b *testing.B, n int, elapsed time.Duration) {
	b.ReportMetric(float64(n)/elapsed.Seconds(), "msg/s")
}

func reportLatency(b *testing.B, prefix string, d []time.Duration) {
	if len(d) == 0 {
		return
	}

	sorted := slices.Clone(d)
	slices.Sort(sorted)

	at := func(q float64) float64 {
		return float64(sorted[min(int(q*float64(len(sorted))), len(sorted)-1)]) / float64(time.Millisecond)
	}

	b.ReportMetric(at(0.50), prefix+"-p50-ms")
	b.ReportMetric(at(0.99), prefix+"-p99-ms")
	b.ReportMetric(float64(sorted[len(sorted)-1])/float64(time.Millisecond), prefix+"-max-ms")
}

// BenchmarkMobileOriginatedIntake measures how fast the SMSC accepts messages that phones send, from the
// MO-Forward-Short-Message request to its answer, while it delivers them in the background.
func BenchmarkMobileOriginatedIntake(b *testing.B) {
	for _, workers := range []int{1, 16, 64} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			subs := population(1000)
			core, _ := newPerfSMSC(b, subs)
			reqs := core.submitRequests(b.N, subs[0], subs[1:])

			b.ResetTimer()

			start := time.Now()

			took, err := core.sendAll(reqs, workers)
			if err != nil {
				b.Fatal(err)
			}

			elapsed := time.Since(start)

			b.StopTimer()

			reportThroughput(b, b.N, elapsed)
			reportLatency(b, "ofr", took)
		})
	}
}

// BenchmarkMobileOriginatedToDelivered measures how fast messages that phones send reach their recipients, from the
// first submission until the SMSC stores the last one as delivered. The number of recipients bounds the parallelism,
// since the SMSC delivers to each recipient one message at a time, and the core latency is added to each routing
// lookup and each delivery.
func BenchmarkMobileOriginatedToDelivered(b *testing.B) {
	for _, recipients := range []int{1, 100, 1000} {
		for _, latency := range []time.Duration{0, 10 * time.Millisecond} {
			b.Run(fmt.Sprintf("recipients=%d/core-latency=%s", recipients, latency), func(b *testing.B) {
				subs := population(recipients + 1)
				core, s := newPerfSMSC(b, subs)
				core.delay.Store(int64(latency))
				reqs := core.submitRequests(b.N, subs[0], subs[1:])

				b.ResetTimer()

				start := time.Now()

				if _, err := core.sendAll(reqs, 64); err != nil {
					b.Fatal(err)
				}

				messages := s.waitForDelivered(b, core, b.N)
				elapsed := time.Since(start)

				b.StopTimer()

				reportThroughput(b, b.N, elapsed)
				reportLatency(b, "e2e", endToEnd(messages))
			})
		}
	}
}

// BenchmarkAlertDrainsHeldMessages measures how fast the SMSC delivers messages held for absent recipients once the
// core alerts it that they are reachable, as after a radio outage: one message for each of b.N recipients.
func BenchmarkAlertDrainsHeldMessages(b *testing.B) {
	subs := population(b.N + 1)
	core, s := newPerfSMSC(b, subs)

	for _, sub := range subs[1:] {
		core.setAbsent(sub.msisdn, true)
	}

	if _, err := core.sendAll(core.submitRequests(b.N, subs[0], subs[1:]), 64); err != nil {
		b.Fatal(err)
	}

	waitFor(b, "every recipient to be held", func() bool {
		counts, err := s.store.CountPending(context.Background(), time.Now())
		if err != nil {
			b.Fatal(err)
		}

		return counts[db.KindMessage].Waiting == b.N
	})

	alerts := make([]*diameter.Message, b.N)

	for i, sub := range subs[1:] {
		core.setAbsent(sub.msisdn, false)
		alerts[i] = core.alertRequest(sub)
	}

	b.ResetTimer()

	start := time.Now()

	took, err := core.sendAll(alerts, 64)
	if err != nil {
		b.Fatal(err)
	}

	s.waitForDelivered(b, core, b.N)

	elapsed := time.Since(start)

	b.StopTimer()

	reportThroughput(b, b.N, elapsed)
	reportLatency(b, "ala", took)
}

// BenchmarkAPISubmit measures how fast the SMSC accepts messages over the HTTP API, while it delivers them in the
// background.
func BenchmarkAPISubmit(b *testing.B) {
	const workers = 16

	subs := population(1000)
	_, s := newPerfSMSC(b, subs)

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: workers}}
	url := "http://" + s.server.APIAddr().String() + "/api/v1/messages"

	bodies := make([][]byte, b.N)
	for i := range bodies {
		body, err := json.Marshal(api.CreateMessageParams{From: "+15550001111", To: "+" + subs[i%len(subs)].msisdn, Text: fmt.Sprintf("perf %d", i)})
		if err != nil {
			b.Fatal(err)
		}

		bodies[i] = body
	}

	b.ResetTimer()

	start := time.Now()

	took, err := runWorkers(b.N, workers, func(i int) error {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bodies[i]))
		if err != nil {
			return err
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+s.token)

		resp, err := client.Do(req)
		if err != nil {
			return err
		}

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("POST %s = %d", url, resp.StatusCode)
		}

		return nil
	})
	if err != nil {
		b.Fatal(err)
	}

	elapsed := time.Since(start)

	b.StopTimer()

	reportThroughput(b, b.N, elapsed)
	reportLatency(b, "post", took)
}
