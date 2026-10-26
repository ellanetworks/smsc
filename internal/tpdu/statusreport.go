package tpdu

import (
	"fmt"
	"time"
)

// mtiStatusReport is the TP-MTI of an SMS-STATUS-REPORT, from the SC to the MS. The same value is an SMS-COMMAND from
// the MS to the SC.
const mtiStatusReport = 0x2

// maxStatusReportOctets is the longest SMS-STATUS-REPORT: 143 octets of TP-UD with a TP-RA of 2 octets and only TP-UDL
// after TP-PI (TS 23.040 clause 9.2.2.3).
const maxStatusReportOctets = 164

const (
	parameterPID = 0x01
	parameterDCS = 0x02
	parameterUDL = 0x04
	parameterExt = 0x80
)

// sourceOriginalSender is the UDH Source Indicator of the information elements that the original sender created (TS
// 23.040 clause 9.2.3.24.6).
const sourceOriginalSender = 0x01

// dcs8Bit is the TP-DCS of a TP-UD of 8-bit data, without a message class.
const dcs8Bit = 0x04

var ErrStatusReportTooLong = fmt.Errorf("sms-status-report exceeds %d octets", maxStatusReportOctets)

// StatusReport is an SMS-STATUS-REPORT (TS 23.040 clause 9.2.2.3) on a short message that an SMS-SUBMIT submitted:
// TP-SRQ is always 0, and the SC sets no TP-LP, since it neither forwards nor spawns messages.
type StatusReport struct {
	MoreMessagesToSend     bool
	MessageReference       uint8
	Recipient              Address
	ServiceCentreTimeStamp time.Time
	DischargeTime          time.Time
	Status                 Status
	// The optional parameters, which TP-PI indicates.
	ProtocolIdentifier *byte
	DataCodingScheme   *byte
	UserData           *UserData
}

// UserData is a TP-UD, with its TP-UDHI and TP-UDL.
type UserData struct {
	Header bool
	Length uint8
	Data   []byte
}

// StatusReport returns the SMS-STATUS-REPORT on the short message of the SMS-SUBMIT, which the SC received at
// timeStamp, with the status that the SC concluded, or found on its last transfer attempt, at discharge. When the
// SMS-SUBMIT asks for it, the report carries its user data header, after a UDH Source Indicator, without the
// information elements that do not fit.
func (s Submit) StatusReport(timeStamp, discharge time.Time, status Status) StatusReport {
	r := StatusReport{
		MessageReference:       s.MessageReference,
		Recipient:              s.Destination,
		ServiceCentreTimeStamp: timeStamp,
		DischargeTime:          discharge,
		Status:                 status,
	}

	if !s.StatusReports().IncludesUserDataHeader() {
		return r
	}

	header, err := userDataHeader(s.UserDataHeader, s.UserData)
	if err != nil || header == nil {
		return r
	}

	ies, ok := informationElements(header[1:])
	if !ok {
		return r
	}

	ra, err := s.Destination.Encode()
	if err != nil {
		return r
	}

	// The room left for TP-UD after the mandatory parameters and TP-PI, TP-PID, TP-DCS and TP-UDL.
	room := maxStatusReportOctets - (2 + len(ra) + 7 + 7 + 1 + 4)

	source := informationElement{id: ieiUDHSourceIndicator, data: []byte{sourceOriginalSender}}.encode()
	ud := append([]byte{0}, source...)
	copied := 0

	for _, ie := range ies {
		encoded := ie.encode()
		if len(ud)+len(encoded) > room {
			break
		}

		ud = append(ud, encoded...)
		copied++
	}

	if copied == 0 {
		return r
	}

	ud[0] = byte(len(ud) - 1)
	pid, dcs := s.ProtocolIdentifier, byte(dcs8Bit)
	r.ProtocolIdentifier, r.DataCodingScheme = &pid, &dcs
	r.UserData = &UserData{Header: true, Length: uint8(len(ud)), Data: ud}

	return r
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

	if r.UserData != nil && r.UserData.Header {
		first |= 0x40
	}

	out := make([]byte, 0, maxStatusReportOctets)
	out = append(out, first, r.MessageReference)
	out = append(out, ra...)
	out = append(out, scts...)
	out = append(out, dt...)
	out = append(out, byte(r.Status))

	var pi byte

	if r.ProtocolIdentifier != nil {
		pi |= parameterPID
	}

	if r.DataCodingScheme != nil {
		pi |= parameterDCS
	}

	if r.UserData != nil {
		pi |= parameterUDL
	}

	if pi != 0 {
		out = append(out, pi)
	}

	if r.ProtocolIdentifier != nil {
		out = append(out, *r.ProtocolIdentifier)
	}

	if r.DataCodingScheme != nil {
		out = append(out, *r.DataCodingScheme)
	}

	if ud := r.UserData; ud != nil {
		// Without TP-DCS, the TP-UD is in the GSM 7 bit default alphabet (TS 23.040 clause 9.2.3.27).
		var dcs byte
		if r.DataCodingScheme != nil {
			dcs = *r.DataCodingScheme
		}

		if len(ud.Data) != userDataOctets(dcs, ud.Length) {
			return nil, fmt.Errorf("sms-status-report: user data is %d octets, length %d implies %d",
				len(ud.Data), ud.Length, userDataOctets(dcs, ud.Length))
		}

		out = append(out, ud.Length)
		out = append(out, ud.Data...)
	}

	if len(out) > maxStatusReportOctets {
		return nil, ErrStatusReportTooLong
	}

	return out, nil
}

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
	rest = rest[15:]

	if len(rest) == 0 {
		return r, nil
	}

	pi := rest[0]
	rest = rest[1:]

	// Further TP-PI octets indicate parameters that this SC does not know.
	for ext := pi; ext&parameterExt != 0; {
		if len(rest) == 0 {
			return StatusReport{}, fmt.Errorf("sms-status-report: parameter indicator %w", errTruncated)
		}

		ext, rest = rest[0], rest[1:]
	}

	next := func() (byte, error) {
		if len(rest) == 0 {
			return 0, fmt.Errorf("sms-status-report: %w", errTruncated)
		}

		v := rest[0]
		rest = rest[1:]

		return v, nil
	}

	if pi&parameterPID != 0 {
		pid, err := next()
		if err != nil {
			return StatusReport{}, err
		}

		r.ProtocolIdentifier = &pid
	}

	var dcs byte

	if pi&parameterDCS != 0 {
		if dcs, err = next(); err != nil {
			return StatusReport{}, err
		}

		r.DataCodingScheme = &dcs
	}

	if pi&parameterUDL != 0 {
		udl, err := next()
		if err != nil {
			return StatusReport{}, err
		}

		octets := userDataOctets(dcs, udl)
		if len(rest) < octets {
			return StatusReport{}, fmt.Errorf("sms-status-report: user data %w", errTruncated)
		}

		r.UserData = &UserData{Header: first&0x40 != 0, Length: udl, Data: append([]byte(nil), rest[:octets]...)}
	}

	return r, nil
}
