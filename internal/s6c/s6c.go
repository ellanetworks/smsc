package s6c

import (
	"context"
	"errors"
	"fmt"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/tgpp"
)

var ErrNoHSS = errors.New("no HSS connected")

type Requester interface {
	Do(ctx context.Context, req *diameter.Message) (*diameter.Message, error)
	NewSessionID() string
}

type Router struct {
	Node                 Requester
	Identity             diameter.Identity
	HSSRealm             string
	ServiceCentreAddress string
}

func (r *Router) SendRoutingInfoForSM(ctx context.Context, req s6c.RoutingRequest) (s6c.Routing, string, error) {
	req.ServiceCentreAddress = r.ServiceCentreAddress
	req.GPRSIndicator = true
	req.SMSFSupport = true

	msg, err := s6c.NewSendRoutingInfoForSMRequest(r.envelope(), req)
	if err != nil {
		return s6c.Routing{}, "", err
	}

	ans, err := r.Node.Do(ctx, msg)
	if err != nil {
		return s6c.Routing{}, "", fmt.Errorf("s6c: %w", err)
	}

	routing, err := s6c.ParseSendRoutingInfoForSMAnswer(ans)

	return routing, originHost(ans), err
}

func (r *Router) ReportSMDeliveryStatus(ctx context.Context, rep s6c.DeliveryReport) (s6c.ReportResult, error) {
	rep.ServiceCentreAddress = r.ServiceCentreAddress
	rep.SMSFSupport = true

	msg, err := s6c.NewReportSMDeliveryStatusRequest(r.envelope(), rep)
	if err != nil {
		return s6c.ReportResult{}, err
	}

	ans, err := r.Node.Do(ctx, msg)
	if err != nil {
		return s6c.ReportResult{}, fmt.Errorf("s6c: %w", err)
	}

	return s6c.ParseReportSMDeliveryStatusAnswer(ans)
}

func originHost(ans *diameter.Message) string {
	if a, ok := ans.Find(diameter.AVPOriginHost, 0); ok {
		return a.UTF8String()
	}

	return ""
}

func (r *Router) envelope() tgpp.Envelope {
	return tgpp.Envelope{
		SessionID:        r.Node.NewSessionID(),
		Origin:           r.Identity,
		DestinationRealm: r.HSSRealm,
	}
}
