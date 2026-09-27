package tgpp

const VendorID uint32 = 10415

const (
	AVPSupportedFeatures uint32 = 628
	AVPFeatureListID     uint32 = 629
	AVPFeatureList       uint32 = 630
	AVPMSISDN            uint32 = 701
	AVPUserIdentifier    uint32 = 3102
	AVPSCAddress         uint32 = 3300
)

const (
	ResultErrorUserUnknown          uint32 = 5001
	ResultErrorAbsentUser           uint32 = 5550
	ResultErrorFacilityNotSupported uint32 = 5552
	ResultErrorSMDeliveryFailure    uint32 = 5555
	ResultErrorServiceNotSubscribed uint32 = 5556
	ResultErrorServiceBarred        uint32 = 5557
)
