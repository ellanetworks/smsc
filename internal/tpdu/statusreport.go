package tpdu

import (
	"fmt"
	"time"
)

// mtiStatusReport is the TP-MTI of an SMS-STATUS-REPORT, from the SC to the MS. The same value is an SMS-COMMAND from
// the MS to the SC.
const mtiStatusReport = 0x2

// StatusReport is an SMS-STATUS-REPORT (TS 23.040 clause 9.2.2.3) on a short message that an SMS-SUBMIT submitted,
// with its mandatory parameters only: TP-SRQ is always 0, and the SC sets no TP-LP, since it neither forwards nor
// spawns messages.
type StatusReport struct {
	MoreMessagesToSend     bool
	MessageReference       uint8
	Recipient              Address
	ServiceCentreTimeStamp time.Time
	DischargeTime          time.Time
	Status                 Status
}

// StatusReport returns the SMS-STATUS-REPORT on the short message of the SMS-SUBMIT, which the SC received at
// timeStamp, with the status that the SC concluded at discharge.
func (s Submit) StatusReport(timeStamp, discharge time.Time, status Status) StatusReport {
	return StatusReport{
		MessageReference:       s.MessageReference,
		Recipient:              s.Destination,
		ServiceCentreTimeStamp: timeStamp,
		DischargeTime:          discharge,
		Status:                 status,
	}
}

func (r StatusReport) Encode() ([]byte, error) {
	ra, err := r.Recipient.Encode()
	if err != nil {
		return nil, fmt.Errorf("sms-status-report: recipient %w", err)
	}

	scts, err := encodeTimeStamp(r.ServiceCentreTimeStamp)
	if err != nil {
		return nil, fmt.Errorf("sms-status-report: %w", err)
	}

	dt, err := encodeTimeStamp(r.DischargeTime)
	if err != nil {
		return nil, fmt.Errorf("sms-status-report: discharge %w", err)
	}

	first := byte(mtiStatusReport)
	if !r.MoreMessagesToSend {
		first |= 0x04
	}

	out := make([]byte, 0, 2+len(ra)+len(scts)+len(dt)+1)
	out = append(out, first, r.MessageReference)
	out = append(out, ra...)
	out = append(out, scts...)
	out = append(out, dt...)

	return append(out, byte(r.Status)), nil
}

// SetMoreMessagesToSend sets TP-MMS on an encoded SMS-STATUS-REPORT, which the SC decides only when it sends the
// report.
func SetMoreMessagesToSend(report []byte, more bool) []byte {
	out := append([]byte(nil), report...)
	if len(out) == 0 {
		return out
	}

	if more {
		out[0] &^= 0x04
	} else {
		out[0] |= 0x04
	}

	return out
}

// DecodeStatusReport decodes the mandatory parameters of an SMS-STATUS-REPORT, and ignores any optional ones.
func DecodeStatusReport(b []byte) (StatusReport, error) {
	if len(b) < 2 {
		return StatusReport{}, fmt.Errorf("sms-status-report: %w", errTruncated)
	}

	first := b[0]
	if first&0x3 != mtiStatusReport {
		return StatusReport{}, fmt.Errorf("sms-status-report: message type indicator %d: %w", first&0x3, ErrUnsupportedMessageType)
	}

	r := StatusReport{
		MoreMessagesToSend: first&0x04 == 0,
		MessageReference:   b[1],
	}

	ra, n, err := decodeAddress(b[2:])
	if err != nil {
		return StatusReport{}, fmt.Errorf("sms-status-report: recipient %w", err)
	}

	r.Recipient = ra
	rest := b[2+n:]

	if len(rest) < 7+7+1 {
		return StatusReport{}, fmt.Errorf("sms-status-report: %w", errTruncated)
	}

	if r.ServiceCentreTimeStamp, err = decodeTimeStamp(rest[:7]); err != nil {
		return StatusReport{}, fmt.Errorf("sms-status-report: %w", err)
	}

	if r.DischargeTime, err = decodeTimeStamp(rest[7:14]); err != nil {
		return StatusReport{}, fmt.Errorf("sms-status-report: discharge %w", err)
	}

	r.Status = Status(rest[14])

	return r, nil
}
