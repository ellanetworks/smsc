package esme

import (
	"errors"
	"strconv"
	"time"
	"unicode/utf16"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/numbering"
	"github.com/ellanetworks/smsc/internal/submission"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/smpp"
)

// SMPP 3.4 section 5.2.5 table 5-3
// TS 23.040 clause 9.1.2.5
const (
	tonUnknown       = 0
	tonInternational = 1
	tonNational      = 2
	tonAlphanumeric  = 5
	tonAbbreviated   = 6
)

// SMPP 3.4 section 5.2.6 table 5-4
const (
	npiUnknown = 0
	npiISDN    = 1
)

const (
	maxE164Digits = 15
	// TS 23.040 clause 9.1.2.5
	maxAddressDigits = 20
)

// SMPP 3.4 section 5.2.12
const (
	esmModeMask        = 0x03
	esmModeDatagram    = 0x01
	esmModeTransaction = 0x02
	esmTypeMask        = 0x3c
	esmTypeDeliveryAck = 0x08
	esmTypeUserAck     = 0x10
	esmUDHI            = 0x40
	esmReplyPath       = 0x80
)

// SMPP 3.4 section 5.2.17
const (
	regReceiptMask  = 0x03
	regReserved     = 0x03
	regSMEAckMask   = 0x0c
	regUserAck      = 0x08
	maxPriorityFlag = 3
)

// SMPP 3.4 section 5.2.19
const (
	dcDefault = 0x00
	dcIA5     = 0x01
	dcOctet   = 0x02
	dcLatin1  = 0x03
	dcOctetA  = 0x04
	dcUCS2    = 0x08
)

// TS 23.038 clause 4
const (
	dcsGSM7     = 0x00
	dcsOctet    = 0x04
	dcsUCS2     = 0x08
	dcsOctetSIM = 0xf6
)

// TS 23.040 clause 9.2.3.24.1
// TS 23.040 clause 9.2.3.24.4
// TS 23.040 clause 9.2.3.24.8
const (
	ieiConcat8  = 0x00
	ieiPorts16  = 0x05
	ieiConcat16 = 0x08
)

type rejection struct {
	status smpp.CommandStatus
	reason string
}

func (r *rejection) Error() string { return r.reason + " (" + r.status.String() + ")" }

func reject(status smpp.CommandStatus, reason string) *rejection {
	return &rejection{status: status, reason: reason}
}

type mapping struct {
	esme   db.ESME
	userID int64
	plan   numbering.Plan
	now    time.Time
}

// SMPP 3.4 section 4.4.1 table 4-10
func (m mapping) request(sm *smpp.Message) (submission.Request, *rejection) {
	r := submission.Request{Origin: db.OriginSMPP, SubmittedBy: m.userID}

	var rej *rejection

	if r.Recipient, r.MSISDN, rej = m.destination(sm.Destination); rej != nil {
		return r, rej
	}

	if r.Originator, rej = source(sm.Source); rej != nil {
		return r, rej
	}

	if r.ExpiresAt, rej = m.expiry(sm); rej != nil {
		return r, rej
	}

	udhi, rej := esmClass(sm.ESMClass)
	if rej != nil {
		return r, rej
	}

	if sm.ReplaceIfPresentFlag == 1 {
		return r, reject(smpp.StatusInvSubRep, "replace_if_present_flag 1 is not supported; use a replace type TP-PID")
	}

	if sm.SMDefaultMsgID != 0 && sm.SMDefaultMsgID != 255 {
		return r, reject(smpp.StatusInvDftMsgID, "sm_default_msg_id "+strconv.Itoa(int(sm.SMDefaultMsgID))+" is not a canned message")
	}

	r.SMPP = &db.NewSMPPMessage{
		ESMEID:               m.esme.ID,
		SourceNPI:            sm.Source.NPI,
		DataCoding:           sm.DataCoding,
		UserMessageReference: sm.Options.UserMessageReference,
	}

	if receipt := sm.RegisteredDelivery & regReceiptMask; receipt != regReserved {
		r.SMPP.Receipt = receipt
	}

	data, rej := m.userData(sm, udhi)
	if rej != nil {
		return r, rej
	}

	r.Data = data
	r.SMPP.SIMAnswer = sm.RegisteredDelivery&regUserAck != 0

	return r, nil
}

// SMPP 3.4 section 5.2.5
// SMPP 3.4 section 5.2.6
// SMPP 3.4 section 5.2.9
// TS 23.040 clause 9.1.2.5
func (m mapping) destination(a smpp.Address) (db.Address, string, *rejection) {
	switch a.TON {
	case tonUnknown, tonInternational, tonNational:
	default:
		return db.Address{}, "", reject(smpp.StatusInvDstTON, "dest_addr_ton "+strconv.Itoa(int(a.TON))+" cannot be routed through the HSS")
	}

	if a.NPI != npiUnknown && a.NPI != npiISDN {
		return db.Address{}, "", reject(smpp.StatusInvDstNPI, "dest_addr_npi "+strconv.Itoa(int(a.NPI))+" is not E.164")
	}

	if !isDigits(a.Addr, maxAddressDigits) {
		return db.Address{}, "", reject(smpp.StatusInvDstAdr, "destination_addr is not digits")
	}

	msisdn, err := m.plan.International(a.TON, a.Addr)
	if err != nil || !isE164(msisdn) {
		return db.Address{}, "", reject(smpp.StatusInvDstAdr, "destination_addr is not an E.164 number")
	}

	return db.Address{Digits: a.Addr, TypeOfNumber: a.TON, NumberingPlan: a.NPI}, msisdn, nil
}

// SMPP 3.4 section 5.2.5
// SMPP 3.4 section 5.2.6
// SMPP 3.4 section 5.2.8
// TS 23.040 clause 9.1.2.5
func source(a smpp.Address) (db.Address, *rejection) {
	if a.Addr == "" {
		return db.Address{}, reject(smpp.StatusInvSrcAdr, "source_addr is empty")
	}

	switch a.TON {
	case tonInternational:
		if a.NPI != npiUnknown && a.NPI != npiISDN {
			return db.Address{}, reject(smpp.StatusInvSrcNPI, "source_addr_npi "+strconv.Itoa(int(a.NPI))+" with an international number")
		}

		if !isE164(a.Addr) {
			return db.Address{}, reject(smpp.StatusInvSrcAdr, "source_addr is not an E.164 number")
		}

		return db.Address{Digits: a.Addr, TypeOfNumber: tonInternational, NumberingPlan: npiISDN}, nil
	case tonAlphanumeric:
		if !submission.IsOriginatorName(a.Addr) {
			return db.Address{}, reject(smpp.StatusInvSrcAdr, "source_addr is not a name of 1 to 11 letters and digits")
		}

		return db.Address{Digits: a.Addr, TypeOfNumber: tonAlphanumeric}, nil
	case tonUnknown, tonNational, 3, 4, tonAbbreviated:
		if !sameNumberingPlan(a.NPI) {
			return db.Address{}, reject(smpp.StatusInvSrcNPI, "source_addr_npi "+strconv.Itoa(int(a.NPI))+" has no TS 23.040 equivalent")
		}

		if !isDigits(a.Addr, maxAddressDigits) {
			return db.Address{}, reject(smpp.StatusInvSrcAdr, "source_addr is not 1 to 20 digits")
		}

		return db.Address{Digits: a.Addr, TypeOfNumber: a.TON, NumberingPlan: a.NPI}, nil
	default:
		return db.Address{}, reject(smpp.StatusInvSrcTON, "source_addr_ton "+strconv.Itoa(int(a.TON))+" is reserved")
	}
}

// TS 23.040 clause 9.1.2.5
func sameNumberingPlan(npi byte) bool {
	switch npi {
	case 0, 1, 3, 4, 8, 9, 10:
		return true
	default:
		return false
	}
}

func isDigits(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}

	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}

func isE164(s string) bool {
	return isDigits(s, maxE164Digits) && s[0] != '0'
}

// SMPP 3.4 section 5.2.15
// SMPP 3.4 section 5.2.16
// SMPP 3.4 section 7.1.1
func (m mapping) expiry(sm *smpp.Message) (time.Time, *rejection) {
	if sm.ScheduleDeliveryTime != "" {
		return time.Time{}, reject(smpp.StatusInvSched, "schedule_delivery_time is not supported")
	}

	if sm.ValidityPeriod == "" {
		return time.Time{}, nil
	}

	t, err := smpp.ParseTime(sm.ValidityPeriod, m.now)
	if err != nil {
		return time.Time{}, reject(smpp.StatusInvExpiry, err.Error())
	}

	at := t.At(m.now)
	if !at.After(m.now) {
		return time.Time{}, reject(smpp.StatusInvExpiry, "validity_period is not in the future")
	}

	return at, nil
}

// SMPP 3.4 section 5.2.12
// SMPP 3.4 section 3.3
func esmClass(c byte) (bool, *rejection) {
	switch c & esmModeMask {
	case esmModeDatagram, esmModeTransaction:
		return false, reject(smpp.StatusInvEsmClass, "esm_class asks for datagram or transaction mode")
	}

	switch c & esmTypeMask {
	case esmTypeDeliveryAck, esmTypeUserAck:
		return false, reject(smpp.StatusInvEsmClass, "esm_class carries an ESME acknowledgement")
	}

	if c&esmReplyPath != 0 {
		return false, reject(smpp.StatusInvEsmClass, "esm_class asks for a reply path")
	}

	return c&esmUDHI != 0, nil
}

func (m mapping) userData(sm *smpp.Message, udhi bool) (*submission.UserData, *rejection) {
	ud := sm.ShortMessage

	if sm.Options.MessagePayload != nil {
		if len(sm.ShortMessage) > 0 {
			return nil, reject(smpp.StatusOptParNotAllwd, "short_message and message_payload are both used")
		}

		ud = sm.Options.MessagePayload
	}

	var header []byte

	if udhi {
		if hasSAR(sm.Options) || hasPorts(sm.Options) {
			return nil, reject(smpp.StatusOptParNotAllwd, "sar_* or port optional parameters with UDHI set")
		}

		if len(ud) == 0 || int(ud[0])+1 > len(ud) {
			return nil, reject(smpp.StatusSubmitFail, "the user data header is longer than the user data")
		}

		header, ud = ud[:1+int(ud[0])], ud[1+int(ud[0]):]
	} else {
		var rej *rejection
		if header, rej = optionsHeader(sm.Options); rej != nil {
			return nil, rej
		}
	}

	d := &submission.UserData{PID: sm.ProtocolID, Header: header}

	dcs, text, rej := m.dataCoding(sm.DataCoding, ud)
	if rej != nil {
		return nil, rej
	}

	if dcs, rej = subunit(dcs, sm.Options.DestAddrSubunit); rej != nil {
		return nil, rej
	}

	if d.PID == tpdu.ProtocolUSIMDataDownload && dcs == dcsOctet {
		dcs = dcsOctetSIM
	}

	d.DCS = dcs

	switch {
	case text != nil:
		d.DCS, d.Septets, d.Octets = encodeText(*text, dcs)
	case tpdu.IsSeptetCoded(dcs):
		for _, s := range ud {
			if s > 0x7f {
				return nil, reject(smpp.StatusSubmitFail, "an octet of GSM 7 bit user data is above 0x7F")
			}
		}

		d.Septets = append([]byte{}, ud...)
	default:
		if tpdu.IsUCS2(dcs) && len(ud)%2 != 0 {
			return nil, reject(smpp.StatusSubmitFail, "UCS2 user data of an odd number of octets")
		}

		d.Octets = append([]byte{}, ud...)
	}

	return d, nil
}

// SMPP 3.4 section 5.2.19
// TS 23.038 clause 4
func (m mapping) dataCoding(dc byte, ud []byte) (byte, *string, *rejection) {
	switch {
	case dc == dcLatin1:
		text := latin1(ud)
		return dcsGSM7, &text, nil
	case dc == dcDefault:
		return dcsGSM7, nil, nil
	case dc == dcIA5:
		for _, c := range ud {
			if c > 0x7f {
				return 0, nil, reject(smpp.StatusSubmitFail, "an octet of IA5 user data is above 0x7F")
			}
		}

		text := string(ud)

		return dcsGSM7, &text, nil
	case dc == dcOctet, dc == dcOctetA:
		return dcsOctet, nil, nil
	case dc == dcUCS2:
		return dcsUCS2, nil, nil
	case dc >= 0xf0 && dc <= 0xf7:
		return dc, nil, nil
	default:
		return 0, nil, reject(smpp.StatusSubmitFail, "data_coding 0x"+strconv.FormatUint(uint64(dc), 16)+" is not supported")
	}
}

func latin1(b []byte) string {
	runes := make([]rune, len(b))
	for i, c := range b {
		runes[i] = rune(c)
	}

	return string(runes)
}

func encodeText(text string, dcs byte) (byte, []byte, []byte) {
	if septets, ok := tpdu.GSM7Septets(text); ok {
		return dcs, septets, nil
	}

	units := utf16.Encode([]rune(text))
	octets := make([]byte, 0, 2*len(units))

	for _, u := range units {
		octets = append(octets, byte(u>>8), byte(u))
	}

	return dcs&^0x0c | dcsUCS2, nil, octets
}

// SMPP 3.4 section 5.3.2.1
// SMPP 3.4 section 5.2.19
// SMPP 3.4 section 3.3
func subunit(dcs byte, v *byte) (byte, *rejection) {
	if v == nil || *v < 1 || *v > 4 {
		return dcs, nil
	}

	class := *v - 1

	switch dcs >> 6 {
	case 0, 1:
		if dcs&0x10 != 0 && dcs&0x03 != class {
			return 0, reject(smpp.StatusInvOptParamVal, "dest_addr_subunit names another class than data_coding")
		}

		return dcs | 0x10 | class, nil
	}

	if dcs>>4 == 0xf && dcs&0x03 == class {
		return dcs, nil
	}

	return 0, reject(smpp.StatusInvOptParamVal, "dest_addr_subunit names another class than data_coding, or one it cannot carry")
}

func hasSAR(o smpp.Options) bool {
	return o.SarMsgRefNum != nil || o.SarTotalSegments != nil || o.SarSegmentSeqnum != nil
}

func hasPorts(o smpp.Options) bool {
	return o.SourcePort != nil || o.DestinationPort != nil
}

// SMPP 3.4 section 5.3.2.22
// SMPP 3.4 section 5.3.2.23
// SMPP 3.4 section 5.3.2.24
// TS 23.040 clause 9.2.3.24.1
// TS 23.040 clause 9.2.3.24.8
// TS 23.040 clause 9.2.3.24.4
func optionsHeader(o smpp.Options) ([]byte, *rejection) {
	var ies []byte

	if o.SarMsgRefNum != nil && o.SarTotalSegments != nil && o.SarSegmentSeqnum != nil {
		ref, total, seq := *o.SarMsgRefNum, *o.SarTotalSegments, *o.SarSegmentSeqnum
		if total == 0 || seq == 0 || seq > total {
			return nil, reject(smpp.StatusInvOptParamVal, "sar_segment_seqnum or sar_total_segments out of range")
		}

		if ref <= 0xff {
			ies = append(ies, ieiConcat8, 3, byte(ref), total, seq)
		} else {
			ies = append(ies, ieiConcat16, 4, byte(ref>>8), byte(ref), total, seq)
		}
	}

	if hasPorts(o) {
		var src, dst uint16
		if o.SourcePort != nil {
			src = *o.SourcePort
		}

		if o.DestinationPort != nil {
			dst = *o.DestinationPort
		}

		ies = append(ies, ieiPorts16, 4, byte(dst>>8), byte(dst), byte(src>>8), byte(src))
	}

	if ies == nil {
		return nil, nil
	}

	return append([]byte{byte(len(ies))}, ies...), nil
}

func submissionStatus(err error) smpp.CommandStatus {
	switch {
	case errors.Is(err, submission.ErrUserDataTooLong):
		return smpp.StatusInvMsgLen
	case errors.Is(err, submission.ErrStore), errors.Is(err, submission.ErrEncode), errors.Is(err, submission.ErrEncodeText):
		return smpp.StatusSysErr
	default:
		return smpp.StatusSubmitFail
	}
}
