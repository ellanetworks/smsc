package diameter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellanetworks/core/sctp"
)

const (
	DefaultWatchdogInterval  = 30 * time.Second
	DefaultHandshakeTimeout  = 30 * time.Second
	DefaultReconnectInterval = 30 * time.Second
	DefaultRequestTimeout    = 30 * time.Second
)

var (
	minWatchdogInterval = 6 * time.Second

	ErrNotConnected = errors.New("diameter: peer not connected")
	ErrUnknownPeer  = errors.New("diameter: unknown peer")
)

type Identity struct {
	OriginHost      string
	OriginRealm     string
	HostIPAddresses []netip.Addr
	VendorID        uint32
	ProductName     string
}

type Application struct {
	ID       uint32
	VendorID uint32
}

type Peer struct {
	Host    string
	Address *sctp.SCTPAddr
}

type Handler interface {
	ServeDiameter(ctx context.Context, c *Conn, req *Message) *Message
}

type HandlerFunc func(ctx context.Context, c *Conn, req *Message) *Message

func (f HandlerFunc) ServeDiameter(ctx context.Context, c *Conn, req *Message) *Message {
	return f(ctx, c, req)
}

type Node struct {
	Identity          Identity
	Applications      []Application
	Handler           Handler
	Peers             []Peer
	WatchdogInterval  time.Duration
	HandshakeTimeout  time.Duration
	ReconnectInterval time.Duration
	RequestTimeout    time.Duration
	Logger            *slog.Logger

	initOnce   sync.Once
	initErr    error
	sctpServer *sctp.Server
	conns      sync.Map
	jitter     time.Duration
	baseCtx    context.Context
	baseCancel context.CancelFunc

	inflight     sync.WaitGroup
	admitMu      sync.Mutex
	shuttingDown bool
	duplicates   duplicateCache

	peersMu      sync.Mutex
	peers        map[string]*peerEntry
	peersChanged chan struct{}
	dialers      sync.WaitGroup

	sessionHigh uint32
	sessionLow  atomic.Uint32
	endToEnd    atomic.Uint32
}

type peerEntry struct {
	config    *Peer
	open      *Conn
	initiator *Conn
	connected bool
	suppress  bool
	kick      chan struct{}
}

func (n *Node) init(ctx context.Context) error {
	n.initOnce.Do(func() {
		if err := n.validate(); err != nil {
			n.initErr = err
			return
		}

		if n.Logger == nil {
			n.Logger = slog.Default()
		}

		if n.WatchdogInterval == 0 {
			n.WatchdogInterval = DefaultWatchdogInterval
		}

		if n.HandshakeTimeout == 0 {
			n.HandshakeTimeout = DefaultHandshakeTimeout
		}

		if n.ReconnectInterval == 0 {
			n.ReconnectInterval = DefaultReconnectInterval
		}

		if n.RequestTimeout == 0 {
			n.RequestTimeout = DefaultRequestTimeout
		}

		n.jitter = watchdogJitter
		n.baseCtx, n.baseCancel = context.WithCancel(context.WithoutCancel(ctx))
		n.sessionHigh = uint32(time.Now().Unix() + ntpEpochOffset)
		n.peers = make(map[string]*peerEntry)
		n.peersChanged = make(chan struct{})
		n.endToEnd.Store(uint32(time.Now().Unix()&0xfff)<<20 | randomUint32()&0xfffff)

		for i := range n.Peers {
			n.peers[strings.ToLower(n.Peers[i].Host)] = &peerEntry{config: &n.Peers[i], kick: make(chan struct{}, 1)}
		}
	})

	return n.initErr
}

const ntpEpochOffset = 2208988800

func (n *Node) validate() error {
	switch {
	case n.Identity.OriginHost == "":
		return errors.New("diameter: Identity.OriginHost is required")
	case n.Identity.OriginRealm == "":
		return errors.New("diameter: Identity.OriginRealm is required")
	case len(n.Identity.HostIPAddresses) == 0:
		return errors.New("diameter: at least one Identity.HostIPAddresses entry is required")
	case n.Identity.ProductName == "":
		return errors.New("diameter: Identity.ProductName is required")
	case len(n.Applications) == 0:
		return errors.New("diameter: at least one Application is required")
	case n.Handler == nil:
		return errors.New("diameter: Handler is required")
	case n.HandshakeTimeout < 0:
		return errors.New("diameter: HandshakeTimeout must not be negative")
	case n.ReconnectInterval < 0:
		return errors.New("diameter: ReconnectInterval must not be negative")
	case n.RequestTimeout < 0:
		return errors.New("diameter: RequestTimeout must not be negative")
	case n.WatchdogInterval != 0 && n.WatchdogInterval < minWatchdogInterval:
		return fmt.Errorf("diameter: WatchdogInterval %s is below the %s minimum", n.WatchdogInterval, minWatchdogInterval)
	}

	for _, p := range n.Peers {
		if p.Host == "" || p.Address == nil {
			return errors.New("diameter: every Peer needs a Host and an Address")
		}
	}

	return nil
}

func (n *Node) Serve(ctx context.Context, ln *sctp.Listener) error {
	if err := n.init(ctx); err != nil {
		return err
	}

	n.sctpServer = sctp.NewServer(sctp.Config{
		PPID:   PPID,
		Name:   "Diameter",
		Logger: n.Logger,
	}, sctp.Callbacks{
		OnConnect: func(sc *sctp.SCTPConn) {
			c := newConn(n, sc, false)
			n.conns.Store(sc, c)
			c.startHandshakeTimer(n.HandshakeTimeout)
		},
		Dispatch: func(_ context.Context, sc *sctp.SCTPConn, b []byte) {
			v, _ := n.conns.LoadOrStore(sc, newConn(n, sc, false))
			v.(*Conn).receive(b)
		},
		OnDisconnect: func(sc *sctp.SCTPConn) {
			if v, ok := n.conns.LoadAndDelete(sc); ok {
				n.connDown(v.(*Conn))
			}
		},
	})

	n.sctpServer.Serve(ctx, ln)

	return nil
}

func (n *Node) Start(ctx context.Context) error {
	if err := n.init(ctx); err != nil {
		return err
	}

	var configured []*peerEntry

	n.peersMu.Lock()

	for _, entry := range n.peers {
		if entry.config != nil {
			configured = append(configured, entry)
		}
	}
	n.peersMu.Unlock()

	for _, entry := range configured {
		n.dialers.Add(1)

		go n.maintain(entry)
	}

	return nil
}

func (n *Node) Do(ctx context.Context, peerHost string, req *Message) (*Message, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, n.RequestTimeout)
		defer cancel()
	}

	n.peersMu.Lock()
	entry, ok := n.peers[strings.ToLower(peerHost)]

	var c *Conn
	if ok {
		c = availableConn(entry)
	}

	if ok && c == nil && entry.suppress {
		entry.suppress = false
		signal(entry.kick)
	}
	n.peersMu.Unlock()

	switch {
	case !ok:
		return nil, ErrUnknownPeer
	case c == nil:
		return nil, ErrNotConnected
	}

	req.Flags |= FlagRequest
	req.EndToEndID = n.nextEndToEnd()

	for {
		ans, err := c.exchange(ctx, req)
		if !errors.Is(err, ErrConnClosed) {
			return ans, err
		}

		c, err = n.waitAvailable(ctx, entry)
		if err != nil {
			return nil, err
		}

		req.Flags |= FlagRetransmit
	}
}

func availableConn(entry *peerEntry) *Conn {
	if entry.open != nil && entry.open.available.Load() {
		return entry.open
	}

	return nil
}

func (n *Node) waitAvailable(ctx context.Context, entry *peerEntry) (*Conn, error) {
	for {
		n.peersMu.Lock()
		c := availableConn(entry)
		changed := n.peersChanged
		n.peersMu.Unlock()

		if c != nil {
			return c, nil
		}

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (n *Node) notifyPeers() {
	n.peersMu.Lock()
	defer n.peersMu.Unlock()

	n.notifyPeersLocked()
}

func (n *Node) notifyPeersLocked() {
	if n.peersChanged == nil {
		return
	}

	close(n.peersChanged)
	n.peersChanged = make(chan struct{})
}

func (n *Node) nextEndToEnd() uint32 {
	return n.endToEnd.Add(1)
}

func (n *Node) NewSessionID() string {
	return n.Identity.OriginHost + ";" + strconv.FormatUint(uint64(n.sessionHigh), 10) + ";" +
		strconv.FormatUint(uint64(n.sessionLow.Add(1)), 10)
}

func (n *Node) Shutdown(ctx context.Context) {
	if n.baseCtx == nil {
		return
	}

	n.admitMu.Lock()
	n.shuttingDown = true
	n.admitMu.Unlock()

	drained := make(chan struct{})

	go func() {
		n.inflight.Wait()
		close(drained)
	}()

	select {
	case <-drained:
	case <-ctx.Done():
	}

	n.baseCancel()

	var wg sync.WaitGroup

	n.forEachConn(func(c *Conn) {
		if !c.state.CompareAndSwap(int32(stateOpen), int32(stateClosing)) {
			return
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			c.sendDPR(DisconnectCauseRebooting)

			select {
			case <-c.disconnected:
			case <-ctx.Done():
				c.abort()
			}
		}()
	})

	wg.Wait()

	if n.sctpServer != nil {
		n.sctpServer.Shutdown(ctx)
	}

	n.forEachConn(func(c *Conn) { c.abort() })
	n.dialers.Wait()
}

func (n *Node) forEachConn(fn func(*Conn)) {
	seen := make(map[*Conn]bool)

	n.conns.Range(func(_, v any) bool {
		c := v.(*Conn)
		seen[c] = true
		fn(c)

		return true
	})

	var initiators []*Conn

	n.peersMu.Lock()

	for _, e := range n.peers {
		for _, c := range []*Conn{e.open, e.initiator} {
			if c != nil && !seen[c] {
				seen[c] = true
				initiators = append(initiators, c)
			}
		}
	}
	n.peersMu.Unlock()

	for _, c := range initiators {
		fn(c)
	}
}

func (n *Node) maintain(entry *peerEntry) {
	defer n.dialers.Done()

	for {
		if n.baseCtx.Err() != nil {
			return
		}

		n.peersMu.Lock()
		skip := entry.open != nil || entry.suppress
		n.peersMu.Unlock()

		if !skip {
			n.dial(entry)
		}

		select {
		case <-n.baseCtx.Done():
			return
		case <-entry.kick:
		case <-time.After(n.ReconnectInterval):
		}
	}
}

func (n *Node) dial(entry *peerEntry) {
	ctx, cancel := context.WithTimeout(n.baseCtx, n.HandshakeTimeout)
	defer cancel()

	sc, err := sctp.Dial(ctx, "sctp", nil, entry.config.Address, sctp.InitMsg{NumOstreams: 2, MaxInstreams: 5, MaxAttempts: 2, MaxInitTimeout: 2})
	if err != nil {
		n.Logger.Warn("failed to connect to Diameter peer", slog.String("peer", entry.config.Host), slog.Any("error", err))
		return
	}

	c := newConn(n, sc, true)
	c.expectedPeer = entry.config.Host

	n.peersMu.Lock()
	if entry.open != nil || n.shutdownStarted() {
		n.peersMu.Unlock()

		_ = sc.Close()

		return
	}

	entry.initiator = c
	n.peersMu.Unlock()

	go c.readLoop()

	c.startHandshakeTimer(n.HandshakeTimeout)
	c.send(c.capabilitiesRequest())

	select {
	case <-c.opened:
	case <-c.disconnected:
	}
}

func (n *Node) shutdownStarted() bool {
	n.admitMu.Lock()
	defer n.admitMu.Unlock()

	return n.shuttingDown
}

func (n *Node) acceptResponder(c *Conn, host string) bool {
	n.peersMu.Lock()
	defer n.peersMu.Unlock()

	key := strings.ToLower(host)

	entry, ok := n.peers[key]
	if !ok {
		entry = &peerEntry{kick: make(chan struct{}, 1)}
		n.peers[key] = entry
	}

	if entry.open != nil {
		return false
	}

	if entry.initiator != nil {
		if strings.ToLower(n.Identity.OriginHost) <= key {
			return false
		}

		entry.initiator.abort()
		entry.initiator = nil
	}

	n.openLocked(entry, c)

	return true
}

func (n *Node) acceptInitiator(c *Conn, host string) bool {
	n.peersMu.Lock()
	defer n.peersMu.Unlock()

	entry, ok := n.peers[strings.ToLower(c.expectedPeer)]
	if !ok || entry.initiator != c || !strings.EqualFold(host, c.expectedPeer) || entry.open != nil {
		return false
	}

	entry.initiator = nil
	n.openLocked(entry, c)

	return true
}

func (n *Node) openLocked(entry *peerEntry, c *Conn) {
	c.reopen = entry.connected && c.initiator
	entry.open = c
	entry.connected = true
	entry.suppress = false
}

func (n *Node) connDown(c *Conn) {
	c.disconnect()

	n.peersMu.Lock()
	defer n.peersMu.Unlock()

	for _, entry := range n.peers {
		if entry.initiator == c {
			entry.initiator = nil
		}

		if entry.open == c {
			entry.open = nil
			entry.suppress = c.dprCause.Load() == int64(DisconnectCauseBusy) ||
				c.dprCause.Load() == int64(DisconnectCauseDoNotWantToTalkToYou)

			if entry.config != nil && !entry.suppress {
				signal(entry.kick)
			}
		}
	}

	n.notifyPeersLocked()
}

func (n *Node) routingError(req *Message) uint32 {
	if host, ok := req.Find(AVPDestinationHost, 0); ok && !strings.EqualFold(host.String(), n.Identity.OriginHost) {
		return ResultUnableToDeliver
	}

	if realm, ok := req.Find(AVPDestinationRealm, 0); ok && !strings.EqualFold(realm.String(), n.Identity.OriginRealm) {
		return ResultRealmNotServed
	}

	return 0
}

func (n *Node) admit(req *Message) uint32 {
	n.admitMu.Lock()
	defer n.admitMu.Unlock()

	if !n.shuttingDown {
		n.inflight.Add(1)
		return 0
	}

	if _, ok := req.Find(AVPDestinationHost, 0); ok {
		return ResultTooBusy
	}

	return ResultUnableToDeliver
}

func (n *Node) capabilityAVPs() []AVP {
	avps := []AVP{
		UTF8String(AVPOriginHost, AVPFlagMandatory, 0, n.Identity.OriginHost),
		UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, n.Identity.OriginRealm),
	}

	for _, addr := range n.Identity.HostIPAddresses {
		avps = append(avps, Address(AVPHostIPAddress, AVPFlagMandatory, 0, addr))
	}

	avps = append(avps,
		Unsigned32(AVPVendorID, AVPFlagMandatory, 0, n.Identity.VendorID),
		UTF8String(AVPProductName, 0, 0, n.Identity.ProductName),
	)

	seenVendors := make(map[uint32]bool)

	for _, app := range n.Applications {
		if app.VendorID != 0 && !seenVendors[app.VendorID] {
			seenVendors[app.VendorID] = true
			avps = append(avps, Unsigned32(AVPSupportedVendorID, AVPFlagMandatory, 0, app.VendorID))
		}
	}

	for _, app := range n.Applications {
		if app.VendorID == 0 {
			avps = append(avps, Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, app.ID))
			continue
		}

		avps = append(avps, Grouped(AVPVendorSpecificApplicationID, AVPFlagMandatory, 0,
			Unsigned32(AVPVendorID, AVPFlagMandatory, 0, app.VendorID),
			Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, app.ID),
		))
	}

	return avps
}

func offersNoInbandSecurity(avps []AVP) bool {
	offers := FindAll(avps, AVPInbandSecurityID, 0)
	if len(offers) == 0 {
		return true
	}

	for _, a := range offers {
		if v, err := a.Unsigned32(); err == nil && v == InbandSecurityNone {
			return true
		}
	}

	return false
}

func (n *Node) commonApplications(avps []AVP) (map[uint32]bool, bool) {
	var offered []uint32

	for _, a := range avps {
		switch a.Code {
		case AVPAuthApplicationID, AVPAcctApplicationID:
			if id, err := a.Unsigned32(); err == nil && a.VendorID == 0 {
				offered = append(offered, id)
			}
		case AVPVendorSpecificApplicationID:
			if a.VendorID != 0 {
				continue
			}

			inner, err := a.Grouped()
			if err != nil {
				continue
			}

			for _, code := range []uint32{AVPAuthApplicationID, AVPAcctApplicationID} {
				if ia, ok := Find(inner, code, 0); ok {
					if id, err := ia.Unsigned32(); err == nil {
						offered = append(offered, id)
					}
				}
			}
		}
	}

	common := make(map[uint32]bool)
	relay := false

	for _, id := range offered {
		if id == RelayApplicationID {
			relay = true
			continue
		}

		for _, app := range n.Applications {
			if app.ID == id {
				common[id] = true
			}
		}
	}

	return common, relay
}
