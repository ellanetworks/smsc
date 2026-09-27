package diameter

import "fmt"

type AVPKey struct {
	Code     uint32
	VendorID uint32
}

type RequiredAVP struct {
	Key           AVPKey
	MinimumLength int
}

type AVPError struct {
	ResultCode uint32
	AVP        AVP
}

func (e *AVPError) Error() string {
	return fmt.Sprintf("diameter: AVP %d (vendor %d) rejected with result %d", e.AVP.Code, e.AVP.VendorID, e.ResultCode)
}

func CheckAVPs(m *Message, known map[AVPKey]bool, required []RequiredAVP) *AVPError {
	for _, a := range m.AVPs {
		if a.Flags&AVPFlagMandatory != 0 && !known[AVPKey{a.Code, a.VendorID}] {
			return &AVPError{ResultCode: ResultAVPUnsupported, AVP: a}
		}
	}

	for _, r := range required {
		if _, ok := m.Find(r.Key.Code, r.Key.VendorID); !ok {
			return &AVPError{
				ResultCode: ResultMissingAVP,
				AVP:        OctetString(r.Key.Code, AVPFlagMandatory, r.Key.VendorID, make([]byte, r.MinimumLength)),
			}
		}
	}

	return nil
}
