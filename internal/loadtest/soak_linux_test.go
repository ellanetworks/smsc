//go:build linux

package loadtest

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"path/filepath"
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
	"github.com/ellanetworks/core/diameter/tgpp"
)

var (
	soak        = flag.Bool("soak", false, "soak the SMSC at -smsc: a steady load with faults, sampled over time")
	soakRate    = flag.Float64("soak-rate", 1000, "the short messages per second from phones")
	esmeRate    = flag.Float64("esme-rate", 100, "the submit_sm per second from the ESME, each asking for a delivery receipt")
	soakFor     = flag.Duration("duration", 45*time.Minute, "how long the load lasts")
	warmUp      = flag.Duration("warmup", 10*time.Minute, "the start of the load left out of the trends")
	sampleEvery = flag.Duration("sample", time.Minute, "how often to sample each node")
	validity    = flag.Duration("validity", 5*time.Minute, "the validity period of the short messages")
	dataDirs    = flag.String("data-dirs", "", "the data directories of the SMSC's nodes, comma-separated, in -smsc order")
)

const absentCycle = 4 * time.Minute

func neverReachable(sub int) bool  { return sub%100 == 7 }
func sometimesAbsent(sub int) bool { return sub%10 == 1 }
func busyOnce(id int64) bool       { return id%50 == 1 }
func illegal(id int64) bool        { return id%200 == 3 }

func deliverable(to int, id int64) bool { return !neverReachable(to) && !illegal(id) }

func clean(to int, id int64) bool {
	return !neverReachable(to) && !sometimesAbsent(to) && !busyOnce(id) && !illegal(id)
}

type soaker struct {
	c     *core
	e     *esme
	start time.Time

	mu        sync.Mutex
	to        map[int64]int
	submitted map[int64]time.Duration
	mo, smpp  []int64
	failed    []error
	busy      map[int64]bool
	waiting   map[int]bool

	absentAnswers, busyAnswers, illegalAnswers, alerts, alertErrors atomic.Int64
}

func TestSoak(t *testing.T) {
	if !*soak {
		t.Skip("run with -soak")
	}

	if minValidity := absentCycle/2 + 3*time.Minute; *validity < minValidity {
		t.Fatalf("-validity must be at least %s, for messages to outlast a busy answer and an absence", minValidity)
	}

	nodes := smscNodes()
	local := netip.MustParseAddr(*localAddr)
	api := nodes[0].api

	token := logIn(t, api)

	for i := range nodes {
		nodes[i].host = diameterHost(t, token, nodes[i].api)
	}

	call(t, token, http.MethodPut, api+"/operator", map[string]any{
		"mcc": "001", "mnc": "01", "service_centre_address": "+" + scAddress, "numbering": map[string]any{"country_code": "1"},
	})
	call(t, token, http.MethodPut, api+"/delivery", map[string]any{
		"default_validity_seconds": int(validity.Seconds()), "retry_intervals_seconds": []int{60},
	})
	ensureESME(t, token, api, local)

	s := &soaker{
		to: map[int64]int{}, submitted: map[int64]time.Duration{}, busy: map[int64]bool{}, waiting: map[int]bool{},
	}
	s.c = newCore(t, local, nodes)
	s.c.mt, s.c.report = s.answerMT, s.reported
	s.e = newESME(t, netip.AddrPortFrom(nodes[0].addr, uint16(*smppPort)).String())
	s.start = time.Now()

	alerting, stopAlerting := context.WithCancel(context.Background())
	defer stopAlerting()

	go s.alert(alerting)

	loading, stopLoading := context.WithTimeout(context.Background(), *soakFor)
	defer stopLoading()

	var load sync.WaitGroup

	load.Go(func() { s.pace(loading, *soakRate, s.submitMO) })
	load.Go(func() { s.pace(loading, *esmeRate, s.submitSMPP) })

	var samples [][]sample

	for tick := time.NewTicker(*sampleEvery); ; {
		samples = append(samples, s.sample(t, nodes))
		s.logSample(t, samples[len(samples)-1])

		select {
		case <-tick.C:
			continue
		case <-loading.Done():
			tick.Stop()
		}

		break
	}

	load.Wait()

	s.drain(t)
	stopAlerting()

	samples = append(samples, s.sample(t, nodes))
	s.logSample(t, samples[len(samples)-1])

	s.checkOutcomes(t)
	s.checkLatency(t)
	checkTrends(t, nodes, samples)
}

func (s *soaker) pace(ctx context.Context, rate float64, submit func()) {
	if rate <= 0 {
		return
	}

	var wg sync.WaitGroup

	started := time.Now()

	for i := 0; ctx.Err() == nil; i++ {
		if d := time.Until(started.Add(time.Duration(float64(i) / rate * float64(time.Second)))); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				continue
			}
		}

		wg.Go(submit)
	}

	wg.Wait()
}

func (s *soaker) record(id int64, to int, smppSubmit bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.to[id] = to

	if smppSubmit {
		s.smpp = append(s.smpp, id)
	} else {
		s.mo = append(s.mo, id)
	}
}

func (s *soaker) result(id int64, began time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.failed = append(s.failed, fmt.Errorf("message %d: %w", id, err))
		return
	}

	s.submitted[id] = time.Since(began)
}

func (s *soaker) submitMO() {
	from := rand.IntN(*subs)
	to := (from + 1 + rand.IntN(*subs-1)) % *subs
	id := s.c.next.Add(1)
	began := time.Now()

	s.c.mu.Lock()
	s.c.sent[id] = began
	s.c.mu.Unlock()

	s.record(id, to, false)
	s.result(id, began, s.c.submit(int(id)%len(s.c.smscs), from, to, id))
}

func (s *soaker) submitSMPP() {
	to := rand.IntN(*subs)
	id := s.c.next.Add(1)
	began := time.Now()

	s.c.mu.Lock()
	s.c.sent[id] = began
	s.c.mu.Unlock()

	s.record(id, to, true)

	msgID, err := s.e.submit(s.e.clients[int(id)%len(s.e.clients)], to, id, true)
	if err == nil {
		s.e.mu.Lock()
		s.e.byMsgID[msgID] = id
		s.e.mu.Unlock()
	}

	s.result(id, began, err)
}

func (s *soaker) absent(sub int, now time.Time) bool {
	if neverReachable(sub) {
		return true
	}

	if !sometimesAbsent(sub) {
		return false
	}

	offset := time.Duration(sub%240) * time.Second

	return (now.Sub(s.start)+offset)%absentCycle < absentCycle/2
}

// TS 29.338 clause 6.2.2
func (s *soaker) answerMT(imsi string, id int64, req *diameter.Message) *diameter.Message {
	sub, err := strconv.Atoi(strings.TrimPrefix(imsi, "00101"))
	if err != nil {
		return nil
	}

	identity := s.c.node.Identity()

	switch {
	case s.absent(sub, time.Now()):
		s.absentAnswers.Add(1)

		d := tgpp.AbsentUserIMSIDetached

		return sgd.NewAbsentUserAnswer(req, identity, &d)
	case illegal(id):
		s.illegalAnswers.Add(1)

		return tgpp.NewExperimentalAnswer(req, identity, tgpp.ResultErrorIllegalUser)
	case busyOnce(id):
		s.mu.Lock()
		first := !s.busy[id]
		s.busy[id] = true
		s.mu.Unlock()

		if first {
			s.busyAnswers.Add(1)

			return tgpp.NewExperimentalAnswer(req, identity, tgpp.ResultErrorUserBusyForMTSMS)
		}
	}

	return nil
}

// TS 29.338 clause 5.3.2.7
func (s *soaker) reported(req *diameter.Message) {
	rep, err := s6c.ParseReportSMDeliveryStatusRequest(req)
	if err != nil || rep.MME == nil {
		return
	}

	sub, err := strconv.Atoi(strings.TrimPrefix(rep.MSISDN, "1555"))
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if rep.MME.Cause == s6c.DeliveryCauseAbsentUser {
		s.waiting[sub] = true
	} else {
		delete(s.waiting, sub)
	}
}

// TS 29.338 clause 5.3.2.5
func (s *soaker) alert(ctx context.Context) {
	for tick := time.NewTicker(time.Second); ; {
		select {
		case <-ctx.Done():
			tick.Stop()
			return
		case <-tick.C:
		}

		now := time.Now()

		s.mu.Lock()

		var ready []int

		for sub := range s.waiting {
			if !s.absent(sub, now) {
				ready = append(ready, sub)
				delete(s.waiting, sub)
			}
		}

		s.mu.Unlock()

		for _, sub := range ready {
			via := rand.IntN(len(s.c.smscs))
			req := s.c.request(via, s6c.CommandAlertServiceCentre, s6c.ApplicationID,
				diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(scAddress)),
				diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
					diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, mustTBCD(msisdnOf(sub)))),
			)

			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			ans, err := s.c.node.Do(c, "smsc"+strconv.Itoa(via), req)

			cancel()

			if err != nil || resultOf(ans) != diameter.ResultSuccess {
				s.alertErrors.Add(1)
				continue
			}

			s.alerts.Add(1)
		}
	}
}

func (s *soaker) drain(t *testing.T) {
	t.Helper()

	deadline := time.Now().Add(max(*drain, *validity+3*time.Minute))

	for {
		undelivered, unreceipted := s.outstanding()
		if undelivered == 0 && unreceipted == 0 {
			t.Logf("drained in %s", time.Since(deadline.Add(-max(*drain, *validity+3*time.Minute))).Round(time.Second))
			return
		}

		if time.Now().After(deadline) {
			t.Logf("not drained: %d messages undelivered, %d receipts missing", undelivered, unreceipted)
			return
		}

		time.Sleep(5 * time.Second)
	}
}

func (s *soaker) outstanding() (undelivered, unreceipted int) {
	s.mu.Lock()
	ids := slices.Concat(s.mo, s.smpp)
	smppIDs := slices.Clone(s.smpp)
	to := make(map[int64]int, len(ids))

	for _, id := range ids {
		to[id] = s.to[id]
	}

	submitted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		_, submitted[id] = s.submitted[id]
	}

	s.mu.Unlock()

	s.c.mu.Lock()

	for _, id := range ids {
		if _, ok := s.c.delivered[id]; submitted[id] && deliverable(to[id], id) && !ok {
			undelivered++
		}
	}

	s.c.mu.Unlock()

	s.e.receiptsOf(s.c, smppIDs)

	s.e.mu.Lock()

	for _, id := range smppIDs {
		if _, ok := s.e.receipts[id]; submitted[id] && !ok {
			unreceipted++
		}
	}

	s.e.mu.Unlock()

	return undelivered, unreceipted
}

func (s *soaker) checkOutcomes(t *testing.T) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	s.e.mu.Lock()
	defer s.e.mu.Unlock()

	states := make(map[int64]string, len(s.e.byMsgID))
	for msgID, id := range s.e.byMsgID {
		states[id] = s.e.states[msgID]
	}

	var undelivered, wronglyDelivered, missingReceipts, wrongReceipts int

	tally := map[string]int{}

	for _, id := range slices.Concat(s.mo, s.smpp) {
		if _, ok := s.submitted[id]; !ok {
			continue
		}

		_, delivered := s.c.delivered[id]

		switch want := deliverable(s.to[id], id); {
		case want && !delivered:
			undelivered++
		case !want && delivered:
			wronglyDelivered++
		}
	}

	for _, id := range s.smpp {
		if _, ok := s.submitted[id]; !ok {
			continue
		}

		state, ok := states[id]
		if _, got := s.e.receipts[id]; !got || !ok {
			missingReceipts++
			continue
		}

		tally[state]++

		if (state == "DELIVRD") != deliverable(s.to[id], id) {
			wrongReceipts++
		}
	}

	t.Logf("from phones: %d submitted; from the ESME: %d submitted; %d submissions failed",
		len(s.mo), len(s.smpp), len(s.failed))
	t.Logf("core answers: %d absent, %d busy, %d illegal user; %d alerts sent, %d failed",
		s.absentAnswers.Load(), s.busyAnswers.Load(), s.illegalAnswers.Load(), s.alerts.Load(), s.alertErrors.Load())
	t.Logf("receipts: %v", tally)

	for _, err := range s.failed[:min(len(s.failed), 5)] {
		t.Errorf("submission failed: %v", err)
	}

	if len(s.failed) > 0 {
		t.Errorf("%d submissions failed", len(s.failed))
	}

	if undelivered > 0 {
		t.Errorf("%d deliverable messages not delivered", undelivered)
	}

	if wronglyDelivered > 0 {
		t.Errorf("%d undeliverable messages delivered", wronglyDelivered)
	}

	if s.c.duplicates > 0 {
		t.Errorf("%d messages delivered more than once", s.c.duplicates)
	}

	if missingReceipts > 0 {
		t.Errorf("%d delivery receipts missing", missingReceipts)
	}

	if wrongReceipts > 0 {
		t.Errorf("%d delivery receipts with the wrong state", wrongReceipts)
	}
}

func (s *soaker) checkLatency(t *testing.T) {
	t.Helper()

	type window struct{ submit, deliver latencies }

	windows := make([]window, int(*soakFor / *sampleEvery)+1)

	s.mu.Lock()
	s.c.mu.Lock()

	for _, id := range s.mo {
		sub, ok := s.submitted[id]
		if !ok || !clean(s.to[id], id) {
			continue
		}

		w := &windows[min(len(windows)-1, int(s.c.sent[id].Sub(s.start) / *sampleEvery))]
		w.submit.add(sub)

		if d, ok := s.c.delivered[id]; ok {
			w.deliver.add(d)
		}
	}

	s.c.mu.Unlock()
	s.mu.Unlock()

	var first, last *window

	for i := range windows {
		w := &windows[i]
		if len(w.deliver.d) == 0 {
			continue
		}

		t.Logf("%6s: submit p99 %6s, delivery p99 %6s, over %d clean messages",
			time.Duration(i)**sampleEvery, w.submit.at(0.99).Round(time.Millisecond), w.deliver.at(0.99).Round(time.Millisecond), len(w.deliver.d))

		if time.Duration(i)**sampleEvery >= *warmUp {
			if first == nil {
				first = w
			}

			last = w
		}
	}

	if first == nil || first == last {
		return
	}

	for _, c := range []struct {
		what        string
		first, last time.Duration
	}{
		{"submit", first.submit.at(0.99), last.submit.at(0.99)},
		{"delivery", first.deliver.at(0.99), last.deliver.at(0.99)},
	} {
		if c.last > 2*c.first+50*time.Millisecond {
			t.Errorf("%s p99 grew from %s to %s", c.what, c.first.Round(time.Millisecond), c.last.Round(time.Millisecond))
		}
	}
}

type sample struct {
	at                                   time.Duration
	rss, heap, goroutines, fds           float64
	storage, disk, pending, notification float64
}

func (s *soaker) sample(t *testing.T, nodes []smscNode) []sample {
	t.Helper()

	var dirs []string
	if *dataDirs != "" {
		dirs = strings.Split(*dataDirs, ",")
	}

	out := make([]sample, len(nodes))

	for i, n := range nodes {
		m := scrape(t, n.api)
		out[i] = sample{
			at: time.Since(s.start), rss: m["process_resident_memory_bytes"], heap: m["go_memstats_heap_inuse_bytes"],
			goroutines: m["go_goroutines"], fds: m["process_open_fds"], storage: m["ellasmsc_database_storage_bytes"],
			pending: m["ellasmsc_messages_pending"], notification: m["ellasmsc_smpp_notifications_pending"],
		}

		if i < len(dirs) {
			out[i].disk = dirSize(strings.TrimSpace(dirs[i]))
		}
	}

	return out
}

func dirSize(dir string) float64 {
	var n int64

	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}

		return nil
	})

	return float64(n)
}

func (s *soaker) logSample(t *testing.T, ss []sample) {
	t.Helper()

	s.c.mu.Lock()
	delivered := len(s.c.delivered)
	s.c.mu.Unlock()

	t.Logf("%s: %d delivered", ss[0].at.Round(time.Second), delivered)

	for i, x := range ss {
		t.Logf("  node %d: rss %4.0f MiB, heap %4.0f MiB, %5.0f goroutines, %4.0f fds, db %5.0f MiB, disk %5.0f MiB, %6.0f pending, %5.0f receipts pending",
			i, x.rss/(1<<20), x.heap/(1<<20), x.goroutines, x.fds, x.storage/(1<<20), x.disk/(1<<20), x.pending, x.notification)
	}
}

func checkTrends(t *testing.T, nodes []smscNode, samples [][]sample) {
	t.Helper()

	var steady [][]sample

	for _, ss := range samples[:len(samples)-1] {
		if ss[0].at >= *warmUp {
			steady = append(steady, ss)
		}
	}

	if len(steady) < 4 {
		t.Logf("too few samples after the warm-up for trends")
		return
	}

	q := len(steady) / 4
	hours := (steady[len(steady)-1][0].at - steady[0][0].at).Hours()

	for i := range nodes {
		mean := func(ss [][]sample, f func(sample) float64) float64 {
			var sum float64
			for _, x := range ss {
				sum += f(x[i])
			}

			return sum / float64(len(ss))
		}

		for _, c := range []struct {
			what  string
			f     func(sample) float64
			limit func(first float64) float64
			unit  float64
		}{
			{"goroutines", func(x sample) float64 { return x.goroutines }, func(f float64) float64 { return f*1.1 + 20 }, 1},
			{"heap in use (MiB)", func(x sample) float64 { return x.heap }, func(f float64) float64 { return f*1.25 + 16<<20 }, 1 << 20},
			{"open fds", func(x sample) float64 { return x.fds }, func(f float64) float64 { return f + 20 }, 1},
			{"pending messages", func(x sample) float64 { return x.pending }, func(f float64) float64 { return f*1.5 + 1000 }, 1},
			{"pending receipts", func(x sample) float64 { return x.notification }, func(f float64) float64 { return f*1.5 + 1000 }, 1},
			{"resident memory (MiB)", func(x sample) float64 { return x.rss }, nil, 1 << 20},
			{"database (MiB)", func(x sample) float64 { return x.storage }, nil, 1 << 20},
			{"disk (MiB)", func(x sample) float64 { return x.disk }, nil, 1 << 20},
		} {
			first, last := mean(steady[:q], c.f), mean(steady[len(steady)-q:], c.f)
			t.Logf("node %d %s: %.0f -> %.0f, %+.0f per hour", i, c.what, first/c.unit, last/c.unit, (last-first)/c.unit/hours)

			if c.limit != nil && last > c.limit(first) {
				t.Errorf("node %d %s grew from %.0f to %.0f", i, c.what, first/c.unit, last/c.unit)
			}
		}
	}
}
