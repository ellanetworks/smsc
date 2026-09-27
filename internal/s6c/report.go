package s6c

import (
	"context"
	"errors"
	"fmt"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/tbcd"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

const (
	DeliveryCauseMemoryCapacityExceeded uint32 = 0
	DeliveryCauseAbsentUser             uint32 = 1
	DeliveryCauseSuccessfulTransfer     uint32 = 2
)

type DeliveryOutcome struct {
	Cause            uint32
	AbsentDiagnostic *uint32
}

type DeliveryReport struct {
	MSISDN        string
	SingleAttempt bool
	MME           *DeliveryOutcome
	SGSN          *DeliveryOutcome
	SMSF3GPP      *DeliveryOutcome
	SMSFNon3GPP   *DeliveryOutcome
	Failed        ServingNodes
}

type ReportResult struct {
	ServingNodes

	AlertMSISDN string
}

func (r *Router) ReportSMDeliveryStatus(ctx context.Context, rep DeliveryReport) (ReportResult, error) {
	msisdn, err := tbcd.Encode(rep.MSISDN)
	if err != nil {
		return ReportResult{}, fmt.Errorf("s6c: MSISDN: %w", err)
	}

	scAddress, err := tbcd.Encode(r.ServiceCentreAddress)
	if err != nil {
		return ReportResult{}, fmt.Errorf("s6c: service centre address: %w", err)
	}

	var outcomes []diameter.AVP

	for _, o := range []struct {
		code    uint32
		flags   uint8
		outcome *DeliveryOutcome
	}{
		{avpMMESMDeliveryOutcome, diameter.AVPFlagMandatory, rep.MME},
		{avpSGSNSMDeliveryOutcome, diameter.AVPFlagMandatory, rep.SGSN},
		{avpSMSF3GPPSMDeliveryOutcome, 0, rep.SMSF3GPP},
		{avpSMSFNon3GPPSMDeliveryOutcome, 0, rep.SMSFNon3GPP},
	} {
		if o.outcome != nil {
			outcomes = append(outcomes, deliveryOutcome(o.code, o.flags, *o.outcome))
		}
	}

	if len(outcomes) == 0 {
		return ReportResult{}, errors.New("s6c: delivery report without an outcome")
	}

	avps := []diameter.AVP{
		diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, r.Node.NewSessionID()),
		diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
		diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, r.Identity.OriginHost),
		diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, r.Identity.OriginRealm),
		diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, r.HSSHost),
		diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, r.HSSRealm),
		diameter.Grouped(tgpp.AVPUserIdentifier, diameter.AVPFlagMandatory, tgpp.VendorID,
			diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, msisdn),
		),
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, scAddress),
		diameter.Grouped(avpSMDeliveryOutcome, diameter.AVPFlagMandatory, tgpp.VendorID, outcomes...),
	}

	if rep.SingleAttempt {
		avps = append(avps, diameter.Unsigned32(avpRDRFlags, 0, tgpp.VendorID, rdrFlagSingleAttempt))
	}

	failed, err := failedNodes(rep.Failed)
	if err != nil {
		return ReportResult{}, err
	}

	avps = append(avps, failed...)

	ans, err := r.Node.Do(ctx, r.HSSHost, &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   CommandReportSMDeliveryStatus,
		ApplicationID: ApplicationID,
		AVPs:          avps,
	})
	if err != nil {
		return ReportResult{}, fmt.Errorf("s6c: %w", err)
	}

	return parseReportAnswer(ans)
}

func deliveryOutcome(code uint32, flags uint8, o DeliveryOutcome) diameter.AVP {
	inner := []diameter.AVP{
		diameter.Unsigned32(avpSMDeliveryCause, diameter.AVPFlagMandatory, tgpp.VendorID, o.Cause),
	}

	if o.AbsentDiagnostic != nil {
		inner = append(inner, diameter.Unsigned32(avpAbsentUserDiagnosticSM, diameter.AVPFlagMandatory, tgpp.VendorID, *o.AbsentDiagnostic))
	}

	return diameter.Grouped(code, flags, tgpp.VendorID, inner...)
}

func parseReportAnswer(ans *diameter.Message) (ReportResult, error) {
	if err := resultError(ans); err != nil {
		return ReportResult{}, err
	}

	var (
		result ReportResult
		err    error
	)

	result.AlertMSISDN, err = alertMSISDN(ans)
	if err != nil {
		return ReportResult{}, err
	}

	result.ServingNodes, err = servingNodes(ans)
	if err != nil {
		return ReportResult{}, err
	}

	return result, nil
}

func failedNodes(n ServingNodes) ([]diameter.AVP, error) {
	var avps []diameter.AVP

	for _, sn := range []struct {
		code uint32
		node *ServingNode
	}{
		{avpServingNode, n.Serving},
		{avpAdditionalServingNode, n.Additional},
	} {
		if sn.node == nil {
			continue
		}

		a, err := servingNodeAVP(sn.code, *sn.node)
		if err != nil {
			return nil, err
		}

		avps = append(avps, a)
	}

	for _, smsf := range []struct {
		node                                       *Node
		groupCode, nameCode, realmCode, numberCode uint32
	}{
		{n.SMSF3GPP, avpSMSF3GPPAddress, avpSMSF3GPPName, avpSMSF3GPPRealm, avpSMSF3GPPNumber},
		{n.SMSFNon3GPP, avpSMSFNon3GPPAddress, avpSMSFNon3GPPName, avpSMSFNon3GPPRealm, avpSMSFNon3GPPNumber},
	} {
		if smsf.node == nil {
			continue
		}

		inner, err := nodeAVPs(*smsf.node, smsf.nameCode, 0, smsf.realmCode, 0, smsf.numberCode, 0)
		if err != nil {
			return nil, err
		}

		avps = append(avps, diameter.Grouped(smsf.groupCode, 0, tgpp.VendorID, inner...))
	}

	return avps, nil
}

func servingNodeAVP(code uint32, n ServingNode) (diameter.AVP, error) {
	var inner []diameter.AVP

	if n.SGSN != nil {
		avps, err := nodeAVPs(*n.SGSN, avpSGSNName, 0, avpSGSNRealm, 0, tgpp.AVPSGSNNumber, diameter.AVPFlagMandatory)
		if err != nil {
			return diameter.AVP{}, err
		}

		inner = append(inner, avps...)
	}

	if n.MME != nil {
		avps, err := nodeAVPs(*n.MME, avpMMEName, diameter.AVPFlagMandatory, avpMMERealm, diameter.AVPFlagMandatory,
			tgpp.AVPMMENumberForMTSMS, diameter.AVPFlagMandatory)
		if err != nil {
			return diameter.AVP{}, err
		}

		inner = append(inner, avps...)
	}

	if n.MSCNumber != "" {
		number, err := tbcd.Encode(n.MSCNumber)
		if err != nil {
			return diameter.AVP{}, fmt.Errorf("s6c: MSC number: %w", err)
		}

		inner = append(inner, diameter.OctetString(avpMSCNumber, diameter.AVPFlagMandatory, tgpp.VendorID, number))
	}

	if n.IPSMGW != nil {
		avps, err := nodeAVPs(*n.IPSMGW, avpIPSMGWName, diameter.AVPFlagMandatory, avpIPSMGWRealm, diameter.AVPFlagMandatory,
			avpIPSMGWNumber, diameter.AVPFlagMandatory)
		if err != nil {
			return diameter.AVP{}, err
		}

		inner = append(inner, avps...)
	}

	return diameter.Grouped(code, diameter.AVPFlagMandatory, tgpp.VendorID, inner...), nil
}

func nodeAVPs(n Node, nameCode uint32, nameFlags uint8, realmCode uint32, realmFlags uint8, numberCode uint32, numberFlags uint8) ([]diameter.AVP, error) {
	var avps []diameter.AVP

	if n.Name != "" {
		avps = append(avps, diameter.UTF8String(nameCode, nameFlags, tgpp.VendorID, n.Name))
	}

	if n.Realm != "" {
		avps = append(avps, diameter.UTF8String(realmCode, realmFlags, tgpp.VendorID, n.Realm))
	}

	if n.Number != "" {
		number, err := tbcd.Encode(n.Number)
		if err != nil {
			return nil, fmt.Errorf("s6c: serving node number: %w", err)
		}

		avps = append(avps, diameter.OctetString(numberCode, numberFlags, tgpp.VendorID, number))
	}

	return avps, nil
}
