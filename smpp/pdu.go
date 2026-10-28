// Package smpp is the SMPP 3.4 protocol layer of Ella SMSC and its clients. Section numbers refer to the SMPP
// Protocol Specification v3.4, Issue 1.2.
//
//   - The codec: the PDU header (§3.2), the PDUs an SMSC exchanges with an ESME in the transmitter, receiver and
//     transceiver sessions (§4), their optional parameters (§3.2.4, §5.3) and the time format (§7.1.1).
//   - Session, the engine that both sides share over a net.Conn: framing, sequence numbers (§5.1.4), responses
//     matched in any order, windows, the timers of §2.9 and §7.2, enquire_link, generic_nack (§3.3, §4.3), the
//     states of §2.2 with the requests that each allows (Table 2-1), and unbind.
//   - Server, the SMSC side: a listener, the bind through an Authenticate callback, and a registry of the bound
//     sessions by account.
//   - Client, the ESME side: one session bound and bound again with a backoff, and its state.
package smpp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// HeaderLength is the size of the PDU header: command_length, command_id, command_status and sequence_number,
	// four octets each (§3.2.1, Table 3-3).
	HeaderLength = 16
	// MaxLength is the largest command_length accepted, 68 KiB. It is Ella's limit, not the spec's: SMPP 3.4 sets no
	// maximum (§3.2.2), and a larger value is taken as a corrupt stream (§4.3). It admits a submit_sm with a full
	// message_payload of 65,535 octets (Table 4-10, §5.3.2.32): header 16 + mandatory parameters at most 348 + TLV
	// header 4 + 65,535 = 65,903 octets, which leaves 3,729 octets for its other optional parameters.
	MaxLength = 68 * 1024
	// MaxSequence is the largest sequence_number (§5.1.4: 0x00000001 to 0x7FFFFFFF).
	MaxSequence = 0x7FFFFFFF
)

// The maximum sizes of the C-Octet Strings, the NULL terminator included (§3.1, note iii; §4).
const (
	maxSystemID       = 16 // §4.1.1, Table 4-1
	maxPassword       = 9  // §4.1.1, Table 4-1
	maxSystemType     = 13 // §4.1.1, Table 4-1
	maxAddressRange   = 41 // §4.1.1, Table 4-1
	maxServiceType    = 6  // §4.4.1, Table 4-10
	maxAddr           = 21 // §4.4.1, Table 4-10: source_addr and destination_addr
	maxTime           = 17 // §4.4.1, Table 4-10: schedule_delivery_time and validity_period, "1 or 17"
	maxMessageID      = 65 // §4.4.2, Table 4-11, and §5.3.2.12
	maxUnusedField    = 1  // §4.6.1, Table 4-18, and §4.6.2, Table 4-19: fields set to NULL
	maxShortMessage   = 254
	smLengthForbidden = 255 // §5.2.21: "not allowed"
)

var (
	// ErrTruncated is returned when fewer octets are given than the header, or than its command_length, announce.
	ErrTruncated = errors.New("smpp: truncated PDU")
	// ErrInvalidField is returned by Encode for a field that does not fit its definition in §4 and §5.
	ErrInvalidField = errors.New("smpp: invalid field")
)

// HeaderError is a command_length outside HeaderLength…MaxLength. The PDU boundaries in the stream are lost: the
// receiver answers generic_nack ESME_RINVCMDLEN (§4.3) and closes the session.
type HeaderError struct {
	Length uint32
}

func (e *HeaderError) Error() string {
	return fmt.Sprintf("smpp: command_length %d outside %d…%d", e.Length, HeaderLength, MaxLength)
}

// Status returns ESME_RINVCMDLEN.
func (e *HeaderError) Status() CommandStatus {
	return StatusInvCmdLen
}

// BodyError is a PDU body that does not match its definition while the header is valid, so the next PDU in the
// stream can still be read. Status is the command_status that the response reports:
//
//   - ESME_RINVCMDID for a command that is not supported (§3.3);
//   - ESME_RINVCMDLEN when the body ends before its mandatory parameters do, or when a header-only PDU has a body
//     (§4.3);
//   - ESME_RINVMSGLEN for sm_length 255 or beyond the body (§5.2.21);
//   - ESME_RINVPARLEN for a C-Octet String longer than its maximum, or an optional parameter expected in the PDU with
//     the wrong size (§3.3, last rule);
//   - ESME_RINVOPTPARSTREAM when the octets after the mandatory parameters do not split into TLVs (§3.2.4.1);
//   - ESME_RINVOPTPARAMVAL for an optional parameter expected in the PDU whose value is not of its type.
type BodyError struct {
	Status CommandStatus
	Field  string
	Reason string
}

func (e *BodyError) Error() string {
	return fmt.Sprintf("smpp: %s: %s (%s)", e.Field, e.Reason, e.Status)
}

// Header is the PDU header (§3.2.1, Table 3-3).
type Header struct {
	Length   uint32
	ID       CommandID
	Status   CommandStatus
	Sequence uint32
}

// DecodeHeader decodes the header at the start of b. It checks command_length only: any command_id is returned, so
// that the caller can answer one it does not support with generic_nack ESME_RINVCMDID (§3.3).
func DecodeHeader(b []byte) (Header, error) {
	if len(b) < HeaderLength {
		return Header{}, ErrTruncated
	}

	h := Header{
		Length:   binary.BigEndian.Uint32(b[0:]),
		ID:       CommandID(binary.BigEndian.Uint32(b[4:])),
		Status:   CommandStatus(binary.BigEndian.Uint32(b[8:])),
		Sequence: binary.BigEndian.Uint32(b[12:]),
	}

	if h.Length < HeaderLength || h.Length > MaxLength {
		return h, &HeaderError{Length: h.Length}
	}

	return h, nil
}

func appendHeader(b []byte, h Header) []byte {
	b = binary.BigEndian.AppendUint32(b, h.Length)
	b = binary.BigEndian.AppendUint32(b, uint32(h.ID))
	b = binary.BigEndian.AppendUint32(b, uint32(h.Status))

	return binary.BigEndian.AppendUint32(b, h.Sequence)
}

// ReadPDU reads one PDU from r: the header, then the command_length - 16 octets of the body (§3.2.2). It returns
// io.EOF when r ends before the first octet, io.ErrUnexpectedEOF when it ends inside the PDU, and a *HeaderError for
// an invalid command_length, after which the stream position is lost.
func ReadPDU(r io.Reader) (Header, []byte, error) {
	var hb [HeaderLength]byte
	if _, err := io.ReadFull(r, hb[:]); err != nil {
		return Header{}, nil, err
	}

	h, err := DecodeHeader(hb[:])
	if err != nil {
		return h, nil, err
	}

	body := make([]byte, h.Length-HeaderLength)
	if _, err := io.ReadFull(r, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}

		return h, nil, err
	}

	return h, body, nil
}

// PDU is the body of a PDU that this package encodes and decodes.
type PDU interface {
	CommandID() CommandID
	appendBody(b []byte, status CommandStatus) ([]byte, error)
}

// Decode decodes the PDU b, whose length must be its command_length.
func Decode(b []byte) (Header, PDU, error) {
	h, err := DecodeHeader(b)
	if err != nil {
		return h, nil, err
	}

	if uint32(len(b)) < h.Length {
		return h, nil, ErrTruncated
	}

	if uint32(len(b)) > h.Length {
		return h, nil, fmt.Errorf("smpp: %d octets after the PDU", uint32(len(b))-h.Length)
	}

	p, err := DecodeBody(h, b[HeaderLength:])

	return h, p, err
}

// DecodeBody decodes the body of a PDU whose header is h. Every error is a *BodyError.
func DecodeBody(h Header, body []byte) (PDU, error) {
	d := &decoder{b: body}

	var p PDU

	switch h.ID {
	case CmdBindTransmitter, CmdBindReceiver, CmdBindTransceiver:
		p = d.bind(h.ID)
	case CmdBindTransmitterResp, CmdBindReceiverResp, CmdBindTransceiverResp:
		p = d.bindResp(h)
	case CmdSubmitSM:
		p = &SubmitSM{Message: d.message(h.ID)}
	case CmdDeliverSM:
		p = &DeliverSM{Message: d.message(h.ID)}
	case CmdSubmitSMResp:
		p = d.submitSMResp(h)
	case CmdDeliverSMResp:
		p = d.deliverSMResp()
	case CmdEnquireLink:
		p = d.headerOnly(&EnquireLink{})
	case CmdEnquireLinkResp:
		p = d.headerOnly(&EnquireLinkResp{})
	case CmdUnbind:
		p = d.headerOnly(&Unbind{})
	case CmdUnbindResp:
		p = d.headerOnly(&UnbindResp{})
	case CmdGenericNack:
		p = d.headerOnly(&GenericNack{})
	default:
		return nil, &BodyError{Status: StatusInvCmdID, Field: "command_id", Reason: h.ID.String() + " not supported"}
	}

	if d.err != nil {
		return nil, d.err
	}

	return p, nil
}

// Encode encodes p in a PDU with the given command_status and sequence_number. When status is not ESME_ROK, the body of
// bind_transmitter_resp, bind_receiver_resp and submit_sm_resp is left out (§4.1.2, §4.1.4, §4.4.2), and
// bind_transceiver_resp carries its system_id only (§4.1.6, Table 4-6). A request must have status ESME_ROK (§3.2.1,
// Table 3-3). sequence_number must be 1 to MaxSequence, or 0 in a generic_nack (§5.1.4, Table 4-9).
func Encode(p PDU, status CommandStatus, sequence uint32) ([]byte, error) {
	id := p.CommandID()

	switch {
	case !id.IsResponse() && status != StatusOK:
		return nil, fmt.Errorf("%w: request %s with command_status %s", ErrInvalidField, id, status)
	case sequence > MaxSequence, sequence == 0 && id != CmdGenericNack:
		return nil, fmt.Errorf("%w: sequence_number 0x%08x outside 0x00000001…0x%08x", ErrInvalidField, sequence,
			MaxSequence)
	}

	b := make([]byte, HeaderLength, 64)

	b, err := p.appendBody(b, status)
	if err != nil {
		return nil, err
	}

	if len(b) > MaxLength {
		return nil, fmt.Errorf("%w: PDU of %d octets", ErrInvalidField, len(b))
	}

	appendHeader(b[:0], Header{Length: uint32(len(b)), ID: id, Status: status, Sequence: sequence})

	return b, nil
}

// Address is a TON, an NPI and an address (§5.2.5, §5.2.6, §5.2.8, §5.2.9).
type Address struct {
	TON  byte
	NPI  byte
	Addr string
}

// Bind is bind_transmitter, bind_receiver or bind_transceiver, as ID says (§4.1.1, §4.1.3, §4.1.5; Tables 4-1, 4-3
// and 4-5).
type Bind struct {
	ID               CommandID
	SystemID         string
	Password         string
	SystemType       string
	InterfaceVersion byte
	AddrTON          byte
	AddrNPI          byte
	AddressRange     string
}

// CommandID returns b.ID.
func (b *Bind) CommandID() CommandID { return b.ID }

func (b *Bind) appendBody(dst []byte, _ CommandStatus) ([]byte, error) {
	if !b.ID.IsBind() {
		return nil, fmt.Errorf("%w: bind with command_id %s", ErrInvalidField, b.ID)
	}

	e := encoder{b: dst}
	e.cstring("system_id", b.SystemID, maxSystemID)
	e.cstring("password", b.Password, maxPassword)
	e.cstring("system_type", b.SystemType, maxSystemType)
	e.b = append(e.b, b.InterfaceVersion, b.AddrTON, b.AddrNPI)
	e.cstring("address_range", b.AddressRange, maxAddressRange)

	return e.b, e.err
}

// BindResp is bind_transmitter_resp, bind_receiver_resp or bind_transceiver_resp, as ID says (§4.1.2, §4.1.4,
// §4.1.6; Tables 4-2, 4-4 and 4-6). When command_status is not ESME_ROK, Encode leaves out the body of the first two
// (notes of Tables 4-2 and 4-4) and writes SystemID without Options in the third, whose Table 4-6 has no such note.
// Decode accepts both forms for all three.
type BindResp struct {
	ID       CommandID
	SystemID string
	Options  Options
}

// CommandID returns b.ID.
func (b *BindResp) CommandID() CommandID { return b.ID }

func (b *BindResp) appendBody(dst []byte, status CommandStatus) ([]byte, error) {
	if !b.ID.IsResponse() || !(b.ID &^ responseBit).IsBind() {
		return nil, fmt.Errorf("%w: bind response with command_id %s", ErrInvalidField, b.ID)
	}

	e := encoder{b: dst}

	switch {
	case status == StatusOK:
		e.cstring("system_id", b.SystemID, maxSystemID)
		e.options(b.Options)
	case b.ID == CmdBindTransceiverResp:
		e.cstring("system_id", b.SystemID, maxSystemID)
	}

	return e.b, e.err
}

// Message is the body of submit_sm and deliver_sm, which have the same format (§4.4.1, Table 4-10; §4.6.1, Table
// 4-18). sm_length is the length of ShortMessage.
type Message struct {
	ServiceType          string
	Source               Address
	Destination          Address
	ESMClass             byte
	ProtocolID           byte
	PriorityFlag         byte
	ScheduleDeliveryTime string
	ValidityPeriod       string
	RegisteredDelivery   byte
	ReplaceIfPresentFlag byte
	DataCoding           byte
	SMDefaultMsgID       byte
	ShortMessage         []byte
	Options              Options
}

// append encodes the message. schedule_delivery_time and validity_period are empty or 16 characters ("Fixed 1 or 17"
// with the NULL, Table 3-1 and Table 4-10), and empty in deliver_sm, where timeMax is 1. short_message and
// message_payload are not both used (Tables 4-10 and 4-18).
func (m *Message) append(dst []byte, timeMax int) ([]byte, error) {
	if len(m.ShortMessage) > maxShortMessage {
		return nil, fmt.Errorf("%w: short_message of %d octets", ErrInvalidField, len(m.ShortMessage))
	}

	if len(m.ShortMessage) > 0 && m.Options.MessagePayload != nil {
		return nil, fmt.Errorf("%w: short_message and message_payload both used", ErrInvalidField)
	}

	e := encoder{b: dst}
	e.cstring("service_type", m.ServiceType, maxServiceType)
	e.address("source_addr", m.Source)
	e.address("destination_addr", m.Destination)
	e.b = append(e.b, m.ESMClass, m.ProtocolID, m.PriorityFlag)
	e.time("schedule_delivery_time", m.ScheduleDeliveryTime, timeMax)
	e.time("validity_period", m.ValidityPeriod, timeMax)
	e.b = append(e.b, m.RegisteredDelivery, m.ReplaceIfPresentFlag, m.DataCoding, m.SMDefaultMsgID)

	e.b = append(e.b, byte(len(m.ShortMessage)))
	e.b = append(e.b, m.ShortMessage...)
	e.options(m.Options)

	return e.b, e.err
}

// SubmitSM is submit_sm (§4.4.1).
type SubmitSM struct {
	Message
}

// CommandID returns submit_sm.
func (*SubmitSM) CommandID() CommandID { return CmdSubmitSM }

func (s *SubmitSM) appendBody(dst []byte, _ CommandStatus) ([]byte, error) {
	return s.append(dst, maxTime)
}

// DeliverSM is deliver_sm (§4.6.1). Its schedule_delivery_time and validity_period must be empty (Table 4-18).
type DeliverSM struct {
	Message
}

// CommandID returns deliver_sm.
func (*DeliverSM) CommandID() CommandID { return CmdDeliverSM }

func (d *DeliverSM) appendBody(dst []byte, _ CommandStatus) ([]byte, error) {
	return d.append(dst, maxUnusedField)
}

// SubmitSMResp is submit_sm_resp (§4.4.2, Table 4-11). Its body is absent when command_status is not ESME_ROK.
type SubmitSMResp struct {
	MessageID string
}

// CommandID returns submit_sm_resp.
func (*SubmitSMResp) CommandID() CommandID { return CmdSubmitSMResp }

func (s *SubmitSMResp) appendBody(dst []byte, status CommandStatus) ([]byte, error) {
	if status != StatusOK {
		return dst, nil
	}

	e := encoder{b: dst}
	e.cstring("message_id", s.MessageID, maxMessageID)

	return e.b, e.err
}

// DeliverSMResp is deliver_sm_resp (§4.6.2, Table 4-19). Its message_id is unused and encoded NULL. When decoding,
// a message_id of up to 64 characters, or no body at all, is tolerated: the response carries nothing else, and
// optional parameters after message_id are ignored (§3.3).
type DeliverSMResp struct {
	MessageID string
}

// CommandID returns deliver_sm_resp.
func (*DeliverSMResp) CommandID() CommandID { return CmdDeliverSMResp }

func (d *DeliverSMResp) appendBody(dst []byte, _ CommandStatus) ([]byte, error) {
	e := encoder{b: dst}
	e.cstring("message_id", d.MessageID, maxUnusedField)

	return e.b, e.err
}

// EnquireLink is enquire_link, header only (§4.11.1, Table 4-28).
type EnquireLink struct{}

// CommandID returns enquire_link.
func (*EnquireLink) CommandID() CommandID { return CmdEnquireLink }

func (*EnquireLink) appendBody(dst []byte, _ CommandStatus) ([]byte, error) { return dst, nil }

// EnquireLinkResp is enquire_link_resp, header only (§4.11.2, Table 4-29).
type EnquireLinkResp struct{}

// CommandID returns enquire_link_resp.
func (*EnquireLinkResp) CommandID() CommandID { return CmdEnquireLinkResp }

func (*EnquireLinkResp) appendBody(dst []byte, _ CommandStatus) ([]byte, error) { return dst, nil }

// Unbind is unbind, header only (§4.2.1, Table 4-7).
type Unbind struct{}

// CommandID returns unbind.
func (*Unbind) CommandID() CommandID { return CmdUnbind }

func (*Unbind) appendBody(dst []byte, _ CommandStatus) ([]byte, error) { return dst, nil }

// UnbindResp is unbind_resp, header only (§4.2.2, Table 4-8).
type UnbindResp struct{}

// CommandID returns unbind_resp.
func (*UnbindResp) CommandID() CommandID { return CmdUnbindResp }

func (*UnbindResp) appendBody(dst []byte, _ CommandStatus) ([]byte, error) { return dst, nil }

// GenericNack is generic_nack, header only (§4.3.1, Table 4-9). Its sequence_number is that of the PDU it answers,
// or 0 when that PDU could not be decoded.
type GenericNack struct{}

// CommandID returns generic_nack.
func (*GenericNack) CommandID() CommandID { return CmdGenericNack }

func (*GenericNack) appendBody(dst []byte, _ CommandStatus) ([]byte, error) { return dst, nil }

// Tag is the tag of an optional parameter (§3.2.4.1, §5.3.1).
type Tag uint16

// The optional parameter tags of §5.3.2, Table 5-7.
const (
	TagDestAddrSubunit          Tag = 0x0005
	TagDestNetworkType          Tag = 0x0006
	TagDestBearerType           Tag = 0x0007
	TagDestTelematicsID         Tag = 0x0008
	TagSourceAddrSubunit        Tag = 0x000D
	TagSourceNetworkType        Tag = 0x000E
	TagSourceBearerType         Tag = 0x000F
	TagSourceTelematicsID       Tag = 0x0010
	TagQosTimeToLive            Tag = 0x0017
	TagPayloadType              Tag = 0x0019
	TagAdditionalStatusInfoText Tag = 0x001D
	TagReceiptedMessageID       Tag = 0x001E
	TagMsMsgWaitFacilities      Tag = 0x0030
	TagPrivacyIndicator         Tag = 0x0201
	TagSourceSubaddress         Tag = 0x0202
	TagDestSubaddress           Tag = 0x0203
	TagUserMessageReference     Tag = 0x0204
	TagUserResponseCode         Tag = 0x0205
	TagSourcePort               Tag = 0x020A
	TagDestinationPort          Tag = 0x020B
	TagSarMsgRefNum             Tag = 0x020C
	TagLanguageIndicator        Tag = 0x020D
	TagSarTotalSegments         Tag = 0x020E
	TagSarSegmentSeqnum         Tag = 0x020F
	TagSCInterfaceVersion       Tag = 0x0210
	TagCallbackNumPresInd       Tag = 0x0302
	TagCallbackNumAtag          Tag = 0x0303
	TagNumberOfMessages         Tag = 0x0304
	TagCallbackNum              Tag = 0x0381
	TagDpfResult                Tag = 0x0420
	TagSetDpf                   Tag = 0x0421
	TagMsAvailabilityStatus     Tag = 0x0422
	TagNetworkErrorCode         Tag = 0x0423
	TagMessagePayload           Tag = 0x0424
	TagDeliveryFailureReason    Tag = 0x0425
	TagMoreMessagesToSend       Tag = 0x0426
	TagMessageState             Tag = 0x0427
	TagUssdServiceOp            Tag = 0x0501
	TagDisplayTime              Tag = 0x1201
	TagSmsSignal                Tag = 0x1203
	TagMsValidity               Tag = 0x1204
	TagAlertOnMessageDelivery   Tag = 0x130C
	TagItsReplyType             Tag = 0x1380
	TagItsSessionInfo           Tag = 0x1383
)

var tagNames = map[Tag]string{
	TagDestAddrSubunit:          "dest_addr_subunit",
	TagDestNetworkType:          "dest_network_type",
	TagDestBearerType:           "dest_bearer_type",
	TagDestTelematicsID:         "dest_telematics_id",
	TagSourceAddrSubunit:        "source_addr_subunit",
	TagSourceNetworkType:        "source_network_type",
	TagSourceBearerType:         "source_bearer_type",
	TagSourceTelematicsID:       "source_telematics_id",
	TagQosTimeToLive:            "qos_time_to_live",
	TagPayloadType:              "payload_type",
	TagAdditionalStatusInfoText: "additional_status_info_text",
	TagReceiptedMessageID:       "receipted_message_id",
	TagMsMsgWaitFacilities:      "ms_msg_wait_facilities",
	TagPrivacyIndicator:         "privacy_indicator",
	TagSourceSubaddress:         "source_subaddress",
	TagDestSubaddress:           "dest_subaddress",
	TagUserMessageReference:     "user_message_reference",
	TagUserResponseCode:         "user_response_code",
	TagSourcePort:               "source_port",
	TagDestinationPort:          "destination_port",
	TagSarMsgRefNum:             "sar_msg_ref_num",
	TagLanguageIndicator:        "language_indicator",
	TagSarTotalSegments:         "sar_total_segments",
	TagSarSegmentSeqnum:         "sar_segment_seqnum",
	TagSCInterfaceVersion:       "sc_interface_version",
	TagCallbackNumPresInd:       "callback_num_pres_ind",
	TagCallbackNumAtag:          "callback_num_atag",
	TagNumberOfMessages:         "number_of_messages",
	TagCallbackNum:              "callback_num",
	TagDpfResult:                "dpf_result",
	TagSetDpf:                   "set_dpf",
	TagMsAvailabilityStatus:     "ms_availability_status",
	TagNetworkErrorCode:         "network_error_code",
	TagMessagePayload:           "message_payload",
	TagDeliveryFailureReason:    "delivery_failure_reason",
	TagMoreMessagesToSend:       "more_messages_to_send",
	TagMessageState:             "message_state",
	TagUssdServiceOp:            "ussd_service_op",
	TagDisplayTime:              "display_time",
	TagSmsSignal:                "sms_signal",
	TagMsValidity:               "ms_validity",
	TagAlertOnMessageDelivery:   "alert_on_message_delivery",
	TagItsReplyType:             "its_reply_type",
	TagItsSessionInfo:           "its_session_info",
}

// String returns the name of the tag in Table 5-7, or "tag(0x…)".
func (t Tag) String() string {
	if name, ok := tagNames[t]; ok {
		return name
	}

	return fmt.Sprintf("tag(0x%04x)", uint16(t))
}

// TLV is an optional parameter (§3.2.4.1, Table 3-4).
type TLV struct {
	Tag   Tag
	Value []byte
}

// MessageState is a message state (§5.2.28, Table 5-6), carried by the message_state optional parameter (§5.3.2.35).
type MessageState byte

// The message states of Table 5-6.
const (
	StateEnroute       MessageState = 1
	StateDelivered     MessageState = 2
	StateExpired       MessageState = 3
	StateDeleted       MessageState = 4
	StateUndeliverable MessageState = 5
	StateAccepted      MessageState = 6
	StateUnknown       MessageState = 7
	StateRejected      MessageState = 8
)

var messageStateNames = map[MessageState]string{
	StateEnroute:       "ENROUTE",
	StateDelivered:     "DELIVERED",
	StateExpired:       "EXPIRED",
	StateDeleted:       "DELETED",
	StateUndeliverable: "UNDELIVERABLE",
	StateAccepted:      "ACCEPTED",
	StateUnknown:       "UNKNOWN",
	StateRejected:      "REJECTED",
}

// String returns the name of the state in Table 5-6, or "reserved(…)".
func (s MessageState) String() string {
	if name, ok := messageStateNames[s]; ok {
		return name
	}

	return fmt.Sprintf("reserved(%d)", byte(s))
}

// NetworkErrorCode is the value of network_error_code (§5.3.2.31): a network type (1 ANSI-136, 2 IS-95, 3 GSM) and an
// error code of that network.
type NetworkErrorCode struct {
	NetworkType byte
	ErrorCode   uint16
}

// Options are the optional parameters of a PDU (§3.2.4). Those that Ella uses are typed fields, nil when absent, and
// are decoded and checked only in a PDU whose table in §4 lists them (expectedOptions). Every other one, unknown or
// not expected in that PDU (§3.3), is kept raw in Other, in the order received. A later occurrence of a typed
// parameter replaces an earlier one. Encode writes the typed ones in the order of Table 5-7, then Other.
type Options struct {
	DestAddrSubunit      *byte             // 0x0005, §5.3.2.1
	ReceiptedMessageID   *string           // 0x001E, §5.3.2.12, C-Octet String of 1 to 65 octets
	UserMessageReference *uint16           // 0x0204, §5.3.2.17
	SourcePort           *uint16           // 0x020A, §5.3.2.20
	DestinationPort      *uint16           // 0x020B, §5.3.2.21
	SarMsgRefNum         *uint16           // 0x020C, §5.3.2.22
	SarTotalSegments     *byte             // 0x020E, §5.3.2.23
	SarSegmentSeqnum     *byte             // 0x020F, §5.3.2.24
	SCInterfaceVersion   *byte             // 0x0210, §5.3.2.25
	NetworkErrorCode     *NetworkErrorCode // 0x0423, §5.3.2.31
	MessagePayload       []byte            // 0x0424, §5.3.2.32, non-nil when present
	MessageState         *MessageState     // 0x0427, §5.3.2.35
	Other                []TLV
}

// decoder reads the parameters of a body in order. The first error is kept and every later read returns zero values.
type decoder struct {
	b   []byte
	err error
}

func (d *decoder) fail(status CommandStatus, field, reason string) {
	if d.err == nil {
		d.err = &BodyError{Status: status, Field: field, Reason: reason}
	}
}

func (d *decoder) byte(field string) byte {
	if d.err != nil {
		return 0
	}

	if len(d.b) == 0 {
		d.fail(StatusInvCmdLen, field, "body ends before the parameter")

		return 0
	}

	v := d.b[0]
	d.b = d.b[1:]

	return v
}

// cstring reads a C-Octet String of at most maxLen octets, the NULL terminator included (§3.1).
func (d *decoder) cstring(field string, maxLen int) string {
	if d.err != nil {
		return ""
	}

	i := bytes.IndexByte(d.b, 0)

	switch {
	case i >= maxLen || (i < 0 && len(d.b) >= maxLen):
		d.fail(StatusInvParLen, field, fmt.Sprintf("longer than %d octets with the NULL terminator", maxLen))

		return ""
	case i < 0:
		d.fail(StatusInvCmdLen, field, "body ends before the NULL terminator")

		return ""
	}

	s := string(d.b[:i])
	d.b = d.b[i+1:]

	return s
}

func (d *decoder) address(field string) Address {
	ton := d.byte(field + "_ton")
	npi := d.byte(field + "_npi")

	return Address{TON: ton, NPI: npi, Addr: d.cstring(field, maxAddr)}
}

// headerOnly returns p, a header-only PDU, after checking that the body is empty: a longer command_length is invalid
// (§4.3).
func (d *decoder) headerOnly(p PDU) PDU {
	if len(d.b) > 0 {
		d.fail(StatusInvCmdLen, "body", fmt.Sprintf("%d octets in a header-only PDU", len(d.b)))
	}

	return p
}

// bind decodes a bind request. Table 4-1, 4-3 and 4-5 list no optional parameters: any that follow address_range are
// ignored (§3.3).
func (d *decoder) bind(id CommandID) *Bind {
	b := &Bind{
		ID:               id,
		SystemID:         d.cstring("system_id", maxSystemID),
		Password:         d.cstring("password", maxPassword),
		SystemType:       d.cstring("system_type", maxSystemType),
		InterfaceVersion: d.byte("interface_version"),
		AddrTON:          d.byte("addr_ton"),
		AddrNPI:          d.byte("addr_npi"),
		AddressRange:     d.cstring("address_range", maxAddressRange),
	}
	d.options(id)

	return b
}

func (d *decoder) bindResp(h Header) *BindResp {
	r := &BindResp{ID: h.ID}
	if len(d.b) == 0 && h.Status != StatusOK {
		return r
	}

	r.SystemID = d.cstring("system_id", maxSystemID)
	r.Options = d.options(h.ID)

	return r
}

// message decodes submit_sm or deliver_sm, as id says. In deliver_sm, schedule_delivery_time and validity_period are
// read with the submit_sm maximum: §4.6.1 gives both PDUs the same format.
func (d *decoder) message(id CommandID) Message {
	m := Message{
		ServiceType:          d.cstring("service_type", maxServiceType),
		Source:               d.address("source_addr"),
		Destination:          d.address("destination_addr"),
		ESMClass:             d.byte("esm_class"),
		ProtocolID:           d.byte("protocol_id"),
		PriorityFlag:         d.byte("priority_flag"),
		ScheduleDeliveryTime: d.cstring("schedule_delivery_time", maxTime),
		ValidityPeriod:       d.cstring("validity_period", maxTime),
		RegisteredDelivery:   d.byte("registered_delivery"),
		ReplaceIfPresentFlag: d.byte("replace_if_present_flag"),
		DataCoding:           d.byte("data_coding"),
		SMDefaultMsgID:       d.byte("sm_default_msg_id"),
	}

	smLength := int(d.byte("sm_length"))

	switch {
	case d.err != nil:
		return m
	case smLength == smLengthForbidden:
		d.fail(StatusInvMsgLen, "sm_length", "255 is not allowed")

		return m
	case smLength > len(d.b):
		d.fail(StatusInvMsgLen, "sm_length", fmt.Sprintf("%d octets announced, %d left", smLength, len(d.b)))

		return m
	}

	m.ShortMessage = bytes.Clone(d.b[:smLength])
	if m.ShortMessage == nil {
		m.ShortMessage = []byte{}
	}

	d.b = d.b[smLength:]
	m.Options = d.options(id)

	return m
}

// submitSMResp decodes submit_sm_resp. Table 4-11 lists no optional parameters: any that follow message_id are
// ignored (§3.3).
func (d *decoder) submitSMResp(h Header) *SubmitSMResp {
	if len(d.b) == 0 && h.Status != StatusOK {
		return &SubmitSMResp{}
	}

	r := &SubmitSMResp{MessageID: d.cstring("message_id", maxMessageID)}
	d.options(h.ID)

	return r
}

// deliverSMResp decodes deliver_sm_resp. Table 4-19 lists no optional parameters: any that follow message_id are
// ignored (§3.3).
func (d *decoder) deliverSMResp() *DeliverSMResp {
	if len(d.b) == 0 {
		return &DeliverSMResp{}
	}

	r := &DeliverSMResp{MessageID: d.cstring("message_id", maxMessageID)}
	d.options(CmdDeliverSMResp)

	return r
}

// expectedOptions lists, for each PDU, the typed optional parameters of Options that its table in §4 lists. A PDU
// that is not here expects none of them.
var expectedOptions = map[CommandID]map[Tag]bool{
	CmdBindTransmitterResp: {TagSCInterfaceVersion: true}, // Table 4-2
	CmdBindReceiverResp:    {TagSCInterfaceVersion: true}, // Table 4-4
	CmdBindTransceiverResp: {TagSCInterfaceVersion: true}, // Table 4-6
	CmdSubmitSM: { // Table 4-10
		TagDestAddrSubunit: true, TagUserMessageReference: true, TagSourcePort: true, TagDestinationPort: true,
		TagSarMsgRefNum: true, TagSarTotalSegments: true, TagSarSegmentSeqnum: true, TagMessagePayload: true,
	},
	CmdDeliverSM: { // Table 4-18
		TagReceiptedMessageID: true, TagUserMessageReference: true, TagSourcePort: true, TagDestinationPort: true,
		TagSarMsgRefNum: true, TagSarTotalSegments: true, TagSarSegmentSeqnum: true, TagNetworkErrorCode: true,
		TagMessagePayload: true, TagMessageState: true,
	},
}

// options decodes the octets after the mandatory parameters of the PDU id as TLVs (§3.2.4). A typed parameter that
// id expects is decoded and checked. An unknown one, or one that id does not expect, is kept raw in Other and the next
// TLV is read (§3.3).
func (d *decoder) options(id CommandID) Options {
	var o Options

	for d.err == nil && len(d.b) > 0 {
		if len(d.b) < 4 {
			d.fail(StatusInvOptParStream, "optional parameters", fmt.Sprintf("%d octets left for a TLV", len(d.b)))

			break
		}

		tag := Tag(binary.BigEndian.Uint16(d.b))
		length := int(binary.BigEndian.Uint16(d.b[2:]))

		if 4+length > len(d.b) {
			d.fail(StatusInvOptParStream, tag.String(),
				fmt.Sprintf("length %d runs past the body (%d octets left)", length, len(d.b)-4))

			break
		}

		if v := d.b[4 : 4+length]; expectedOptions[id][tag] {
			d.setOption(&o, tag, v)
		} else {
			o.Other = append(o.Other, TLV{Tag: tag, Value: bytes.Clone(v)})
		}

		d.b = d.b[4+length:]
	}

	return o
}

func (d *decoder) setOption(o *Options, tag Tag, v []byte) {
	fixed := func(n int) bool {
		if len(v) != n {
			d.fail(StatusInvParLen, tag.String(), fmt.Sprintf("length %d, want %d", len(v), n))

			return false
		}

		return true
	}

	switch tag {
	case TagDestAddrSubunit:
		if fixed(1) {
			o.DestAddrSubunit = new(v[0])
		}
	case TagReceiptedMessageID:
		if len(v) < 1 || len(v) > maxMessageID {
			d.fail(StatusInvParLen, tag.String(), fmt.Sprintf("length %d outside 1…%d", len(v), maxMessageID))

			return
		}

		if i := bytes.IndexByte(v, 0); i != len(v)-1 {
			d.fail(StatusInvOptParamVal, tag.String(), "not a C-Octet String")

			return
		}

		o.ReceiptedMessageID = new(string(v[:len(v)-1]))
	case TagUserMessageReference:
		if fixed(2) {
			o.UserMessageReference = new(binary.BigEndian.Uint16(v))
		}
	case TagSourcePort:
		if fixed(2) {
			o.SourcePort = new(binary.BigEndian.Uint16(v))
		}
	case TagDestinationPort:
		if fixed(2) {
			o.DestinationPort = new(binary.BigEndian.Uint16(v))
		}
	case TagSarMsgRefNum:
		if fixed(2) {
			o.SarMsgRefNum = new(binary.BigEndian.Uint16(v))
		}
	case TagSarTotalSegments:
		if fixed(1) {
			o.SarTotalSegments = new(v[0])
		}
	case TagSarSegmentSeqnum:
		if fixed(1) {
			o.SarSegmentSeqnum = new(v[0])
		}
	case TagSCInterfaceVersion:
		if fixed(1) {
			o.SCInterfaceVersion = new(v[0])
		}
	case TagNetworkErrorCode:
		if fixed(3) {
			o.NetworkErrorCode = &NetworkErrorCode{NetworkType: v[0], ErrorCode: binary.BigEndian.Uint16(v[1:])}
		}
	case TagMessagePayload:
		o.MessagePayload = append([]byte{}, v...)
	case TagMessageState:
		if fixed(1) {
			o.MessageState = new(MessageState(v[0]))
		}
	default:
		o.Other = append(o.Other, TLV{Tag: tag, Value: bytes.Clone(v)})
	}
}

// encoder appends the parameters of a body. The first error is kept.
type encoder struct {
	b   []byte
	err error
}

// cstring appends s and its NULL terminator, at most maxLen octets in all (§3.1).
func (e *encoder) cstring(field, s string, maxLen int) {
	switch {
	case e.err != nil:
		return
	case len(s)+1 > maxLen:
		e.err = fmt.Errorf("%w: %s %q longer than %d octets with the NULL terminator", ErrInvalidField, field, s, maxLen)
	case bytes.IndexByte([]byte(s), 0) >= 0:
		e.err = fmt.Errorf("%w: %s contains a NULL", ErrInvalidField, field)
	default:
		e.b = append(append(e.b, s...), 0)
	}
}

// time appends a schedule_delivery_time or validity_period: empty, or the 16 characters of §7.1.1, with the NULL
// terminator at most maxLen octets.
func (e *encoder) time(field, s string, maxLen int) {
	if e.err == nil && s != "" && len(s) != timeLength {
		e.err = fmt.Errorf("%w: %s %q is %d characters, want 0 or %d", ErrInvalidField, field, s, len(s), timeLength)

		return
	}

	e.cstring(field, s, maxLen)
}

func (e *encoder) address(field string, a Address) {
	e.b = append(e.b, a.TON, a.NPI)
	e.cstring(field, a.Addr, maxAddr)
}

func (e *encoder) tlv(tag Tag, v []byte) {
	if e.err != nil {
		return
	}

	if len(v) > 0xFFFF {
		e.err = fmt.Errorf("%w: %s of %d octets", ErrInvalidField, tag, len(v))

		return
	}

	e.b = binary.BigEndian.AppendUint16(e.b, uint16(tag))
	e.b = binary.BigEndian.AppendUint16(e.b, uint16(len(v)))
	e.b = append(e.b, v...)
}

func (e *encoder) options(o Options) {
	u8 := func(tag Tag, v *byte) {
		if v != nil {
			e.tlv(tag, []byte{*v})
		}
	}
	u16 := func(tag Tag, v *uint16) {
		if v != nil {
			e.tlv(tag, binary.BigEndian.AppendUint16(nil, *v))
		}
	}

	u8(TagDestAddrSubunit, o.DestAddrSubunit)

	if o.ReceiptedMessageID != nil {
		id := *o.ReceiptedMessageID
		if len(id)+1 > maxMessageID || bytes.IndexByte([]byte(id), 0) >= 0 {
			e.err = fmt.Errorf("%w: receipted_message_id %q", ErrInvalidField, id)

			return
		}

		e.tlv(TagReceiptedMessageID, append([]byte(id), 0))
	}

	u16(TagUserMessageReference, o.UserMessageReference)
	u16(TagSourcePort, o.SourcePort)
	u16(TagDestinationPort, o.DestinationPort)
	u16(TagSarMsgRefNum, o.SarMsgRefNum)
	u8(TagSarTotalSegments, o.SarTotalSegments)
	u8(TagSarSegmentSeqnum, o.SarSegmentSeqnum)
	u8(TagSCInterfaceVersion, o.SCInterfaceVersion)

	if n := o.NetworkErrorCode; n != nil {
		e.tlv(TagNetworkErrorCode, binary.BigEndian.AppendUint16([]byte{n.NetworkType}, n.ErrorCode))
	}

	if o.MessagePayload != nil {
		e.tlv(TagMessagePayload, o.MessagePayload)
	}

	if o.MessageState != nil {
		e.tlv(TagMessageState, []byte{byte(*o.MessageState)})
	}

	for _, t := range o.Other {
		e.tlv(t.Tag, t.Value)
	}
}
