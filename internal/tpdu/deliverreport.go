package tpdu

import "fmt"

const (
	parameterIndicatorPID       = 0x01
	parameterIndicatorDCS       = 0x02
	parameterIndicatorUDL       = 0x04
	parameterIndicatorExtension = 0x80
)

// DeliverReport is an SMS-DELIVER-REPORT (TS 23.040 clause 9.2.2.1a): the answer of an MS to an SMS-DELIVER, in the
// RP-ACK or the RP-ERROR. Its optional parameters are nil when the TP-PI says they are absent.
type DeliverReport struct {
	UserDataHeader bool
	// FailureCause is the TP-FCS of a report for RP-ERROR.
	FailureCause       *byte
	ProtocolIdentifier *byte
	DataCodingScheme   *byte
	UserDataLength     *uint8
	UserData           []byte
}

// DecodeDeliverReport decodes an SMS-DELIVER-REPORT for RP-ACK when ack is set, and for RP-ERROR otherwise. Both have
// the same TP-MTI, so only the transfer that carried it tells them apart. A report that ends before its mandatory TP-PI
// has no optional parameters. A report for RP-ERROR with unused bits set in its first octet has only the TP-FCS
// "Unspecified error cause"; for RP-ACK, those bits are ignored (TS 23.040 clause 9.2.2.1a).
func DecodeDeliverReport(b []byte, ack bool) (DeliverReport, error) {
	if len(b) == 0 {
		return DeliverReport{}, fmt.Errorf("sms-deliver-report: %w", errTruncated)
	}

	if b[0]&0x3 != mtiDeliver {
		return DeliverReport{}, fmt.Errorf("sms-deliver-report: message type indicator %d: %w", b[0]&0x3,
			ErrUnsupportedMessageType)
	}

	r := DeliverReport{UserDataHeader: b[0]&0x40 != 0}
	rest := b[1:]

	if !ack {
		if len(rest) == 0 {
			return DeliverReport{}, fmt.Errorf("sms-deliver-report: failure cause %w", errTruncated)
		}

		if b[0]&deliverReportUnusedBits != 0 {
			fcs := FailureUnspecified
			return DeliverReport{FailureCause: &fcs}, nil
		}

		fcs := rest[0]
		r.FailureCause = &fcs
		rest = rest[1:]
	}

	if len(rest) == 0 {
		return r, nil
	}

	// Each TP-PI octet with its extension bit set is followed by another. Reserved bits mean that more information
	// follows the TP-UD, which the receiver discards (TS 23.040 clause 9.2.3.27).
	pi := rest[0]
	reserved := pi&0x78 != 0

	for rest[0]&parameterIndicatorExtension != 0 {
		rest = rest[1:]
		if len(rest) == 0 {
			return DeliverReport{}, fmt.Errorf("sms-deliver-report: parameter indicator %w", errTruncated)
		}

		reserved = reserved || rest[0]&^parameterIndicatorExtension != 0
	}

	rest = rest[1:]

	take := func(name string) (*byte, error) {
		if len(rest) == 0 {
			return nil, fmt.Errorf("sms-deliver-report: %s %w", name, errTruncated)
		}

		v := rest[0]
		rest = rest[1:]

		return &v, nil
	}

	var err error

	if pi&parameterIndicatorPID != 0 {
		if r.ProtocolIdentifier, err = take("protocol identifier"); err != nil {
			return DeliverReport{}, err
		}
	}

	if pi&parameterIndicatorDCS != 0 {
		if r.DataCodingScheme, err = take("data coding scheme"); err != nil {
			return DeliverReport{}, err
		}
	}

	if pi&parameterIndicatorUDL != 0 {
		if r.UserDataLength, err = take("user data length"); err != nil {
			return DeliverReport{}, err
		}

		// Without a TP-DCS, the TP-UD is in the GSM 7 bit default alphabet.
		var dcs byte
		if r.DataCodingScheme != nil {
			dcs = *r.DataCodingScheme
		}

		n := userDataOctets(dcs, *r.UserDataLength)
		if len(rest) < n {
			return DeliverReport{}, fmt.Errorf("sms-deliver-report: user data %w", errTruncated)
		}

		r.UserData = append([]byte{}, rest[:n]...)
		rest = rest[n:]
	}

	if len(rest) > 0 && !reserved {
		return DeliverReport{}, fmt.Errorf("sms-deliver-report: %d trailing octets", len(rest))
	}

	return r, nil
}
