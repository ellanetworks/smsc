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

// The types of number of SMPP 3.4 section 5.2.5, Table 5-3, which are those of TS 23.040 clause 9.1.2.5.
const (
	tonUnknown       = 0
	tonInternational = 1
	tonNational      = 2
	tonAlphanumeric  = 5
	tonAbbreviated   = 6
)

// The numbering plans of SMPP 3.4 section 5.2.6, Table 5-4.
const (
	npiUnknown = 0
	npiISDN    = 1
)

const (
	maxE164Digits = 15
	// maxAddressDigits is the most digits of a TP address (TS 23.040 clause 9.1.2.5).
	maxAddressDigits = 20
)

// The fields of esm_class in submit_sm (SMPP 3.4 section 5.2.12).
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

// The fields of registered_delivery (SMPP 3.4 section 5.2.17).
const (
	regReceiptMask  = 0x03
	regReserved     = 0x03
	regSMEAckMask   = 0x0c
	regUserAck      = 0x08 // xxxx10xx and xxxx11xx ask for an SME Manual/User Acknowledgement
	maxPriorityFlag = 3
)

// The data_coding values of SMPP 3.4 section 5.2.19.
const (
	dcDefault = 0x00
	dcIA5     = 0x01
	dcOctet   = 0x02 // note b: a TDMA/CDMA value on a GSM 03.38 reserved codepoint
	dcLatin1  = 0x03
	dcOctetA  = 0x04 // note a: common to GSM
	dcUCS2    = 0x08
)

// The TP-DCS values that the mapping produces (TS 23.038 clause 4).
const (
	dcsGSM7     = 0x00
	dcsOctet    = 0x04
	dcsUCS2     = 0x08
	dcsOctetSIM = 0xf6 // group 1111, 8-bit data, class 2
)

// The information elements that sar_* and the ports become (TS 23.040 clauses 9.2.3.24.1, 9.2.3.24.4, 9.2.3.24.8).
const (
	ieiConcat8  = 0x00
	ieiPorts16  = 0x05
	ieiConcat16 = 0x08
)

// rejection is a submit_sm that the SMSC refuses: the command_status of its submit_sm_resp, and why, for the log.
type rejection struct {
	status smpp.CommandStatus
	reason string
}

func (r *rejection) Error() string { return r.reason + " (" + r.status.String() + ")" }

func reject(status smpp.CommandStatus, reason string) *rejection {
	return &rejection{status: status, reason: reason}
}

// mapping is what the SMSC needs of a submit_sm and of the ESME that sent it.
type mapping struct {
	esme   db.ESME
	userID int64
	plan   numbering.Plan
	now    time.Time
}

// request maps a submit_sm to a submission request (SMPP 3.4 section 4.4.1, Table 4-10), or returns why it is
// refused. The submission still checks the user data against TS 23.040 (submissionStatus).
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

	// replace_if_present_flag 1 asks to replace on source, destination and service_type (SMPP 3.4 section 5.2.18),
	// which the SC has no function for: it replaces on the TS 23.040 replace types of TP-PID only. 2 to 255 are
	// reserved, and reserved values take the default, 0 (SMPP 3.4 section 3.3).
	if sm.ReplaceIfPresentFlag == 1 {
		return r, reject(smpp.StatusInvSubRep, "replace_if_present_flag 1 is not supported; use a replace type TP-PID")
	}

	// Values 1 to 254 name a canned message, which the SC has none of; 0 and the reserved 255 name none (SMPP 3.4
	// sections 5.2.20 and 3.3).
	if sm.SMDefaultMsgID != 0 && sm.SMDefaultMsgID != 255 {
		return r, reject(smpp.StatusInvDftMsgID, "sm_default_msg_id "+strconv.Itoa(int(sm.SMDefaultMsgID))+" is not a canned message")
	}

	// priority_flag above 3 is reserved and has no default, so it is ignored, as 0 to 3 are (SMPP 3.4 sections 5.2.14
	// and 3.3).

	r.SMPP = &db.NewSMPPMessage{
		ESMEID:               m.esme.ID,
		SourceNPI:            sm.Source.NPI,
		DataCoding:           sm.DataCoding,
		UserMessageReference: sm.Options.UserMessageReference,
	}

	// registered_delivery bits 1-0 = 11 is reserved and takes the default, no receipt (SMPP 3.4 sections 5.2.17 and
	// 3.3). Bits 3-2 = 10 or 11 ask for an SME Manual/User Acknowledgement, which the SIM's answer is (SMPP 3.4
	// section 2.11). Bits 3-2 = 01, an SME Delivery Acknowledgement, means the user read the message (SMPP 3.4 section
	// 2.11), which SGd does not report; bit 4, an intermediate notification, is beyond the scope of SMPP 3.4.
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

// destination returns the recipient as submitted and its MSISDN in international format (SMPP 3.4 sections 5.2.5,
// 5.2.6 and 5.2.9; TS 23.040 clause 9.1.2.5).
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

// source returns the originator (SMPP 3.4 sections 5.2.5, 5.2.6 and 5.2.8; TS 23.040 clause 9.1.2.5). SMPP 3.4 lets
// the SMSC default an empty source_addr; the SC has no default.
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
		// The numbering plan of an alphanumeric address is reserved, 0000 (TS 23.040 clause 9.1.2.5).
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

// sameNumberingPlan reports whether an SMPP numbering plan has the same value and meaning in TS 23.040 clause 9.1.2.5:
// unknown, ISDN, data, telex, national, private and ERMES. SMPP's 6, Land Mobile (E.212), is a Service Centre
// Specific plan in TS 23.040, and 14 and 18 are reserved there.
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

// isE164 reports whether s is an international number: 1 to 15 digits, without a leading 0.
func isE164(s string) bool {
	return isDigits(s, maxE164Digits) && s[0] != '0'
}

// expiry returns when the message expires, zero for the default validity period (SMPP 3.4 sections 5.2.15, 5.2.16
// and 7.1.1). The SC has no scheduled delivery.
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

// esmClass checks the esm_class of a submit_sm and returns its UDHI (SMPP 3.4 section 5.2.12). The SC is store and
// forward, so the default mode and store and forward are the same. The reserved message types take the default, a
// normal message (SMPP 3.4 section 3.3).
func esmClass(c byte) (bool, *rejection) {
	switch c & esmModeMask {
	case esmModeDatagram, esmModeTransaction:
		return false, reject(smpp.StatusInvEsmClass, "esm_class asks for datagram or transaction mode")
	}

	// ESME acknowledgements answer messages from an MS, which the SMSC does not route to ESMEs.
	switch c & esmTypeMask {
	case esmTypeDeliveryAck, esmTypeUserAck:
		return false, reject(smpp.StatusInvEsmClass, "esm_class carries an ESME acknowledgement")
	}

	// The reply path asks the SC to set TP-RP in the SMS-DELIVER (TS 23.040 clause 9.2.3.17), which offers the
	// recipient the SC for its reply. The SC has no reply procedure (TS 23.040 annex D), so it refuses it.
	if c&esmReplyPath != 0 {
		return false, reject(smpp.StatusInvEsmClass, "esm_class asks for a reply path")
	}

	return c&esmUDHI != 0, nil
}

// userData maps short_message or message_payload, data_coding, protocol_id, dest_addr_subunit and the sar_* and port
// optional parameters to the user data of one TPDU.
func (m mapping) userData(sm *smpp.Message, udhi bool) (*submission.UserData, *rejection) {
	ud := sm.ShortMessage

	if sm.Options.MessagePayload != nil {
		// short_message and message_payload must not both be used (SMPP 3.4 Table 4-10).
		if len(sm.ShortMessage) > 0 {
			return nil, reject(smpp.StatusOptParNotAllwd, "short_message and message_payload are both used")
		}

		ud = sm.Options.MessagePayload
	}

	var header []byte

	if udhi {
		// SMPP 3.4 section 5.2.12, notes: sar_* and the ports cannot be used with a UDH in the user data.
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

	// Convention, not SMPP: TS 23.040 clause 9.2.3.9 needs message class 2 for (U)SIM data download, and data_coding
	// 0x02 and 0x04 have no class, so the SMSC sends 8-bit data class 2, 11110110, which every ME takes (as
	// pySim-smpp2sim does).
	if d.PID == tpdu.ProtocolUSIMDataDownload && dcs == dcsOctet {
		dcs = dcsOctetSIM
	}

	d.DCS = dcs

	switch {
	case text != nil:
		d.DCS, d.Septets, d.Octets = encodeText(*text, dcs)
	case tpdu.IsSeptetCoded(dcs):
		// The octets are unpacked septets (SMPP 3.4 section 5.2.19 leaves their packing to the SMSC).
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

// dataCoding maps data_coding (SMPP 3.4 section 5.2.19) to a TP-DCS (TS 23.038 clause 4). For a character set that
// the SC converts, it returns the text, whose TP-DCS encodeText chooses. TS 23.038 clause 4 takes reserved codings as
// the GSM 7 bit default alphabet, so the values that SMPP puts on GSM reserved codepoints (note b) are not copied.
func (m mapping) dataCoding(dc byte, ud []byte) (byte, *string, *rejection) {
	switch {
	case dc == dcLatin1:
		text := latin1(ud)
		return dcsGSM7, &text, nil
	case dc == dcDefault:
		// The SMSC Default Alphabet is the GSM 7 bit default alphabet, one unpacked septet per octet.
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
		// GSM message class control. 0xF8 to 0xFF set bit 3, which is reserved (TS 23.038 clause 4).
		return dc, nil, nil
	default:
		// SMPP 3.4 has no status for an invalid data_coding; ESME_RINVDCS is SMPP 5.0's. MWI control, 0xC0 to 0xDF, is
		// not supported.
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

// encodeText codes text in the GSM 7 bit default alphabet, or in UCS2 when a character has no GSM 7 bit equivalent,
// as the API codes text. The class bits that dest_addr_subunit set are kept.
func encodeText(text string, dcs byte) (byte, []byte, []byte) {
	if septets, ok := tpdu.GSM7Septets(text); ok {
		return dcs, septets, nil
	}

	units := utf16.Encode([]rune(text))
	octets := make([]byte, 0, 2*len(units))

	for _, u := range units {
		octets = append(octets, byte(u>>8), byte(u))
	}

	// Group 00xx: bits 3-2 = 10, UCS2 (TS 23.038 clause 4).
	return dcs&^0x0c | dcsUCS2, nil, octets
}

// subunit applies dest_addr_subunit (SMPP 3.4 section 5.3.2.1), which SMPP 3.4 section 5.2.19 note e recommends for
// the message class. Its values name the TS 23.038 classes: 1 MS display, class 0; 2 mobile equipment, class 1; 3
// smart card, class 2, the (U)SIM; 4 external unit, class 3, the TE. 0 and the reserved values change nothing (SMPP
// 3.4 section 3.3).
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

// optionsHeader returns the user data header of the sar_* and port optional parameters, or nil for none. sar_* make a
// concatenation information element only when all three are present (SMPP 3.4 sections 5.3.2.22 to 5.3.2.24): with
// an 8-bit reference when it fits, else a 16-bit one (TS 23.040 clauses 9.2.3.24.1 and 9.2.3.24.8). The ports make the
// 16-bit application port addressing element (TS 23.040 clause 9.2.3.24.4); a missing port is 0.
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

// submissionStatus returns the command_status of a submission that failed.
func submissionStatus(err error) smpp.CommandStatus {
	switch {
	case errors.Is(err, submission.ErrUserDataTooLong):
		return smpp.StatusInvMsgLen
	case errors.Is(err, submission.ErrStore), errors.Is(err, submission.ErrEncode), errors.Is(err, submission.ErrEncodeText):
		return smpp.StatusSysErr
	default:
		// The TP-PID, TP-DCS or UDH rules of TS 23.040 clause 9.2.3.9.
		return smpp.StatusSubmitFail
	}
}
