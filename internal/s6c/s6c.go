package s6c

import (
	"context"
	"errors"
	"fmt"

	"github.com/ellanetworks/smsc/diameter"
	"github.com/ellanetworks/smsc/internal/tbcd"
	"github.com/ellanetworks/smsc/internal/tgpp"
)

const ApplicationID uint32 = 16777312

const (
	CommandSendRoutingInfoForSM   uint32 = 8388647
	CommandAlertServiceCentre     uint32 = 8388648
	CommandReportSMDeliveryStatus uint32 = 8388649
)

const (
	avpLMSI                              uint32 = 2400
	avpServingNode                       uint32 = 2401
	avpMMEName                           uint32 = 2402
	avpMSCNumber                         uint32 = 2403
	avpAdditionalServingNode             uint32 = 2406
	avpMMERealm                          uint32 = 2408
	avpSGSNName                          uint32 = 2409
	avpSGSNRealm                         uint32 = 2410
	avpIPSMGWNumber                      uint32 = 3100
	avpIPSMGWName                        uint32 = 3101
	avpIPSMGWRealm                       uint32 = 3112
	avpSMRPMTI                           uint32 = 3308
	avpSRRFlags                          uint32 = 3310
	avpMWDStatus                         uint32 = 3312
	avpMMEAbsentUserDiagnosticSM         uint32 = 3313
	avpMSCAbsentUserDiagnosticSM         uint32 = 3314
	avpSGSNAbsentUserDiagnosticSM        uint32 = 3315
	avpSMDeliveryOutcome                 uint32 = 3316
	avpMMESMDeliveryOutcome              uint32 = 3317
	avpSGSNSMDeliveryOutcome             uint32 = 3319
	avpSMDeliveryCause                   uint32 = 3321
	avpAbsentUserDiagnosticSM            uint32 = 3322
	avpRDRFlags                          uint32 = 3323
	avpSMSMICorrelationID                uint32 = 3324
	avpMaximumUEAvailabilityTime         uint32 = 3329
	avpSMSGMSCAlertEvent                 uint32 = 3333
	avpSMSF3GPPAbsentUserDiagnosticSM    uint32 = 3334
	avpSMSFNon3GPPAbsentUserDiagnosticSM uint32 = 3335
	avpSMSF3GPPSMDeliveryOutcome         uint32 = 3336
	avpSMSFNon3GPPSMDeliveryOutcome      uint32 = 3337
	avpSMSF3GPPNumber                    uint32 = 3338
	avpSMSFNon3GPPNumber                 uint32 = 3339
	avpSMSF3GPPName                      uint32 = 3340
	avpSMSFNon3GPPName                   uint32 = 3341
	avpSMSF3GPPRealm                     uint32 = 3342
	avpSMSFNon3GPPRealm                  uint32 = 3343
	avpSMSF3GPPAddress                   uint32 = 3344
	avpSMSFNon3GPPAddress                uint32 = 3345
)

const (
	smRPMTIDeliver                uint32 = 0
	srrFlagGPRSIndicator          uint32 = 1 << 0
	srrFlagSingleAttempt          uint32 = 1 << 2
	rdrFlagSingleAttempt          uint32 = 1 << 0
	featureListID                 uint32 = 1
	featureSMSFSupport            uint32 = 1 << 0
	MWDStatusSCAddressNotIncluded uint32 = 1 << 0
	MWDStatusMNRF                 uint32 = 1 << 1
	MWDStatusMCEF                 uint32 = 1 << 2
	MWDStatusMNRG                 uint32 = 1 << 3
	MWDStatusMNR5G                uint32 = 1 << 4
	MWDStatusMNR5GN3G             uint32 = 1 << 5
)

var ErrMalformedAnswer = errors.New("s6c: malformed answer")

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

type Request struct {
	MSISDN        string
	SingleAttempt bool
}

type Node struct {
	Name   string
	Realm  string
	Number string
}

type ServingNode struct {
	MME       *Node
	SGSN      *Node
	MSCNumber string
	IPSMGW    *Node
}

func (s ServingNode) empty() bool {
	return s.MME == nil && s.SGSN == nil && s.MSCNumber == "" && s.IPSMGW == nil
}

type ServingNodes struct {
	Serving     *ServingNode
	Additional  *ServingNode
	SMSF3GPP    *Node
	SMSFNon3GPP *Node
}

func (n ServingNodes) empty() bool {
	return n.Serving == nil && n.Additional == nil && n.SMSF3GPP == nil && n.SMSFNon3GPP == nil
}

type Routing struct {
	ServingNodes

	IMSI        string
	LMSI        []byte
	MWDStatus   uint32
	Absent      AbsentUserDiagnostics
	AlertMSISDN string
}

type AbsentUserDiagnostics struct {
	MME         *uint32
	MSC         *uint32
	SGSN        *uint32
	SMSF3GPP    *uint32
	SMSFNon3GPP *uint32
}

type ResultError struct {
	ResultCode   uint32
	Experimental bool
	VendorID     uint32
	MWDStatus    uint32
	Absent       AbsentUserDiagnostics
	AlertMSISDN  string
}

func (e *ResultError) Error() string {
	if e.Experimental {
		return fmt.Sprintf("s6c: request failed with experimental result %d", e.ResultCode)
	}

	return fmt.Sprintf("s6c: request failed with result %d", e.ResultCode)
}

func IsExperimental(err error, code uint32) bool {
	var re *ResultError

	return errors.As(err, &re) && re.Experimental && re.VendorID == tgpp.VendorID && re.ResultCode == code
}

func (r *Router) SendRoutingInfoForSM(ctx context.Context, req Request) (Routing, error) {
	msisdn, err := tbcd.Encode(req.MSISDN)
	if err != nil {
		return Routing{}, fmt.Errorf("s6c: MSISDN: %w", err)
	}

	scAddress, err := tbcd.Encode(r.ServiceCentreAddress)
	if err != nil {
		return Routing{}, fmt.Errorf("s6c: service centre address: %w", err)
	}

	flags := srrFlagGPRSIndicator
	if req.SingleAttempt {
		flags |= srrFlagSingleAttempt
	}

	avps := []diameter.AVP{
		diameter.UTF8String(diameter.AVPSessionID, diameter.AVPFlagMandatory, 0, r.Node.NewSessionID()),
		diameter.Unsigned32(diameter.AVPAuthSessionState, diameter.AVPFlagMandatory, 0, diameter.AuthSessionStateNoStateMaintained),
		diameter.UTF8String(diameter.AVPOriginHost, diameter.AVPFlagMandatory, 0, r.Identity.OriginHost),
		diameter.UTF8String(diameter.AVPOriginRealm, diameter.AVPFlagMandatory, 0, r.Identity.OriginRealm),
		diameter.UTF8String(diameter.AVPDestinationHost, diameter.AVPFlagMandatory, 0, r.HSSHost),
		diameter.UTF8String(diameter.AVPDestinationRealm, diameter.AVPFlagMandatory, 0, r.HSSRealm),
		diameter.OctetString(tgpp.AVPMSISDN, diameter.AVPFlagMandatory, tgpp.VendorID, msisdn),
		diameter.Grouped(tgpp.AVPSupportedFeatures, 0, tgpp.VendorID,
			diameter.Unsigned32(diameter.AVPVendorID, diameter.AVPFlagMandatory, 0, tgpp.VendorID),
			diameter.Unsigned32(tgpp.AVPFeatureListID, 0, tgpp.VendorID, featureListID),
			diameter.Unsigned32(tgpp.AVPFeatureList, 0, tgpp.VendorID, featureSMSFSupport),
		),
		diameter.OctetString(tgpp.AVPSCAddress, diameter.AVPFlagMandatory, tgpp.VendorID, scAddress),
		diameter.Unsigned32(avpSMRPMTI, diameter.AVPFlagMandatory, tgpp.VendorID, smRPMTIDeliver),
		diameter.Unsigned32(avpSRRFlags, diameter.AVPFlagMandatory, tgpp.VendorID, flags),
	}

	ans, err := r.Node.Do(ctx, r.HSSHost, &diameter.Message{
		Flags:         diameter.FlagRequest | diameter.FlagProxiable,
		CommandCode:   CommandSendRoutingInfoForSM,
		ApplicationID: ApplicationID,
		AVPs:          avps,
	})
	if err != nil {
		return Routing{}, fmt.Errorf("s6c: %w", err)
	}

	return parseAnswer(ans)
}

func parseAnswer(ans *diameter.Message) (Routing, error) {
	if err := resultError(ans); err != nil {
		return Routing{}, err
	}

	userName, ok := ans.Find(diameter.AVPUserName, 0)
	if !ok || userName.String() == "" {
		return Routing{}, fmt.Errorf("%w: no User-Name", ErrMalformedAnswer)
	}

	routing := Routing{IMSI: userName.String()}

	if lmsi, ok := ans.Find(avpLMSI, tgpp.VendorID); ok {
		routing.LMSI = lmsi.Data
	}

	if mwd, ok := ans.Find(avpMWDStatus, tgpp.VendorID); ok {
		routing.MWDStatus, _ = mwd.Unsigned32()
	}

	routing.Absent = absentUserDiagnostics(ans)

	var err error

	routing.AlertMSISDN, err = alertMSISDN(ans)
	if err != nil {
		return Routing{}, err
	}

	routing.ServingNodes, err = servingNodes(ans)
	if err != nil {
		return Routing{}, err
	}

	if routing.empty() {
		return Routing{}, fmt.Errorf("%w: no serving node", ErrMalformedAnswer)
	}

	return routing, nil
}

func servingNodes(ans *diameter.Message) (ServingNodes, error) {
	var (
		nodes ServingNodes
		err   error
	)

	nodes.Serving, err = servingNode(ans, avpServingNode)
	if err != nil {
		return ServingNodes{}, err
	}

	nodes.Additional, err = servingNode(ans, avpAdditionalServingNode)
	if err != nil {
		return ServingNodes{}, err
	}

	nodes.SMSF3GPP, err = smsfAddress(ans, avpSMSF3GPPAddress, avpSMSF3GPPName, avpSMSF3GPPRealm, avpSMSF3GPPNumber)
	if err != nil {
		return ServingNodes{}, err
	}

	nodes.SMSFNon3GPP, err = smsfAddress(ans, avpSMSFNon3GPPAddress, avpSMSFNon3GPPName, avpSMSFNon3GPPRealm, avpSMSFNon3GPPNumber)
	if err != nil {
		return ServingNodes{}, err
	}

	return nodes, nil
}

func alertMSISDN(m *diameter.Message) (string, error) {
	a, ok := m.Find(tgpp.AVPUserIdentifier, tgpp.VendorID)
	if !ok {
		return "", nil
	}

	msisdn, err := userIdentifierMSISDN(a)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrMalformedAnswer, err)
	}

	return msisdn, nil
}

func userIdentifierMSISDN(a diameter.AVP) (string, error) {
	inner, err := a.Grouped()
	if err != nil {
		return "", fmt.Errorf("User-Identifier: %w", err)
	}

	msisdn, ok := diameter.Find(inner, tgpp.AVPMSISDN, tgpp.VendorID)
	if !ok {
		return "", nil
	}

	digits, err := tbcd.Decode(msisdn.Data)
	if err != nil {
		return "", fmt.Errorf("User-Identifier MSISDN: %w", err)
	}

	return digits, nil
}

func resultError(ans *diameter.Message) error {
	if rc, ok := ans.Find(diameter.AVPResultCode, 0); ok {
		code, err := rc.Unsigned32()
		if err != nil {
			return fmt.Errorf("%w: Result-Code", ErrMalformedAnswer)
		}

		if code == diameter.ResultSuccess {
			return nil
		}

		return &ResultError{ResultCode: code}
	}

	er, ok := ans.Find(diameter.AVPExperimentalResult, 0)
	if !ok {
		return fmt.Errorf("%w: no Result-Code or Experimental-Result", ErrMalformedAnswer)
	}

	inner, err := er.Grouped()
	if err != nil {
		return fmt.Errorf("%w: Experimental-Result", ErrMalformedAnswer)
	}

	vendorAVP, ok := diameter.Find(inner, diameter.AVPVendorID, 0)
	if !ok {
		return fmt.Errorf("%w: no Vendor-Id in Experimental-Result", ErrMalformedAnswer)
	}

	vendorID, err := vendorAVP.Unsigned32()
	if err != nil {
		return fmt.Errorf("%w: Experimental-Result Vendor-Id", ErrMalformedAnswer)
	}

	codeAVP, ok := diameter.Find(inner, diameter.AVPExperimentalResultCode, 0)
	if !ok {
		return fmt.Errorf("%w: no Experimental-Result-Code", ErrMalformedAnswer)
	}

	code, err := codeAVP.Unsigned32()
	if err != nil {
		return fmt.Errorf("%w: Experimental-Result-Code", ErrMalformedAnswer)
	}

	e := &ResultError{ResultCode: code, Experimental: true, VendorID: vendorID}

	e.AlertMSISDN, err = alertMSISDN(ans)
	if err != nil {
		return err
	}

	if mwd, ok := ans.Find(avpMWDStatus, tgpp.VendorID); ok {
		e.MWDStatus, _ = mwd.Unsigned32()
	}

	e.Absent = absentUserDiagnostics(ans)

	return e
}

func absentUserDiagnostics(ans *diameter.Message) AbsentUserDiagnostics {
	return AbsentUserDiagnostics{
		MME:         optionalUnsigned32(ans, avpMMEAbsentUserDiagnosticSM),
		MSC:         optionalUnsigned32(ans, avpMSCAbsentUserDiagnosticSM),
		SGSN:        optionalUnsigned32(ans, avpSGSNAbsentUserDiagnosticSM),
		SMSF3GPP:    optionalUnsigned32(ans, avpSMSF3GPPAbsentUserDiagnosticSM),
		SMSFNon3GPP: optionalUnsigned32(ans, avpSMSFNon3GPPAbsentUserDiagnosticSM),
	}
}

func optionalUnsigned32(m *diameter.Message, code uint32) *uint32 {
	a, ok := m.Find(code, tgpp.VendorID)
	if !ok {
		return nil
	}

	v, err := a.Unsigned32()
	if err != nil {
		return nil
	}

	return &v
}

func servingNode(m *diameter.Message, code uint32) (*ServingNode, error) {
	a, ok := m.Find(code, tgpp.VendorID)
	if !ok {
		return nil, nil
	}

	inner, err := a.Grouped()
	if err != nil {
		return nil, fmt.Errorf("%w: serving node", ErrMalformedAnswer)
	}

	identity := func(code uint32) string {
		v, _ := diameter.Find(inner, code, tgpp.VendorID)
		return v.String()
	}

	number := func(code uint32) (string, error) {
		v, ok := diameter.Find(inner, code, tgpp.VendorID)
		if !ok {
			return "", nil
		}

		digits, err := tbcd.Decode(v.Data)
		if err != nil {
			return "", fmt.Errorf("%w: serving node number: %w", ErrMalformedAnswer, err)
		}

		return digits, nil
	}

	mmeNumber, err := number(tgpp.AVPMMENumberForMTSMS)
	if err != nil {
		return nil, err
	}

	sgsnNumber, err := number(tgpp.AVPSGSNNumber)
	if err != nil {
		return nil, err
	}

	mscNumber, err := number(avpMSCNumber)
	if err != nil {
		return nil, err
	}

	ipsmgwNumber, err := number(avpIPSMGWNumber)
	if err != nil {
		return nil, err
	}

	node := &ServingNode{MSCNumber: mscNumber}

	if name := identity(avpMMEName); name != "" && mmeNumber != "" {
		node.MME = &Node{Name: name, Realm: identity(avpMMERealm), Number: mmeNumber}
	}

	if sgsnNumber != "" {
		node.SGSN = &Node{Name: identity(avpSGSNName), Realm: identity(avpSGSNRealm), Number: sgsnNumber}
	}

	if ipsmgwNumber != "" {
		node.IPSMGW = &Node{Name: identity(avpIPSMGWName), Realm: identity(avpIPSMGWRealm), Number: ipsmgwNumber}
	}

	if node.empty() {
		return nil, fmt.Errorf("%w: serving node with no delivery target", ErrMalformedAnswer)
	}

	return node, nil
}

func smsfAddress(m *diameter.Message, groupCode, nameCode, realmCode, numberCode uint32) (*Node, error) {
	a, ok := m.Find(groupCode, tgpp.VendorID)
	if !ok {
		return nil, nil
	}

	inner, err := a.Grouped()
	if err != nil {
		return nil, fmt.Errorf("%w: SMSF address", ErrMalformedAnswer)
	}

	node := &Node{}

	if v, ok := diameter.Find(inner, nameCode, tgpp.VendorID); ok {
		node.Name = v.String()
	}

	if v, ok := diameter.Find(inner, realmCode, tgpp.VendorID); ok {
		node.Realm = v.String()
	}

	if v, ok := diameter.Find(inner, numberCode, tgpp.VendorID); ok {
		digits, err := tbcd.Decode(v.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: SMSF number: %w", ErrMalformedAnswer, err)
		}

		node.Number = digits
	}

	return node, nil
}
