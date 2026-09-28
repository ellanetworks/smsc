package s6c

import (
	"context"
	"fmt"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
)

type Requester interface {
	Do(ctx context.Context, peerHost string, req *diameter.Message) (*diameter.Message, error)
	NewSessionID() string
}

type Router struct {
	Node                 Requester
	Identity             diameter.Identity
	HSSHost              string
	HSSRealm             string
	ServiceCentreAddress string
}

func (r *Router) SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (s6c.Routing, error) {
	req.ServiceCentreAddress = r.ServiceCentreAddress
	req.GPRSIndicator = true
	req.SMSFSupport = true

	msg, err := s6c.NewSendRoutingInfoForSMRequest(r.envelope(), req)
	if err != nil {
		return s6c.Routing{}, err
	}

	ans, err := r.Node.Do(ctx, r.HSSHost, msg)
	if err != nil {
		return s6c.Routing{}, fmt.Errorf("s6c: %w", err)
	}

	return s6c.ParseSendRoutingInfoForSMAnswer(ans)
}

func (r *Router) ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error) {
	rep.ServiceCentreAddress = r.ServiceCentreAddress
	rep.SMSFSupport = true

	msg, err := s6c.NewReportSMDeliveryStatusRequest(r.envelope(), rep)
	if err != nil {
		return s6c.ReportResult{}, err
	}

	ans, err := r.Node.Do(ctx, r.HSSHost, msg)
	if err != nil {
		return s6c.ReportResult{}, fmt.Errorf("s6c: %w", err)
	}

	return s6c.ParseReportSMDeliveryStatusAnswer(ans)
}

func (r *Router) envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        r.Node.NewSessionID(),
		Origin:           r.Identity,
		DestinationHost:  r.HSSHost,
		DestinationRealm: r.HSSRealm,
	}
}
