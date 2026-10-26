package tpdu

import "fmt"

type informationElement struct {
	id   byte
	data []byte
}

// userDataHeader returns the user data header at the start of a TP-UD, its length octet included (TS 23.040 clause
// 9.2.3.24), or nil when TP-UDHI is not set.
func userDataHeader(udhi bool, ud []byte) ([]byte, error) {
	if !udhi {
		return nil, nil
	}

	if len(ud) == 0 || int(ud[0])+1 > len(ud) {
		return nil, fmt.Errorf("user data header: %w", errTruncated)
	}

	return ud[:1+int(ud[0])], nil
}

// informationElements splits the information elements of a user data header, its length octet excluded. It returns
// false when they are malformed.
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
