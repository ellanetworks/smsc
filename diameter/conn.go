package diameter

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"math/big"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/sctp"
)

var ErrConnClosed = errors.New("diameter: connection closed")

type connState int

const (
	stateWaitCER connState = iota
	stateWaitCEA
	stateOpen
	stateClosing
)

const readBufferSize = 65536

type Conn struct {
	sc        *sctp.SCTPConn
	node      *Node
	logger    *slog.Logger
	initiator bool

	state        atomic.Int32
	unordered    atomic.Bool
	available    atomic.Bool
	reopening    atomic.Bool
	dprCause     atomic.Int64
	reopen       bool
	expectedPeer string
	peerHost     string
	peerRealm    string
	peerIsRelay  bool
	commonAppIDs map[uint32]bool

	hopByHop atomic.Uint32

	mu      sync.Mutex
	pending map[uint32]chan *Message

	activity     chan struct{}
	watchdogDWA  chan struct{}
	opened       chan struct{}
	disconnected chan struct{}
	openOnce     sync.Once
	closeOnce    sync.Once

	handshakeMu    sync.Mutex
	handshakeTimer *time.Timer
}

func newConn(n *Node, sc *sctp.SCTPConn, initiator bool) *Conn {
	c := &Conn{
		sc:           sc,
		node:         n,
		logger:       n.Logger,
		initiator:    initiator,
		pending:      make(map[uint32]chan *Message),
		activity:     make(chan struct{}, 1),
		watchdogDWA:  make(chan struct{}, 1),
		opened:       make(chan struct{}),
		disconnected: make(chan struct{}),
	}

	if initiator {
		c.state.Store(int32(stateWaitCEA))
	}

	c.dprCause.Store(-1)
	c.hopByHop.Store(randomUint32())

	return c
}

func randomUint32() uint32 {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return binary.BigEndian.Uint32(b[:])
}

func (c *Conn) PeerHost() string {
	return c.peerHost
}

func (c *Conn) PeerRealm() string {
	return c.peerRealm
}

func (c *Conn) Do(ctx context.Context, req *Message) (*Message, error) {
	req.Flags |= FlagRequest
	req.EndToEndID = c.node.nextEndToEnd()

	return c.exchange(ctx, req)
}

func (c *Conn) exchange(ctx context.Context, req *Message) (*Message, error) {
	req.HopByHopID = c.hopByHop.Add(1)

	ch := make(chan *Message, 1)

	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return nil, ErrConnClosed
	}

	c.pending[req.HopByHopID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.pending != nil {
			delete(c.pending, req.HopByHopID)
		}
		c.mu.Unlock()
	}()

	if err := c.write(req); err != nil {
		return nil, err
	}

	select {
	case ans, ok := <-ch:
		if !ok {
			return nil, ErrConnClosed
		}

		return ans, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.disconnected:
		return nil, ErrConnClosed
	}
}

func (c *Conn) Answer(req *Message, resultCode uint32) *Message {
	return NewAnswer(req, c.node.Identity, resultCode)
}

func (c *Conn) write(m *Message) error {
	b, err := m.Marshal()
	if err != nil {
		return err
	}

	info := sctp.SndRcvInfo{PPID: sctp.PPIDWireOrder(PPID)}
	if c.unordered.Load() {
		info.Flags = sctp.SCTPUnordered
	}

	if _, err := c.sc.WriteMsg(b, &info); err != nil {
		return err
	}

	return nil
}

func (c *Conn) readLoop() {
	defer c.node.connDown(c)

	buf := make([]byte, readBufferSize)

	for {
		n, info, err := c.sc.ReadMsg(buf)
		if err != nil {
			return
		}

		if info != nil && info.PPID != sctp.PPIDWireOrder(PPID) {
			c.logger.Debug("discarding SCTP message with an unexpected payload protocol identifier", slog.String("peer", c.expectedPeer))
			continue
		}

		c.receive(append([]byte(nil), buf[:n]...))
	}
}

func (c *Conn) receive(b []byte) {
	m, err := Unmarshal(b)
	if err != nil {
		c.receiveMalformed(m, err)
		return
	}

	switch connState(c.state.Load()) {
	case stateWaitCER:
		if m.IsRequest() && m.CommandCode == CommandCapabilitiesExchange {
			c.handleCER(m)
			return
		}

		c.logger.Warn("closing Diameter connection: first message is not a CER", slog.Uint64("command_code", uint64(m.CommandCode)))
		c.abort()

		return
	case stateWaitCEA:
		if !m.IsRequest() && m.CommandCode == CommandCapabilitiesExchange {
			c.handleCEA(m)
			return
		}

		c.logger.Warn("closing Diameter connection: expected a CEA", slog.Uint64("command_code", uint64(m.CommandCode)))
		c.abort()

		return
	case stateOpen, stateClosing:
		c.unordered.Store(true)
	}

	if c.reopening.Load() && !isWatchdogOrDisconnect(m) {
		c.logger.Debug("discarding Diameter message while the connection is reopening", slog.Uint64("command_code", uint64(m.CommandCode)))
		return
	}

	if m.CommandCode != CommandDeviceWatchdog || m.IsRequest() {
		signal(c.activity)
	}

	if !m.IsRequest() {
		c.receiveAnswer(m)
		return
	}

	switch m.CommandCode {
	case CommandDeviceWatchdog:
		c.send(c.Answer(m, ResultSuccess))
	case CommandDisconnectPeer:
		if cause, ok := m.Find(AVPDisconnectCause, 0); ok {
			if v, err := cause.Unsigned32(); err == nil {
				c.dprCause.Store(int64(v))
			}
		}

		c.state.Store(int32(stateClosing))
		c.send(c.Answer(m, ResultSuccess))
	case CommandCapabilitiesExchange:
		c.logger.Warn("ignoring CER on an open Diameter connection", slog.String("peer", c.peerHost))
	default:
		if !c.peerIsRelay && !c.commonAppIDs[m.ApplicationID] {
			c.send(c.Answer(m, ResultApplicationUnsupported))
			return
		}

		if code := c.node.routingError(m); code != 0 {
			c.send(c.Answer(m, code))
			return
		}

		if code := c.node.admit(m); code != 0 {
			c.send(c.Answer(m, code))
			return
		}

		go c.serve(m)
	}
}

func isWatchdogOrDisconnect(m *Message) bool {
	return m.CommandCode == CommandDeviceWatchdog || m.CommandCode == CommandDisconnectPeer
}

func (c *Conn) receiveMalformed(m *Message, err error) {
	state := connState(c.state.Load())
	if m == nil || !m.IsRequest() || state == stateWaitCER || state == stateWaitCEA {
		c.logger.Warn("closing Diameter connection on an unparsable message", slog.Any("error", err))
		c.abort()

		return
	}

	switch {
	case errors.Is(err, ErrUnsupportedVersion):
		c.send(c.Answer(m, ResultUnsupportedVersion))
	case errors.Is(err, ErrInvalidMessageLength):
		c.send(c.Answer(m, ResultInvalidMessageLength))
	case errors.Is(err, ErrInvalidAVPLength):
		c.send(c.Answer(m, ResultInvalidAVPLength))
	}
}

func (c *Conn) receiveAnswer(m *Message) {
	switch m.CommandCode {
	case CommandDeviceWatchdog:
		signal(c.watchdogDWA)
		return
	case CommandDisconnectPeer:
		c.close()
		return
	}

	c.mu.Lock()
	ch, ok := c.pending[m.HopByHopID]
	c.mu.Unlock()

	if !ok {
		c.logger.Debug("discarding Diameter answer with an unknown Hop-by-Hop Identifier", slog.Uint64("hop_by_hop", uint64(m.HopByHopID)))
		return
	}

	select {
	case ch <- m:
	default:
	}
}

func (c *Conn) handleCER(req *Message) {
	host, hasHost := req.Find(AVPOriginHost, 0)
	realm, hasRealm := req.Find(AVPOriginRealm, 0)

	if !hasHost || !hasRealm {
		c.send(c.capabilitiesAnswer(req, ResultMissingAVP))
		c.close()

		return
	}

	if !offersNoInbandSecurity(req.AVPs) {
		c.logger.Warn("rejecting Diameter peer with no common security mechanism", slog.String("peer", host.String()))
		c.send(c.capabilitiesAnswer(req, ResultNoCommonSecurity))
		c.close()

		return
	}

	common, relay := c.node.commonApplications(req.AVPs)
	if len(common) == 0 && !relay {
		c.logger.Warn("rejecting Diameter peer with no common application", slog.String("peer", host.String()))
		c.send(c.capabilitiesAnswer(req, ResultNoCommonApplication))
		c.close()

		return
	}

	if !c.node.acceptResponder(c, host.String()) {
		c.logger.Info("closing duplicate Diameter connection from peer", slog.String("peer", host.String()))
		c.abort()

		return
	}

	c.stopHandshakeTimer()
	c.send(c.capabilitiesAnswer(req, ResultSuccess))
	c.open(host.String(), realm.String(), common, relay)
}

func (c *Conn) handleCEA(ans *Message) {
	c.stopHandshakeTimer()

	host, hasHost := ans.Find(AVPOriginHost, 0)
	realm, hasRealm := ans.Find(AVPOriginRealm, 0)

	result, _ := ans.Find(AVPResultCode, 0)
	code, _ := result.Unsigned32()

	if !hasHost || !hasRealm || code != ResultSuccess {
		c.logger.Warn("Diameter peer refused the capabilities exchange", slog.String("peer", c.expectedPeer), slog.Uint64("result_code", uint64(code)))
		c.abort()

		return
	}

	common, relay := c.node.commonApplications(ans.AVPs)
	if len(common) == 0 && !relay {
		c.logger.Warn("closing Diameter connection with no common application", slog.String("peer", host.String()))
		c.abort()

		return
	}

	if !c.node.acceptInitiator(c, host.String()) {
		c.logger.Info("closing Diameter connection superseded or from an unexpected peer", slog.String("peer", host.String()))
		c.abort()

		return
	}

	c.unordered.Store(true)
	c.open(host.String(), realm.String(), common, relay)
}

func (c *Conn) open(host, realm string, common map[uint32]bool, relay bool) {
	c.peerHost = host
	c.peerRealm = realm
	c.peerIsRelay = relay
	c.commonAppIDs = common
	c.state.Store(int32(stateOpen))

	c.logger.Info("Diameter peer connected", slog.String("peer", host), slog.String("realm", realm))

	go c.watchdog(c.node.WatchdogInterval, c.reopen)

	c.openOnce.Do(func() { close(c.opened) })
}

func (c *Conn) capabilitiesAnswer(req *Message, resultCode uint32) *Message {
	return &Message{
		CommandCode: CommandCapabilitiesExchange,
		HopByHopID:  req.HopByHopID,
		EndToEndID:  req.EndToEndID,
		AVPs:        append([]AVP{Unsigned32(AVPResultCode, AVPFlagMandatory, 0, resultCode)}, c.node.capabilityAVPs()...),
	}
}

func (c *Conn) capabilitiesRequest() *Message {
	return &Message{
		Flags:       FlagRequest,
		CommandCode: CommandCapabilitiesExchange,
		HopByHopID:  c.hopByHop.Add(1),
		EndToEndID:  c.node.nextEndToEnd(),
		AVPs:        c.node.capabilityAVPs(),
	}
}

func (c *Conn) sendDPR(cause uint32) {
	c.send(&Message{
		Flags:       FlagRequest,
		CommandCode: CommandDisconnectPeer,
		HopByHopID:  c.hopByHop.Add(1),
		EndToEndID:  c.node.nextEndToEnd(),
		AVPs: []AVP{
			UTF8String(AVPOriginHost, AVPFlagMandatory, 0, c.node.Identity.OriginHost),
			UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, c.node.Identity.OriginRealm),
			Unsigned32(AVPDisconnectCause, AVPFlagMandatory, 0, cause),
		},
	})
}

func (c *Conn) serve(req *Message) {
	defer c.node.inflight.Done()

	key, entry, original := c.node.duplicates.begin(req)
	if !original {
		select {
		case <-entry.done:
			c.send(entry.answerFor(req))
		case <-c.node.baseCtx.Done():
		}

		return
	}

	ans := c.handle(req)

	c.node.duplicates.finish(key, entry, ans)
	c.send(ans)
}

func (c *Conn) handle(req *Message) (ans *Message) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("panic handling Diameter request",
				slog.Any("panic", r), slog.Uint64("command_code", uint64(req.CommandCode)), slog.String("stack", string(debug.Stack())))

			ans = c.Answer(req, ResultUnableToComply)
		}
	}()

	ans = c.node.Handler.ServeDiameter(c.node.baseCtx, c, req)
	if ans == nil {
		ans = c.Answer(req, ResultUnableToComply)
	}

	return ans
}

func (c *Conn) send(m *Message) {
	if err := c.write(m); err != nil {
		c.logger.Warn("failed to send Diameter message", slog.Uint64("command_code", uint64(m.CommandCode)), slog.Any("error", err))
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *Conn) abort() {
	_ = c.sc.Abort()
}

func (c *Conn) close() {
	_ = c.sc.Close()
}

func (c *Conn) startHandshakeTimer(timeout time.Duration) {
	c.handshakeMu.Lock()
	defer c.handshakeMu.Unlock()

	c.handshakeTimer = time.AfterFunc(timeout, func() {
		state := connState(c.state.Load())
		if state != stateWaitCER && state != stateWaitCEA {
			return
		}

		c.logger.Warn("closing Diameter connection: capabilities exchange timed out", slog.Duration("timeout", timeout))
		c.abort()
	})
}

func (c *Conn) stopHandshakeTimer() {
	c.handshakeMu.Lock()
	defer c.handshakeMu.Unlock()

	if c.handshakeTimer != nil {
		c.handshakeTimer.Stop()
	}
}

func (c *Conn) disconnect() {
	c.closeOnce.Do(func() {
		c.stopHandshakeTimer()
		c.available.Store(false)
		close(c.disconnected)

		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
	})
}

type watchdogStatus int

const (
	watchdogOkay watchdogStatus = iota
	watchdogSuspect
	watchdogReopen
)

func (c *Conn) watchdog(twinit time.Duration, reopen bool) {
	status := watchdogOkay
	pending := false
	numDWA := 0

	timer := time.NewTimer(jitter(twinit, c.node.jitter))
	defer timer.Stop()

	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		timer.Reset(jitter(twinit, c.node.jitter))
	}

	setStatus := func(s watchdogStatus) {
		status = s
		c.reopening.Store(s == watchdogReopen)
		c.available.Store(s == watchdogOkay)
		c.node.notifyPeers()
	}

	sendDWR := func() {
		c.send(&Message{
			Flags:       FlagRequest,
			CommandCode: CommandDeviceWatchdog,
			HopByHopID:  c.hopByHop.Add(1),
			EndToEndID:  c.node.nextEndToEnd(),
			AVPs: []AVP{
				UTF8String(AVPOriginHost, AVPFlagMandatory, 0, c.node.Identity.OriginHost),
				UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, c.node.Identity.OriginRealm),
			},
		})

		pending = true
	}

	if reopen {
		setStatus(watchdogReopen)
		sendDWR()
	} else {
		setStatus(watchdogOkay)
	}

	for {
		select {
		case <-c.disconnected:
			return
		case <-c.watchdogDWA:
			pending = false

			switch status {
			case watchdogReopen:
				numDWA++
				if numDWA == 3 {
					setStatus(watchdogOkay)
				}
			case watchdogSuspect:
				setStatus(watchdogOkay)
			}

			if status != watchdogReopen {
				reset()
			}
		case <-c.activity:
			if status == watchdogSuspect {
				setStatus(watchdogOkay)
			}

			if status != watchdogReopen {
				reset()
			}
		case <-timer.C:
			switch status {
			case watchdogOkay:
				if !pending {
					sendDWR()
				} else {
					setStatus(watchdogSuspect)
				}
			case watchdogSuspect:
				c.logger.Warn("closing Diameter connection: watchdog expired twice without an answer", slog.String("peer", c.peerHost))
				c.abort()

				return
			case watchdogReopen:
				switch {
				case !pending:
					sendDWR()
				case numDWA < 0:
					c.logger.Warn("closing reopened Diameter connection: watchdog unanswered", slog.String("peer", c.peerHost))
					c.abort()

					return
				default:
					numDWA = -1
				}
			}

			timer.Reset(jitter(twinit, c.node.jitter))
		}
	}
}

var watchdogJitter = 2 * time.Second

func jitter(twinit, spread time.Duration) time.Duration {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(2*spread)+1))
	if err != nil {
		return twinit
	}

	return twinit - spread + time.Duration(n.Int64())
}
