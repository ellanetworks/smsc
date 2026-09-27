package diameter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/ellanetworks/core/sctp"
)

const DefaultWatchdogInterval = 30 * time.Second

var minWatchdogInterval = 6 * time.Second

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

type Handler interface {
	ServeDiameter(ctx context.Context, c *Conn, req *Message) *Message
}

type HandlerFunc func(ctx context.Context, c *Conn, req *Message) *Message

func (f HandlerFunc) ServeDiameter(ctx context.Context, c *Conn, req *Message) *Message {
	return f(ctx, c, req)
}

type Server struct {
	Identity         Identity
	Applications     []Application
	Handler          Handler
	WatchdogInterval time.Duration
	Logger           *slog.Logger

	sctpServer *sctp.Server
	conns      sync.Map
	jitter     time.Duration
}

func (s *Server) Serve(ctx context.Context, ln *sctp.Listener) error {
	if err := s.validate(); err != nil {
		return err
	}

	if s.Logger == nil {
		s.Logger = slog.Default()
	}

	if s.WatchdogInterval == 0 {
		s.WatchdogInterval = DefaultWatchdogInterval
	}

	s.jitter = watchdogJitter

	s.sctpServer = sctp.NewServer(sctp.Config{
		PPID:   PPID,
		Name:   "Diameter",
		Logger: s.Logger,
	}, sctp.Callbacks{
		Dispatch: s.dispatch,
		OnDisconnect: func(sc *sctp.SCTPConn) {
			if v, ok := s.conns.LoadAndDelete(sc); ok {
				v.(*Conn).disconnect()
			}
		},
	})

	s.sctpServer.Serve(ctx, ln)

	return nil
}

func (s *Server) validate() error {
	switch {
	case s.Identity.OriginHost == "":
		return errors.New("diameter: Identity.OriginHost is required")
	case s.Identity.OriginRealm == "":
		return errors.New("diameter: Identity.OriginRealm is required")
	case len(s.Identity.HostIPAddresses) == 0:
		return errors.New("diameter: at least one Identity.HostIPAddresses entry is required")
	case s.Identity.ProductName == "":
		return errors.New("diameter: Identity.ProductName is required")
	case len(s.Applications) == 0:
		return errors.New("diameter: at least one Application is required")
	case s.Handler == nil:
		return errors.New("diameter: Handler is required")
	case s.WatchdogInterval != 0 && s.WatchdogInterval < minWatchdogInterval:
		return fmt.Errorf("diameter: WatchdogInterval %s is below the %s minimum", s.WatchdogInterval, minWatchdogInterval)
	}

	return nil
}

func (s *Server) Shutdown(ctx context.Context) {
	if s.sctpServer == nil {
		return
	}

	var wg sync.WaitGroup

	s.conns.Range(func(_, v any) bool {
		c := v.(*Conn)
		if !c.state.CompareAndSwap(int32(stateOpen), int32(stateClosing)) {
			return true
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			c.send(&Message{
				Flags:       FlagRequest,
				CommandCode: CommandDisconnectPeer,
				HopByHopID:  c.hopByHop.Add(1),
				EndToEndID:  c.endToEnd.Add(1),
				AVPs: []AVP{
					UTF8String(AVPOriginHost, AVPFlagMandatory, 0, s.Identity.OriginHost),
					UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, s.Identity.OriginRealm),
					Unsigned32(AVPDisconnectCause, AVPFlagMandatory, 0, DisconnectCauseRebooting),
				},
			})

			select {
			case <-c.disconnected:
			case <-ctx.Done():
			}
		}()

		return true
	})

	wg.Wait()
	s.sctpServer.Shutdown(ctx)
}

func (s *Server) dispatch(ctx context.Context, sc *sctp.SCTPConn, b []byte) {
	v, _ := s.conns.LoadOrStore(sc, newConn(s, sc))
	v.(*Conn).receive(ctx, b)
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

	common, relay := c.srv.commonApplications(req.AVPs)
	if len(common) == 0 && !relay {
		c.logger.Warn("rejecting Diameter peer with no common application", slog.String("peer", host.String()))
		c.send(c.capabilitiesAnswer(req, ResultNoCommonApplication))
		c.close()

		return
	}

	c.peerHost = host.String()
	c.peerRealm = realm.String()
	c.peerIsRelay = relay
	c.commonAppIDs = common
	c.state.Store(int32(stateOpen))

	c.send(c.capabilitiesAnswer(req, ResultSuccess))

	c.logger.Info("Diameter peer connected", slog.String("peer", c.peerHost), slog.String("realm", c.peerRealm))

	go c.watchdog(c.srv.WatchdogInterval)
}

func (c *Conn) capabilitiesAnswer(req *Message, resultCode uint32) *Message {
	ans := &Message{
		CommandCode: CommandCapabilitiesExchange,
		HopByHopID:  req.HopByHopID,
		EndToEndID:  req.EndToEndID,
		AVPs: []AVP{
			Unsigned32(AVPResultCode, AVPFlagMandatory, 0, resultCode),
			UTF8String(AVPOriginHost, AVPFlagMandatory, 0, c.srv.Identity.OriginHost),
			UTF8String(AVPOriginRealm, AVPFlagMandatory, 0, c.srv.Identity.OriginRealm),
		},
	}

	for _, addr := range c.srv.Identity.HostIPAddresses {
		ans.AVPs = append(ans.AVPs, Address(AVPHostIPAddress, AVPFlagMandatory, 0, addr))
	}

	ans.AVPs = append(ans.AVPs,
		Unsigned32(AVPVendorID, AVPFlagMandatory, 0, c.srv.Identity.VendorID),
		UTF8String(AVPProductName, 0, 0, c.srv.Identity.ProductName),
	)

	seenVendors := make(map[uint32]bool)

	for _, app := range c.srv.Applications {
		if app.VendorID != 0 && !seenVendors[app.VendorID] {
			seenVendors[app.VendorID] = true
			ans.AVPs = append(ans.AVPs, Unsigned32(AVPSupportedVendorID, AVPFlagMandatory, 0, app.VendorID))
		}
	}

	for _, app := range c.srv.Applications {
		if app.VendorID == 0 {
			ans.AVPs = append(ans.AVPs, Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, app.ID))
			continue
		}

		ans.AVPs = append(ans.AVPs, Grouped(AVPVendorSpecificApplicationID, AVPFlagMandatory, 0,
			Unsigned32(AVPVendorID, AVPFlagMandatory, 0, app.VendorID),
			Unsigned32(AVPAuthApplicationID, AVPFlagMandatory, 0, app.ID),
		))
	}

	return ans
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

func (s *Server) commonApplications(avps []AVP) (map[uint32]bool, bool) {
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

		for _, app := range s.Applications {
			if app.ID == id {
				common[id] = true
			}
		}
	}

	return common, relay
}
