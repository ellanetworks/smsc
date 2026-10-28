package tpdu

const (
	ProtocolANSI136RData     byte = 0x7c
	ProtocolUSIMDataDownload byte = 0x7f
)

func IsReplaceType(pid byte) bool {
	return pid >= 0x41 && pid <= 0x47
}

func IsDeviceTrigger(pid byte) bool {
	return pid == 0x48
}

func RequestsTelematicInterworking(pid byte) bool {
	return pid&0xe0 == 0x20 && pid != 0x3f
}
