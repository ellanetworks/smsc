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
	stateOpen
	stateClosing
)

type Conn struct {
	sc     *sctp.SCTPConn
	srv    *Server
	logger *slog.Logger

	state        atomic.Int32
	unordered    atomic.Bool
	peerHost     string
	peerRealm    string
	peerIsRelay  bool
	commonAppIDs map[uint32]bool

	hopByHop atomic.Uint32
	endToEnd atomic.Uint32

	mu      sync.Mutex
	pending map[uint32]chan *Message

	activity     chan struct{}
	watchdogDWA  chan struct{}
	disconnected chan struct{}
	closeOnce    sync.Once
}

func newConn(srv *Server, sc *sctp.SCTPConn) *Conn {
	c := &Conn{
		sc:           sc,
		srv:          srv,
		logger:       srv.Logger,
		pending:      make(map[uint32]chan *Message),
		activity:     make(chan struct{}, 1),
		watchdogDWA:  make(chan struct{}, 1),
		disconnected: make(chan struct{}),
	}

	c.hopByHop.Store(randomUint32())
	c.endToEnd.Store(uint32(time.Now().Unix()&0xfff)<<20 | randomUint32()&0xfffff)

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
	req.HopByHopID = c.hopByHop.Add(1)
	req.EndToEndID = c.endToEnd.Add(1)

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
	ans := &Message{
		Flags:         req.Flags & FlagProxiable,
		CommandCode:   req.CommandCode,
		ApplicationID: req.ApplicationID,
		HopByHopID:    req.HopByHopID,
		EndToEndID:    req.EndToEndID,
	}

	if resultCode >= 3000 && resultCode < 4000 {
		ans.Flags |= FlagError
	}

	if sessionID, ok := req.Find(AVPSessionID, 0); ok {
		ans.AVPs = append(ans.AVPs, sessionID)
	}

	ans.AVPs = append(ans.AVPs,
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, c.srv.Identity.OriginHost),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, c.srv.Identity.OriginRealm),
		Unsigned32(AVPResultCode, AVPFlagMandatory, 0, resultCode),
	)

	ans.AVPs = append(ans.AVPs, FindAll(req.AVPs, AVPProxyInfo, 0)...)

	return ans
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

func (c *Conn) receive(ctx context.Context, b []byte) {
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
	case stateOpen, stateClosing:
		c.unordered.Store(true)
		c.signal(c.activity)
	}

	if !m.IsRequest() {
		c.receiveAnswer(m)
		return
	}

	switch m.CommandCode {
	case CommandDeviceWatchdog:
		c.send(c.Answer(m, ResultSuccess))
	case CommandDisconnectPeer:
		c.state.Store(int32(stateClosing))
		c.send(c.Answer(m, ResultSuccess))
	case CommandCapabilitiesExchange:
		c.logger.Warn("ignoring CER on an open Diameter connection", slog.String("peer", c.peerHost))
	default:
		if !c.peerIsRelay && !c.commonAppIDs[m.ApplicationID] {
			c.send(c.Answer(m, ResultApplicationUnsupported))
			return
		}

		go c.serve(ctx, m)
	}
}

func (c *Conn) receiveMalformed(m *Message, err error) {
	if m == nil || !m.IsRequest() || connState(c.state.Load()) == stateWaitCER {
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
		c.signal(c.watchdogDWA)
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

func (c *Conn) serve(ctx context.Context, req *Message) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("panic handling Diameter request",
				slog.Any("panic", r), slog.Uint64("command_code", uint64(req.CommandCode)), slog.String("stack", string(debug.Stack())))
			c.send(c.Answer(req, ResultUnableToComply))
		}
	}()

	ans := c.srv.Handler.ServeDiameter(ctx, c, req)
	if ans == nil {
		ans = c.Answer(req, ResultUnableToComply)
	}

	c.send(ans)
}

func (c *Conn) send(m *Message) {
	if err := c.write(m); err != nil {
		c.logger.Warn("failed to send Diameter message", slog.Uint64("command_code", uint64(m.CommandCode)), slog.Any("error", err))
	}
}

func (c *Conn) signal(ch chan struct{}) {
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

func (c *Conn) disconnect() {
	c.closeOnce.Do(func() {
		close(c.disconnected)

		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
	})
}

func (c *Conn) watchdog(twinit time.Duration) {
	pending := false
	suspect := false

	timer := time.NewTimer(jitter(twinit, c.srv.jitter))
	defer timer.Stop()

	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}

		timer.Reset(jitter(twinit, c.srv.jitter))
	}

	for {
		select {
		case <-c.disconnected:
			return
		case <-c.watchdogDWA:
			pending = false
			suspect = false

			reset()
		case <-c.activity:
			suspect = false

			reset()
		case <-timer.C:
			switch {
			case suspect:
				c.logger.Warn("closing Diameter connection: watchdog expired twice without an answer", slog.String("peer", c.peerHost))
				c.abort()

				return
			case pending:
				suspect = true
			default:
				c.send(&Message{
					Flags:       FlagRequest,
					CommandCode: CommandDeviceWatchdog,
					HopByHopID:  c.hopByHop.Add(1),
					EndToEndID:  c.endToEnd.Add(1),
					AVPs: []AVP{
						UTF8String(AVPOriginHost, AVPFlagMandatory, 0, c.srv.Identity.OriginHost),
						UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, c.srv.Identity.OriginRealm),
					},
				})

				pending = true
			}

			timer.Reset(jitter(twinit, c.srv.jitter))
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
