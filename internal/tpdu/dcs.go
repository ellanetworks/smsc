package tpdu

func userDataOctets(dcs, udl byte) int {
	if IsSeptetCoded(dcs) {
		return (int(udl)*7 + 7) / 8
	}

	return int(udl)
}

// IsSeptetCoded is whether a TP-DCS codes the TP-UD in the GSM 7 bit default alphabet, so that the TP-UDL counts septets
// (TS 23.040 clause 9.2.3.16). Reserved codings count as the GSM 7 bit default alphabet (TS 23.038 clause 4).
func IsSeptetCoded(dcs byte) bool {
	switch dcs >> 4 {
	case 0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7:
		if dcs&0x20 != 0 {
			return false
		}

		charset := (dcs >> 2) & 0x3

		return charset == 0x0 || charset == 0x3
	case 0xe:
		return false
	case 0xf:
		return dcs&0x04 == 0
	default:
		return true
	}
}

// MessageClass returns the message class that a TP-DCS indicates, if any (TS 23.038 clause 4).
func MessageClass(dcs byte) (uint8, bool) {
	switch dcs >> 4 {
	case 0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7:
		if dcs&0x10 == 0 {
			return 0, false
		}

		return dcs & 0x3, true
	case 0xf:
		return dcs & 0x3, true
	default:
		return 0, false
	}
}

// IsUCS2 is whether a TP-DCS codes the TP-UD in UCS2 (TS 23.038 clause 4).
func IsUCS2(dcs byte) bool {
	switch dcs >> 4 {
	case 0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7:
		return dcs&0x20 == 0 && (dcs>>2)&0x3 == 0x2
	case 0xe:
		return true
	default:
		return false
	}
}
