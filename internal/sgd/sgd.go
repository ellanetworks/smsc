package sgd

const ApplicationID uint32 = 16777313

const (
	CommandMOForwardShortMessage uint32 = 8388645
	CommandMTForwardShortMessage uint32 = 8388646
)

const (
	AVPEPSLocationInformation           uint32 = 1496
	AVPMPSPriority                      uint32 = 1616
	AVPNRCellGlobalIdentity             uint32 = 1726
	AVPSMRPUI                           uint32 = 3301
	AVPTFRFlags                         uint32 = 3302
	AVPSMDeliveryFailureCause           uint32 = 3303
	AVPSMEnumeratedDeliveryFailureCause uint32 = 3304
	AVPSMDiagnosticInfo                 uint32 = 3305
	AVPSMDeliveryTimer                  uint32 = 3306
	AVPSMDeliveryStartTime              uint32 = 3307
	AVPAbsentUserDiagnosticSM           uint32 = 3322
	AVPSMDeliveryOutcome                uint32 = 3316
	AVPSMSMICorrelationID               uint32 = 3324
	AVPOFRFlags                         uint32 = 3328
)

const maxSMRPUILength = 200

const TFRFlagMoreMessagesToSend uint32 = 1 << 0

const (
	CauseMemoryCapacityExceeded uint32 = 0
	CauseEquipmentProtocolError uint32 = 1
	CauseEquipmentNotSMEquipped uint32 = 2
	CauseUnknownServiceCentre   uint32 = 3
	CauseSCCongestion           uint32 = 4
	CauseInvalidSMEAddress      uint32 = 5
	CauseUserNotSCUser          uint32 = 6
)
