package tpdu

func userDataOctets(dcs, udl byte) int {
	if isSeptetCoded(dcs) {
		return (int(udl)*7 + 7) / 8
	}

	return int(udl)
}

func isSeptetCoded(dcs byte) bool {
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

func isUCS2(dcs byte) bool {
	switch dcs >> 4 {
	case 0x0, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7:
		return dcs&0x20 == 0 && (dcs>>2)&0x3 == 0x2
	case 0xe:
		return true
	default:
		return false
	}
}
