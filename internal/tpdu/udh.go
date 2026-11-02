package tpdu

import "fmt"

type informationElement struct {
	id   byte
	data []byte
}

// TS 23.040 clause 9.2.3.24
func userDataHeader(udhi bool, ud []byte) ([]byte, error) {
	if !udhi {
		return nil, nil
	}

	if len(ud) == 0 || int(ud[0])+1 > len(ud) {
		return nil, fmt.Errorf("user data header: %w", errTruncated)
	}

	return ud[:1+int(ud[0])], nil
}

func SplitUserData(udhi bool, ud []byte) (header, rest []byte, err error) {
	header, err = userDataHeader(udhi, ud)
	if err != nil {
		return nil, nil, err
	}

	return header, ud[len(header):], nil
}

// TS 23.040 clause 9.2.3.24
func ValidUserDataHeader(h []byte) bool {
	if len(h) == 0 || int(h[0]) != len(h)-1 {
		return false
	}

	_, ok := informationElements(h[1:])

	return ok
}

// TS 23.040 clause 9.2.3.24
func HasApplicationPort(h []byte) bool {
	if len(h) == 0 {
		return false
	}

	ies, ok := informationElements(h[1:])
	if !ok {
		return false
	}

	for _, ie := range ies {
		if ie.id == ieiApplicationPort8 || ie.id == ieiApplicationPort16 {
			return true
		}
	}

	return false
}

func informationElements(ies []byte) ([]informationElement, bool) {
	var out []informationElement

	for len(ies) > 0 {
		if len(ies) < 2 || len(ies) < 2+int(ies[1]) {
			return nil, false
		}

		length := int(ies[1])
		out = append(out, informationElement{id: ies[0], data: ies[2 : 2+length]})
		ies = ies[2+length:]
	}

	return out, true
}
