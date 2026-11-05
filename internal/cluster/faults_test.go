package cluster

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/delivery"
	"github.com/ellanetworks/smsc/internal/settings"
	"github.com/hashicorp/raft"
)

const (
	faultTPDU = "11" + "07" + "0b91" + "5155210300f2" + "00" + "00" + "aa" + "05" + "e8329bfd06"
	mmeSuffix = ".mme.example.org"
)

var (
	errPartitioned = errors.New("partitioned")
	errReleased    = errors.New("the hung log store was released")
)

type partition struct {
	isolated atomic.Bool
}

func (p *partition) isolate() { p.isolated.Store(true) }

func (p *partition) heal() { p.isolated.Store(false) }

func (p *partition) wrap(t raft.Transport) raft.Transport {
	pt := &partitionedTransport{Transport: t, p: p, consumer: make(chan raft.RPC), done: make(chan struct{})}

	go pt.filter()

	return pt
}

type partitionedTransport struct {
	raft.Transport
	p        *partition
	consumer chan raft.RPC
	done     chan struct{}
	once     sync.Once
}

func (t *partitionedTransport) filter() {
	for {
		select {
		case <-t.done:
			return
		case rpc := <-t.Transport.Consumer():
			if t.dropped(rpc) {
				continue
			}

			select {
			case t.consumer <- rpc:
			case <-t.done:
				return
			}
		}
	}
}

func (t *partitionedTransport) dropped(rpc raft.RPC) bool {
	if !t.p.isolated.Load() {
		return false
	}

	if rpc.Reader != nil {
		_, _ = io.Copy(io.Discard, rpc.Reader)
	}

	rpc.Respond(nil, errPartitioned)

	return true
}

func (t *partitionedTransport) Consumer() <-chan raft.RPC { return t.consumer }

func (t *partitionedTransport) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	if cb == nil {
		t.Transport.SetHeartbeatHandler(nil)
		return
	}

	t.Transport.SetHeartbeatHandler(func(rpc raft.RPC) {
		if !t.dropped(rpc) {
			cb(rpc)
		}
	})
}

func (t *partitionedTransport) AppendEntriesPipeline(id raft.ServerID, target raft.ServerAddress) (raft.AppendPipeline, error) {
	if t.p.isolated.Load() {
		return nil, errPartitioned
	}

	pipeline, err := t.Transport.AppendEntriesPipeline(id, target)
	if err != nil {
		return nil, err
	}

	return partitionedPipeline{AppendPipeline: pipeline, p: t.p}, nil
}

func (t *partitionedTransport) AppendEntries(id raft.ServerID, target raft.ServerAddress, args *raft.AppendEntriesRequest,
	resp *raft.AppendEntriesResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.AppendEntries(id, target, args, resp)
}

func (t *partitionedTransport) RequestVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestVoteRequest,
	resp *raft.RequestVoteResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.RequestVote(id, target, args, resp)
}

func (t *partitionedTransport) RequestPreVote(id raft.ServerID, target raft.ServerAddress, args *raft.RequestPreVoteRequest,
	resp *raft.RequestPreVoteResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.(raft.WithPreVote).RequestPreVote(id, target, args, resp)
}

func (t *partitionedTransport) InstallSnapshot(id raft.ServerID, target raft.ServerAddress, args *raft.InstallSnapshotRequest,
	resp *raft.InstallSnapshotResponse, data io.Reader,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.InstallSnapshot(id, target, args, resp, data)
}

func (t *partitionedTransport) TimeoutNow(id raft.ServerID, target raft.ServerAddress, args *raft.TimeoutNowRequest,
	resp *raft.TimeoutNowResponse,
) error {
	if t.p.isolated.Load() {
		return errPartitioned
	}

	return t.Transport.TimeoutNow(id, target, args, resp)
}

func (t *partitionedTransport) Close() error {
	t.once.Do(func() { close(t.done) })

	if c, ok := t.Transport.(raft.WithClose); ok {
		return c.Close()
	}

	return nil
}

type partitionedPipeline struct {
	raft.AppendPipeline
	p *partition
}

func (p partitionedPipeline) AppendEntries(args *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) (raft.AppendFuture, error) {
	if p.p.isolated.Load() {
		return nil, errPartitioned
	}

	return p.AppendPipeline.AppendEntries(args, resp)
}

type hangingLogStore struct {
	hung    atomic.Bool
	release chan struct{}
	once    sync.Once
}

func newHangingLogStore() *hangingLogStore {
	return &hangingLogStore{release: make(chan struct{})}
}

func (h *hangingLogStore) hang() { h.hung.Store(true) }

func (h *hangingLogStore) unhang() { h.once.Do(func() { close(h.release) }) }

func (h *hangingLogStore) wrap(s raft.LogStore) raft.LogStore { return hangingLogs{LogStore: s, h: h} }

type hangingLogs struct {
	raft.LogStore
	h *hangingLogStore
}

func (l hangingLogs) StoreLog(e *raft.Log) error { return l.StoreLogs([]*raft.Log{e}) }

func (l hangingLogs) StoreLogs(logs []*raft.Log) error {
	if l.h.hung.Load() {
		<-l.h.release
		return errReleased
	}

	return l.LogStore.StoreLogs(logs)
}

type skewedClock struct {
	offset atomic.Int64
}

func (c *skewedClock) now() time.Time {
	return time.Now().Add(time.Duration(c.offset.Load()))
}

type transfer struct {
	node   string
	msisdn string
	start  time.Time
	end    time.Time
	ok     bool
}

type fakeCore struct {
	mu        sync.Mutex
	inflight  map[string]int
	overlaps  []string
	transfers []transfer
	routings  []transfer
	tfrGates  map[string]chan struct{}
	sriGates  map[string]chan struct{}
	absent    map[string]int
	latency   time.Duration
	sessions  atomic.Int64
}

func newFakeCore() *fakeCore {
	return &fakeCore{
		inflight: map[string]int{},
		tfrGates: map[string]chan struct{}{},
		sriGates: map[string]chan struct{}{},
		absent:   map[string]int{},
	}
}

func (c *fakeCore) holdTransfer(msisdn string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	gate := make(chan struct{})
	c.tfrGates[msisdn] = gate

	return gate
}

func (c *fakeCore) holdRouting(msisdn string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	gate := make(chan struct{})
	c.sriGates[msisdn] = gate

	return gate
}

func (c *fakeCore) answerAbsent(msisdn string, times int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.absent[msisdn] = times
}

func (c *fakeCore) snapshot() ([]transfer, []transfer, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]transfer(nil), c.transfers...), append([]transfer(nil), c.routings...), append([]string(nil), c.overlaps...)
}

func (c *fakeCore) waitTransfer(t testing.TB, node, msisdn string) {
	t.Helper()

	eventually(t, "a forward to "+msisdn+" from "+node, func() error {
		transfers, _, _ := c.snapshot()

		for _, tr := range transfers {
			if tr.node == node && tr.msisdn == msisdn {
				return nil
			}
		}

		return errors.New("none yet")
	})
}

func (c *fakeCore) waitRouting(t testing.TB, node, msisdn string) {
	t.Helper()

	eventually(t, "a routing lookup of "+msisdn+" from "+node, func() error {
		_, routings, _ := c.snapshot()

		for _, r := range routings {
			if r.node == node && r.msisdn == msisdn {
				return nil
			}
		}

		return errors.New("none yet")
	})
}

func (c *fakeCore) check(t testing.TB) {
	t.Helper()

	if _, _, overlaps := c.snapshot(); len(overlaps) > 0 {
		t.Fatalf("overlapping MT-Forward-Short-Message per recipient: %v", overlaps)
	}
}

func (c *fakeCore) forward(ctx context.Context, node, msisdn string) (*diameter.Message, error) {
	c.mu.Lock()

	now := time.Now()

	if c.inflight[msisdn] > 0 {
		c.overlaps = append(c.overlaps, fmt.Sprintf("%s from %s at %s", msisdn, node, now.Format(time.StampMicro)))
	}

	c.inflight[msisdn]++
	i := len(c.transfers)
	c.transfers = append(c.transfers, transfer{node: node, msisdn: msisdn, start: now})

	gate := c.tfrGates[msisdn]
	delete(c.tfrGates, msisdn)

	absent := c.absent[msisdn] > 0
	if absent {
		c.absent[msisdn]--
	}

	latency := c.latency
	c.mu.Unlock()

	err := wait(ctx, gate, latency)

	c.mu.Lock()
	c.inflight[msisdn]--
	c.transfers[i].end = time.Now()
	c.transfers[i].ok = err == nil && !absent
	c.mu.Unlock()

	switch {
	case err != nil:
		return nil, err
	case absent:
		return experimentalAnswer(tgpp.ResultErrorAbsentUser), nil
	default:
		return successAnswer(), nil
	}
}

func (c *fakeCore) route(ctx context.Context, node, msisdn string) (s6c.Routing, error) {
	c.mu.Lock()
	c.routings = append(c.routings, transfer{node: node, msisdn: msisdn, start: time.Now()})
	gate := c.sriGates[msisdn]
	delete(c.sriGates, msisdn)
	c.mu.Unlock()

	if err := wait(ctx, gate, 0); err != nil {
		return s6c.Routing{}, err
	}

	return s6c.Routing{
		IMSI: "00101" + msisdn[len(msisdn)-10:],
		ServingNodes: s6c.ServingNodes{
			Serving: &s6c.ServingNode{MME: &s6c.NodeAddress{Name: msisdn + mmeSuffix, Realm: "epc.example.org", Number: "15550000010"}},
		},
	}, nil
}

func wait(ctx context.Context, gate chan struct{}, latency time.Duration) error {
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func successAnswer() *diameter.Message {
	return &diameter.Message{AVPs: []diameter.AVP{
		diameter.Unsigned32(diameter.AVPResultCode, diameter.AVPFlagMandatory, 0, diameter.ResultSuccess),
	}}
}

func experimentalAnswer(code uint32) *diameter.Message {
	return &diameter.Message{AVPs: []diameter.AVP{
		diameter.Grouped(diameter.AVPExperimentalResult, diameter.AVPFlagMandatory, 0,
			diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
			diameter.Unsigned32(diameter.AVPExperimentalResultCode, diameter.AVPFlagMandatory, 0, code),
		),
	}}
}

type coreRouter struct {
	core *fakeCore
	node string
}

func (r coreRouter) SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (s6c.Routing, string, error) {
	routing, err := r.core.route(ctx, r.node, req.MSISDN)

	return routing, "hss.example.org", err
}

func (r coreRouter) ReportSMDeliveryStatus(context.Context, s6c.DeliveryReport) (s6c.ReportResult, error) {
	return s6c.ReportResult{}, nil
}

type coreSender struct {
	core *fakeCore
	node string
}

func (s coreSender) Do(ctx context.Context, peer string, _ *diameter.Message) (*diameter.Message, error) {
	return s.core.forward(ctx, s.node, strings.TrimSuffix(peer, mmeSuffix))
}

func (s coreSender) PeerRealm(string) (string, bool) { return "epc.example.org", true }

func (s coreSender) NewSessionID() string {
	return fmt.Sprintf("%s.smsc.example.org;1;%d", s.node, s.core.sessions.Add(1))
}

type faultOptions struct {
	attemptTimeout  time.Duration
	retry           []time.Duration
	offsets         []time.Duration
	logStoreTimeout time.Duration
}

type faultNode struct {
	*env
	name      string
	address   string
	clock     *skewedClock
	deliverer *delivery.Deliverer
	part      *partition
	logs      *hangingLogStore

	mu    sync.Mutex
	terms []uint64
}

func (n *faultNode) leading() bool {
	ok, _ := n.node.Leader()
	return ok
}

func (n *faultNode) ledTerms() []uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]uint64(nil), n.terms...)
}

type faultCluster struct {
	*testCluster
	core  *fakeCore
	nodes []*faultNode
}

func newFaultCluster(t *testing.T, opts faultOptions) *faultCluster {
	t.Helper()

	if testing.Short() {
		t.Skip("fault injection on a multi-node cluster")
	}

	fc := &faultCluster{core: newFakeCore()}

	fc.testCluster = newClusterWith(t, 3, func(i int, dir, address string) *env {
		fn := &faultNode{
			name: fmt.Sprintf("node%d", i), address: address, clock: &skewedClock{}, part: &partition{},
			logs: newHangingLogStore(),
		}

		if i < len(opts.offsets) {
			fn.clock.offset.Store(int64(opts.offsets[i]))
		}

		ctx := context.Background()

		d, err := db.Open(ctx, filepath.Join(dir, "smsc.db"))
		if err != nil {
			t.Fatal(err)
		}

		fn.deliverer = &delivery.Deliverer{
			Store:  d,
			Router: coreRouter{core: fc.core, node: fn.name},
			Sender: coreSender{core: fc.core, node: fn.name},
			Identity: func() diameter.Identity {
				return diameter.Identity{OriginHost: fn.name + ".smsc.example.org", OriginRealm: "example.org"}
			},
			Settings: func() settings.Settings {
				return settings.Settings{
					Operator: settings.Operator{ServiceCentreAddress: "15550000000"},
					Delivery: settings.Delivery{DefaultValidity: 24 * time.Hour, RetryIntervals: opts.retry},
				}
			},
			AttemptTimeout: opts.attemptTimeout,
			Concurrency:    16,
			Now:            fn.clock.now,
			Logger:         slog.New(slog.DiscardHandler),
		}

		d.OnEvents(func(ev db.Events) {
			if len(ev.Alerted) > 0 {
				fn.deliverer.Alerted(ev.Alerted)
			} else if ev.Messages {
				fn.deliverer.Notify()
			}
		})

		n, err := Open(ctx, Config{
			Dir: filepath.Join(dir, "raft"), Address: address, Version: "test", Now: fn.clock.now,
			Logger: slog.New(slog.DiscardHandler), transport: fn.part.wrap, logStore: fn.logs.wrap,
			logStoreTimeout: opts.logStoreTimeout,
		}, d)
		if err != nil {
			_ = d.Close()

			t.Fatal(err)
		}

		d.SetProposer(n)

		n.Lead(func(ctx context.Context, term uint64) {
			fn.mu.Lock()
			fn.terms = append(fn.terms, term)
			fn.mu.Unlock()

			fn.deliverer.Run(ctx, term)
		})

		fn.env = &env{dir: dir, db: d, node: n, stop: func() {
			fn.logs.unhang()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_ = fn.deliverer.Wait(ctx)
		}}

		fc.nodes = append(fc.nodes, fn)

		return fn.env
	})

	t.Cleanup(func() {
		for _, n := range fc.nodes {
			n.logs.unhang()
		}
	})

	waitVoters(t, fc.testCluster, 3)

	return fc
}

func (c *faultCluster) leaderNode() *faultNode {
	leader := c.leader()

	for _, n := range c.nodes {
		if n.env == leader {
			return n
		}
	}

	c.t.Fatal("no leader")

	return nil
}

func (c *faultCluster) newLeader(old *faultNode) *faultNode {
	c.t.Helper()

	var leader *faultNode

	eventually(c.t, "a leader other than "+old.name, func() error {
		for _, n := range c.nodes {
			if n != old && n.leading() {
				leader = n
				return nil
			}
		}

		return errors.New("none yet")
	})

	return leader
}

func (c *faultCluster) transferTo(to *faultNode) {
	c.t.Helper()

	eventually(c.t, "leadership on "+to.name, func() error {
		if to.leading() {
			return nil
		}

		for _, n := range c.nodes {
			if r, _ := n.node.current(); r.State() == raft.Leader && n != to {
				return r.LeadershipTransferToServer(raft.ServerID(to.node.ID()), raft.ServerAddress(to.address)).Error()
			}
		}

		return errors.New("no leader to hand over from")
	})

	eventually(c.t, "leadership established on "+to.name, func() error {
		if !to.leading() {
			return errors.New("not yet")
		}

		return nil
	})
}

func createMessage(on *faultNode, msisdn string, submitted time.Time) (int64, error) {
	tpdu, err := hex.DecodeString(faultTPDU)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return on.db.CreateMessage(ctx, db.NewMessage{
		Originator:  db.Address{Digits: "15551230001", TypeOfNumber: 1, NumberingPlan: 1},
		Recipient:   db.Address{Digits: msisdn, TypeOfNumber: 1, NumberingPlan: 1},
		MSISDN:      msisdn,
		Origin:      db.OriginMobile,
		TPDU:        tpdu,
		SubmittedAt: submitted,
		ExpiresAt:   submitted.Add(time.Hour),
	})
}

func storeMessage(t testing.TB, on *faultNode, msisdn string, submitted time.Time) int64 {
	t.Helper()

	var id int64

	eventually(t, "storing a message to "+msisdn, func() error {
		var err error

		id, err = createMessage(on, msisdn, submitted)

		return err
	})

	return id
}

func waitDelivered(t testing.TB, on *faultNode, id int64) db.Message {
	t.Helper()

	var m db.Message

	eventually(t, fmt.Sprintf("message %d delivered on %s", id, on.name), func() error {
		var err error

		m, err = on.db.GetMessage(context.Background(), id)
		if err != nil {
			return err
		}

		if m.Status != db.StatusDelivered {
			return fmt.Errorf("status %s", m.Status)
		}

		return nil
	})

	return m
}

func forwardsTo(transfers []transfer, msisdn string) []transfer {
	var out []transfer

	for _, tr := range transfers {
		if tr.msisdn == msisdn {
			out = append(out, tr)
		}
	}

	return out
}

// TS 23.040 clause 6.2
func TestPartitionedLeaderStopsDelivering(t *testing.T) {
	c := newFaultCluster(t, faultOptions{attemptTimeout: time.Second, retry: []time.Duration{500 * time.Millisecond}})
	old := c.leaderNode()

	const held, unclaimed = "15551230002", "15551230003"

	c.core.holdTransfer(held)
	routing := c.core.holdRouting(unclaimed)

	heldID := storeMessage(t, old, held, time.Now())
	unclaimedID := storeMessage(t, old, unclaimed, time.Now())

	c.core.waitTransfer(t, old.name, held)
	c.core.waitRouting(t, old.name, unclaimed)

	partitionedAt := time.Now()

	old.part.isolate()
	close(routing)

	next := c.newLeader(old)
	ledAt := time.Now()

	eventually(t, "the isolated leader stepping down", func() error {
		if old.leading() {
			return errors.New("still leading")
		}

		return nil
	})

	waitDelivered(t, next, unclaimedID)

	time.Sleep(time.Until(ledAt.Add(1500 * time.Millisecond)))

	healedAt := time.Now()

	old.part.heal()

	m := waitDelivered(t, next, heldID)

	c.core.check(t)

	transfers, _, _ := c.core.snapshot()

	for _, tr := range transfers {
		if tr.node == old.name && tr.start.After(partitionedAt) {
			t.Fatalf("the isolated leader forwarded to %s at %s, after losing quorum at %s", tr.msisdn,
				tr.start.Format(time.StampMicro), partitionedAt.Format(time.StampMicro))
		}
	}

	for _, tr := range forwardsTo(transfers, held) {
		if tr.node != old.name && tr.start.Before(healedAt) {
			t.Fatalf("%s forwarded to %s at %s, while the isolated leader's claim was held until %s", tr.node, held,
				tr.start.Format(time.StampMicro), healedAt.Format(time.StampMicro))
		}
	}

	if got := forwardsTo(transfers, unclaimed); len(got) != 1 || got[0].node != next.name {
		t.Fatalf("forwards to %s = %+v, want one from the new leader %s", unclaimed, got, next.name)
	}

	if m.Retries > 1 {
		t.Fatalf("the isolated leader's late outcome was applied %d times", m.Retries)
	}
}

type leaderTimes struct {
	mu        sync.Mutex
	last      map[string]int64
	decreases []string
}

func watchLeaderTimes(t *testing.T, c *faultCluster) *leaderTimes {
	t.Helper()

	lt := &leaderTimes{last: map[string]int64{}}
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}

			lt.mu.Lock()

			for _, n := range c.nodes {
				now := n.db.LeaderTime()
				if now < lt.last[n.name] {
					lt.decreases = append(lt.decreases, fmt.Sprintf("%s from %d to %d", n.name, lt.last[n.name], now))
				}

				lt.last[n.name] = now
			}

			lt.mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		close(stop)
		<-done
	})

	return lt
}

func (lt *leaderTimes) check(t *testing.T) {
	t.Helper()

	lt.mu.Lock()
	defer lt.mu.Unlock()

	if len(lt.decreases) > 0 {
		t.Fatalf("leader time went backwards: %v", lt.decreases)
	}
}

// TS 23.040 clause 6.2
func TestLeaderChangeAcrossSkewedClocks(t *testing.T) {
	c := newFaultCluster(t, faultOptions{
		attemptTimeout: 2 * time.Second,
		retry:          []time.Duration{300 * time.Millisecond},
		offsets:        []time.Duration{0, 30 * time.Second, -30 * time.Second},
	})
	base, ahead, behind := c.nodes[0], c.nodes[1], c.nodes[2]

	c.transferTo(base)

	times := watchLeaderTimes(t, c)

	const first, second, retried = "15551230002", "15551230003", "15551230004"

	gate := c.core.holdTransfer(first)
	firstID := storeMessage(t, base, first, time.Now())
	c.core.waitTransfer(t, base.name, first)

	c.transferTo(ahead)
	time.Sleep(300 * time.Millisecond)
	close(gate)

	waitDelivered(t, ahead, firstID)

	aheadTime := ahead.db.LeaderTime()

	gate = c.core.holdTransfer(second)
	secondID := storeMessage(t, ahead, second, time.Now())
	c.core.waitTransfer(t, ahead.name, second)

	c.transferTo(behind)
	time.Sleep(300 * time.Millisecond)
	close(gate)

	waitDelivered(t, behind, secondID)

	c.core.answerAbsent(retried, 1)
	retriedID := storeMessage(t, behind, retried, behind.clock.now())
	m := waitDelivered(t, behind, retriedID)

	c.core.check(t)
	times.check(t)

	if m.Retries != 1 {
		t.Fatalf("message to %s delivered after %d retries, want 1", retried, m.Retries)
	}

	if got := behind.db.LeaderTime(); got <= aheadTime {
		t.Fatalf("leader time %d under the slow clock, %d under the fast one", got, aheadTime)
	}

	transfers, _, _ := c.core.snapshot()

	for msisdn, want := range map[string]int{first: 1, second: 1, retried: 2} {
		if got := forwardsTo(transfers, msisdn); len(got) != want {
			t.Fatalf("forwards to %s = %+v, want %d", msisdn, got, want)
		}
	}
}

func TestHungLogStoreHandsLeadershipOver(t *testing.T) {
	const storeTimeout = time.Second

	c := newFaultCluster(t, faultOptions{attemptTimeout: time.Second, logStoreTimeout: storeTimeout})
	stuck := c.leaderNode()

	var writer *faultNode

	for _, n := range c.nodes {
		if n != stuck {
			writer = n
			break
		}
	}

	const msisdn, routed = "15551230002", "15551230003"

	routing := c.core.holdRouting(routed)
	routedID := storeMessage(t, stuck, routed, time.Now())
	c.core.waitRouting(t, stuck.name, routed)

	stuck.logs.hang()
	close(routing)

	hungAt := time.Now()
	id := storeMessage(t, writer, msisdn, time.Now())

	next := c.newLeader(stuck)

	if took := time.Since(hungAt); took > storeTimeout+15*time.Second {
		t.Fatalf("a new leader took %s after the log store hung", took)
	}

	t.Logf("%s led %s after the log store of %s hung", next.name, time.Since(hungAt), stuck.name)

	waitDelivered(t, next, id)
	waitDelivered(t, next, routedID)

	eventually(t, "the stuck node not leading", func() error {
		if stuck.leading() {
			return errors.New("still leading")
		}

		return nil
	})

	c.core.check(t)

	transfers, _, _ := c.core.snapshot()

	for _, tr := range transfers {
		if tr.node == stuck.name {
			t.Fatalf("the node with a hung log store forwarded to %s at %s", tr.msisdn, tr.start.Format(time.StampMicro))
		}
	}

	for _, m := range []string{msisdn, routed} {
		if got := forwardsTo(transfers, m); len(got) != 1 {
			t.Fatalf("forwards to %s = %+v, want one", m, got)
		}
	}
}

func TestLeadershipLostAndRegained(t *testing.T) {
	c := newFaultCluster(t, faultOptions{attemptTimeout: 2 * time.Second, retry: []time.Duration{300 * time.Millisecond}})
	c.core.latency = 20 * time.Millisecond

	home := c.leaderNode()

	var away *faultNode

	for _, n := range c.nodes {
		if n != home {
			away = n
			break
		}
	}

	recipients := []string{"15551230002", "15551230003", "15551230004", "15551230005"}
	produced := make(chan error, 1)

	go func() {
		defer close(produced)

		for i := range 32 {
			on, msisdn := c.nodes[i%len(c.nodes)], recipients[i%len(recipients)]
			deadline := time.Now().Add(10 * time.Second)

			_, err := createMessage(on, msisdn, time.Now())
			for err != nil && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)

				_, err = createMessage(on, msisdn, time.Now())
			}

			if err != nil {
				produced <- err
				return
			}

			time.Sleep(15 * time.Millisecond)
		}
	}()

	for range 3 {
		homeRaft, _ := home.node.current()
		awayRaft, _ := away.node.current()

		if err := homeRaft.LeadershipTransferToServer(raft.ServerID(away.node.ID()), raft.ServerAddress(away.address)).Error(); err != nil {
			t.Fatal(err)
		}

		eventually(t, "leadership back on "+home.name, func() error {
			if awayRaft.State() != raft.Leader {
				return errors.New(away.name + " not leading")
			}

			return awayRaft.LeadershipTransferToServer(raft.ServerID(home.node.ID()), raft.ServerAddress(home.address)).Error()
		})

		eventually(t, home.name+" leading again", func() error {
			if !home.leading() {
				return errors.New("not yet")
			}

			return nil
		})
	}

	if err := <-produced; err != nil {
		t.Fatal(err)
	}

	for _, msisdn := range recipients {
		storeMessage(t, home, msisdn, time.Now())
	}

	stored := map[string]int{}

	eventually(t, "every message delivered", func() error {
		for _, msisdn := range recipients {
			counts, err := home.db.CountMessages(context.Background(), db.MessageFilter{MSISDN: msisdn})
			if err != nil {
				return err
			}

			if len(counts) != 1 || counts[db.StatusDelivered] == 0 {
				return fmt.Errorf("messages to %s: %v", msisdn, counts)
			}

			stored[msisdn] = counts[db.StatusDelivered]
		}

		return nil
	})

	c.core.check(t)

	transfers, _, _ := c.core.snapshot()

	for _, msisdn := range recipients {
		delivered := 0

		for _, tr := range forwardsTo(transfers, msisdn) {
			if tr.ok {
				delivered++
			}
		}

		if delivered != stored[msisdn] {
			t.Fatalf("%d successful forwards to %s, want %d", delivered, msisdn, stored[msisdn])
		}
	}

	terms := home.ledTerms()
	for i := 1; i < len(terms); i++ {
		if terms[i] <= terms[i-1] {
			t.Fatalf("%s led terms %v", home.name, terms)
		}
	}

	if len(terms) < 4 {
		t.Fatalf("%s led terms %v, want at least 4", home.name, terms)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, n := range c.nodes {
		if err := n.deliverer.Wait(ctx); err != nil {
			t.Fatalf("%s still delivering: %v", n.name, err)
		}
	}
}
