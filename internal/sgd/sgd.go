package sgd

const ApplicationID uint32 = 16777313

const CommandMOForwardShortMessage uint32 = 8388645

const (
	AVPEPSLocationInformation           uint32 = 1496
	AVPMPSPriority                      uint32 = 1616
	AVPNRCellGlobalIdentity             uint32 = 1726
	AVPSMRPUI                           uint32 = 3301
	AVPSMDeliveryFailureCause           uint32 = 3303
	AVPSMEnumeratedDeliveryFailureCause uint32 = 3304
	AVPSMDiagnosticInfo                 uint32 = 3305
	AVPSMDeliveryOutcome                uint32 = 3316
	AVPSMSMICorrelationID               uint32 = 3324
	AVPOFRFlags                         uint32 = 3328
)

const (
	avpRouteRecord uint32 = 282
	avpDRMP        uint32 = 301
)

const maxSMRPUILength = 200

const (
	CauseUnknownServiceCentre uint32 = 3
	CauseSCCongestion         uint32 = 4
	CauseInvalidSMEAddress    uint32 = 5
	CauseUserNotSCUser        uint32 = 6
)
