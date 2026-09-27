package tgpp

const VendorID uint32 = 10415

const (
	AVPSupportedFeatures uint32 = 628
	AVPFeatureListID     uint32 = 629
	AVPFeatureList       uint32 = 630
	AVPMSISDN            uint32 = 701
	AVPSGSNNumber        uint32 = 1489
	AVPMMENumberForMTSMS uint32 = 1645
	AVPUserIdentifier    uint32 = 3102
	AVPSCAddress         uint32 = 3300
)

const (
	ResultErrorUserUnknown          uint32 = 5001
	ResultErrorAbsentUser           uint32 = 5550
	ResultErrorUserBusyForMTSMS     uint32 = 5551
	ResultErrorFacilityNotSupported uint32 = 5552
	ResultErrorIllegalUser          uint32 = 5553
	ResultErrorIllegalEquipment     uint32 = 5554
	ResultErrorSMDeliveryFailure    uint32 = 5555
	ResultErrorServiceNotSubscribed uint32 = 5556
	ResultErrorServiceBarred        uint32 = 5557
	ResultErrorMWDListFull          uint32 = 5558
)
