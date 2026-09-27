package tpdu

func IsReplaceType(pid byte) bool {
	return pid >= 0x41 && pid <= 0x47
}

func RequestsTelematicInterworking(pid byte) bool {
	return pid&0xe0 == 0x20 && pid != 0x3f
}
