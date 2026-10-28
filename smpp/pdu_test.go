package smpp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// ascii returns the hex of the octets of s, for the text parts of the vectors below.
func ascii(s string) string {
	return hex.EncodeToString([]byte(s))
}

// The vectors are assembled by hand from the field tables of §4: header (Table 3-3) ‖ mandatory parameters in table
// order ‖ TLVs (Table 3-4), in the order Encode writes them.
var pduVectors = map[string]struct {
	hex    string
	status CommandStatus
	seq    uint32
	pdu    PDU
}{
	// §3.2.2: the example data stream of the specification, a bind_transmitter (Table 4-1).
	"bind_transmitter §3.2.2 example": {
		hex: "0000002f" + "00000002" + "00000000" + "00000001" +
			"534d50503354455354" + "00" + // system_id "SMPP3TEST"
			"7365637265743038" + "00" + // password "secret08"
			"5355424d495431" + "00" + // system_type "SUBMIT1"
			"00" + "01" + "01" + // interface_version, addr_ton, addr_npi
			"00", // address_range
		seq: 1,
		pdu: &Bind{
			ID: CmdBindTransmitter, SystemID: "SMPP3TEST", Password: "secret08", SystemType: "SUBMIT1",
			InterfaceVersion: 0x00, AddrTON: 1, AddrNPI: 1,
		},
	},
	// §4.1.5, Table 4-5.
	"bind_transceiver": {
		hex: "00000023" + "00000009" + "00000000" + "00000002" +
			"6f7461" + "00" + // system_id "ota"
			"736563726574" + "00" + // password "secret"
			"4f5441" + "00" + // system_type "OTA"
			"34" + "01" + "01" + // interface_version 3.4, addr_ton, addr_npi
			"00", // address_range
		seq: 2,
		pdu: &Bind{
			ID: CmdBindTransceiver, SystemID: "ota", Password: "secret", SystemType: "OTA",
			InterfaceVersion: 0x34, AddrTON: 1, AddrNPI: 1,
		},
	},
	// §4.1.3, Table 4-3: an empty password and system_type, and an address_range.
	"bind_receiver": {
		hex: "0000001f" + "00000001" + "00000000" + "00000003" +
			"6f7461" + "00" + "00" + "00" + // system_id "ota", password, system_type
			"34" + "00" + "00" +
			"5e31353535" + "00", // address_range "^1555"
		seq: 3,
		pdu: &Bind{ID: CmdBindReceiver, SystemID: "ota", InterfaceVersion: 0x34, AddressRange: "^1555"},
	},
	// §4.1.6, Table 4-6, with sc_interface_version (§5.3.2.25).
	"bind_transceiver_resp": {
		hex: "0000001e" + "80000009" + "00000000" + "00000002" +
			"456c6c61534d5343" + "00" + // system_id "EllaSMSC"
			"0210" + "0001" + "34", // sc_interface_version 0x34
		seq: 2,
		pdu: &BindResp{ID: CmdBindTransceiverResp, SystemID: "EllaSMSC", Options: Options{SCInterfaceVersion: new(byte(0x34))}},
	},
	// §4.1.4, Table 4-4, without optional parameters.
	"bind_receiver_resp": {
		hex: "00000019" + "80000001" + "00000000" + "00000003" +
			"456c6c61534d5343" + "00",
		seq: 3,
		pdu: &BindResp{ID: CmdBindReceiverResp, SystemID: "EllaSMSC"},
	},
	// §4.1.2, Table 4-2 note: no body when command_status is not ESME_ROK.
	"bind_transmitter_resp ESME_RBINDFAIL": {
		hex:    "00000010" + "80000002" + "0000000d" + "00000001",
		status: StatusBindFail,
		seq:    1,
		pdu:    &BindResp{ID: CmdBindTransmitterResp},
	},
	// §4.1.4, Table 4-4 note: no body when command_status is not ESME_ROK.
	"bind_receiver_resp ESME_RBINDFAIL": {
		hex:    "00000010" + "80000001" + "0000000d" + "00000003",
		status: StatusBindFail,
		seq:    3,
		pdu:    &BindResp{ID: CmdBindReceiverResp},
	},
	// §4.1.6, Table 4-6: no note, so system_id is sent when command_status is not ESME_ROK.
	"bind_transceiver_resp ESME_RBINDFAIL": {
		hex: "00000019" + "80000009" + "0000000d" + "00000002" +
			"456c6c61534d5343" + "00", // system_id "EllaSMSC"
		status: StatusBindFail,
		seq:    2,
		pdu:    &BindResp{ID: CmdBindTransceiverResp, SystemID: "EllaSMSC"},
	},
	// §4.4.1, Table 4-10: an OTA submit, esm_class UDHI, PID 0x7F, data_coding 0x02, relative validity_period and
	// user_message_reference (§5.3.2.17).
	"submit_sm": {
		hex: "00000057" + "00000004" + "00000000" + "00000003" +
			"00" + // service_type
			"01" + "01" + "3135353530303031303030" + "00" + // source "15550001000"
			"01" + "01" + "3135353531323330303032" + "00" + // destination "15551230002"
			"40" + "7f" + "00" + // esm_class, protocol_id, priority_flag
			"00" + // schedule_delivery_time
			"30303030303130303030303030303052" + "00" + // validity_period "000001000000000R"
			"01" + "00" + "02" + "00" + // registered_delivery, replace_if_present_flag, data_coding, sm_default_msg_id
			"0a" + "0270000008" + "0d00000001" + // sm_length 10, short_message
			"0204" + "0002" + "0007", // user_message_reference 7
		seq: 3,
		pdu: &SubmitSM{Message: Message{
			Source:             Address{TON: 1, NPI: 1, Addr: "15550001000"},
			Destination:        Address{TON: 1, NPI: 1, Addr: "15551230002"},
			ESMClass:           0x40,
			ProtocolID:         0x7f,
			ValidityPeriod:     "000001000000000R",
			RegisteredDelivery: 0x01,
			DataCoding:         0x02,
			ShortMessage:       []byte{0x02, 0x70, 0x00, 0x00, 0x08, 0x0d, 0x00, 0x00, 0x00, 0x01},
			Options:            Options{UserMessageReference: new(uint16(7))},
		}},
	},
	// §4.4.1, Table 4-10: no short_message, and the typed TLVs of §5.3.2.1, .20, .21, .22, .23, .24 and .32, then a
	// vendor-specific one (§5.3.1, 0x1400–0x3FFF) kept raw.
	"submit_sm with optional parameters": {
		hex: "0000005e" + "00000004" + "00000000" + "00000004" +
			"00" +
			"05" + "00" + "456c6c61" + "00" + // source alphanumeric "Ella"
			"01" + "01" + "3135353531323330303032" + "00" +
			"00" + "00" + "01" + // esm_class, protocol_id, priority_flag 1
			"00" + "00" + // schedule_delivery_time, validity_period
			"00" + "00" + "04" + "00" +
			"00" + // sm_length 0
			"0005" + "0001" + "03" + // dest_addr_subunit: Smart Card 1
			"020a" + "0002" + "0b84" + // source_port 2948
			"020b" + "0002" + "23f0" + // destination_port 9200
			"020c" + "0002" + "0102" + // sar_msg_ref_num 258
			"020e" + "0001" + "03" + // sar_total_segments 3
			"020f" + "0001" + "01" + // sar_segment_seqnum 1
			"0424" + "0003" + "010203" + // message_payload
			"1400" + "0002" + "abcd", // vendor-specific
		seq: 4,
		pdu: &SubmitSM{Message: Message{
			Source:       Address{TON: 5, NPI: 0, Addr: "Ella"},
			Destination:  Address{TON: 1, NPI: 1, Addr: "15551230002"},
			PriorityFlag: 1,
			DataCoding:   0x04,
			ShortMessage: []byte{},
			Options: Options{
				DestAddrSubunit:  new(byte(3)),
				SourcePort:       new(uint16(2948)),
				DestinationPort:  new(uint16(9200)),
				SarMsgRefNum:     new(uint16(258)),
				SarTotalSegments: new(byte(3)),
				SarSegmentSeqnum: new(byte(1)),
				MessagePayload:   []byte{1, 2, 3},
				Other:            []TLV{{Tag: 0x1400, Value: []byte{0xab, 0xcd}}},
			},
		}},
	},
	// §4.4.2, Table 4-11.
	"submit_sm_resp": {
		hex: "00000013" + "80000004" + "00000000" + "00000003" +
			"3432" + "00", // message_id "42"
		seq: 3,
		pdu: &SubmitSMResp{MessageID: "42"},
	},
	// §4.4.2, Table 4-11 note: no body when command_status is not ESME_ROK.
	"submit_sm_resp ESME_RSUBMITFAIL": {
		hex:    "00000010" + "80000004" + "00000045" + "00000003",
		status: StatusSubmitFail,
		seq:    3,
		pdu:    &SubmitSMResp{},
	},
	// §4.6.1, Table 4-18: a delivery receipt (esm_class 0x04, §5.2.12) with receipted_message_id (§5.3.2.12),
	// user_message_reference (§5.3.2.17), network_error_code (§5.3.2.31) and message_state (§5.3.2.35).
	"deliver_sm receipt": {
		hex: "00000062" + "00000005" + "00000000" + "00000008" +
			"00" +
			"01" + "01" + "3135353531323330303032" + "00" +
			"01" + "01" + "3135353530303031303030" + "00" +
			"04" + "00" + "00" +
			"00" + "00" + // schedule_delivery_time, validity_period: NULL
			"00" + "00" + "01" + "00" + // data_coding IA5
			"12" + ascii("id:42 stat:DELIVRD") +
			"001e" + "0003" + "3432" + "00" + // receipted_message_id "42"
			"0204" + "0002" + "0007" +
			"0423" + "0003" + "03" + "0022" + // network_error_code GSM 0x0022
			"0427" + "0001" + "02", // message_state DELIVERED
		seq: 8,
		pdu: &DeliverSM{Message: Message{
			Source:       Address{TON: 1, NPI: 1, Addr: "15551230002"},
			Destination:  Address{TON: 1, NPI: 1, Addr: "15550001000"},
			ESMClass:     0x04,
			DataCoding:   0x01,
			ShortMessage: []byte("id:42 stat:DELIVRD"),
			Options: Options{
				ReceiptedMessageID:   new("42"),
				UserMessageReference: new(uint16(7)),
				NetworkErrorCode:     &NetworkErrorCode{NetworkType: 3, ErrorCode: 0x22},
				MessageState:         new(StateDelivered),
			},
		}},
	},
	// §4.6.2, Table 4-19: message_id NULL.
	"deliver_sm_resp": {
		hex: "00000011" + "80000005" + "00000000" + "00000008" + "00",
		seq: 8,
		pdu: &DeliverSMResp{},
	},
	// §4.11.1, Table 4-28.
	"enquire_link": {
		hex: "00000010" + "00000015" + "00000000" + "00000005",
		seq: 5,
		pdu: &EnquireLink{},
	},
	// §4.11.2, Table 4-29.
	"enquire_link_resp": {
		hex: "00000010" + "80000015" + "00000000" + "00000005",
		seq: 5,
		pdu: &EnquireLinkResp{},
	},
	// §4.2.1, Table 4-7.
	"unbind": {
		hex: "00000010" + "00000006" + "00000000" + "00000006",
		seq: 6,
		pdu: &Unbind{},
	},
	// §4.2.2, Table 4-8.
	"unbind_resp": {
		hex: "00000010" + "80000006" + "00000000" + "00000006",
		seq: 6,
		pdu: &UnbindResp{},
	},
	// §4.3.1, Table 4-9: ESME_RINVCMDID.
	"generic_nack": {
		hex:    "00000010" + "80000000" + "00000003" + "00000007",
		status: StatusInvCmdID,
		seq:    7,
		pdu:    &GenericNack{},
	},
}

func TestDecode(t *testing.T) {
	for name, tt := range pduVectors {
		t.Run(name, func(t *testing.T) {
			b := mustHex(t, tt.hex)

			h, p, err := Decode(b)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			want := Header{Length: uint32(len(b)), ID: tt.pdu.CommandID(), Status: tt.status, Sequence: tt.seq}
			if h != want {
				t.Fatalf("header = %+v, want %+v", h, want)
			}

			if !reflect.DeepEqual(p, tt.pdu) {
				t.Fatalf("pdu = %#v\nwant  %#v", p, tt.pdu)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	for name, tt := range pduVectors {
		t.Run(name, func(t *testing.T) {
			got, err := Encode(tt.pdu, tt.status, tt.seq)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			if want := mustHex(t, tt.hex); !bytes.Equal(got, want) {
				t.Fatalf("Encode = %x\nwant     %x", got, want)
			}
		})
	}
}

func TestDecodeTLVOrderAndUnknownTags(t *testing.T) {
	// §3.3: an unrecognised TLV is ignored and the next one processed. TLVs may come in any order (§3.2.4).
	b := mustHex(t, "00000038"+"00000005"+"00000000"+"00000001"+
		"00"+"00"+"00"+"00"+"00"+"00"+"00"+ // service_type, source, destination
		"00"+"00"+"00"+"00"+"00"+"00"+"00"+"00"+"00"+ // esm_class … sm_default_msg_id
		"00"+ // sm_length 0
		"4000"+"0003"+"aabbcc"+ // reserved tag 0x4000
		"0427"+"0001"+"05"+ // message_state UNDELIVERABLE
		"0019"+"0001"+"00"+ // payload_type: defined, not typed
		"0204"+"0002"+"1234")

	_, p, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	o := p.(*DeliverSM).Options
	if o.MessageState == nil || *o.MessageState != StateUndeliverable {
		t.Fatalf("message_state = %v", o.MessageState)
	}

	if o.UserMessageReference == nil || *o.UserMessageReference != 0x1234 {
		t.Fatalf("user_message_reference = %v", o.UserMessageReference)
	}

	want := []TLV{{Tag: 0x4000, Value: []byte{0xaa, 0xbb, 0xcc}}, {Tag: TagPayloadType, Value: []byte{0}}}
	if !reflect.DeepEqual(o.Other, want) {
		t.Fatalf("Other = %+v, want %+v", o.Other, want)
	}
}

// bindHex returns a bind_transmitter with the given C-Octet String parameters, as hex without NULL terminators.
func bindHex(systemID, password, systemType, addressRange string) string {
	body := systemID + "00" + password + "00" + systemType + "00" + "34" + "00" + "00" + addressRange + "00"
	length := HeaderLength + len(body)/2

	return hex.EncodeToString([]byte{0, 0, byte(length >> 8), byte(length)}) + "00000002" + "00000000" + "00000001" +
		body
}

func TestCStringLimits(t *testing.T) {
	tests := map[string]struct {
		hex   string
		field string
		ok    bool
	}{
		"system_id 15 characters":     {bindHex(ascii(strings.Repeat("a", 15)), "", "", ""), "", true},
		"system_id 16 characters":     {bindHex(ascii(strings.Repeat("a", 16)), "", "", ""), "system_id", false},
		"password 8 characters":       {bindHex("", ascii("12345678"), "", ""), "", true},
		"password 9 characters":       {bindHex("", ascii("123456789"), "", ""), "password", false},
		"system_type 12 characters":   {bindHex("", "", ascii(strings.Repeat("t", 12)), ""), "", true},
		"system_type 13 characters":   {bindHex("", "", ascii(strings.Repeat("t", 13)), ""), "system_type", false},
		"address_range 40 characters": {bindHex("", "", "", ascii(strings.Repeat("1", 40))), "", true},
		"address_range 41 characters": {bindHex("", "", "", ascii(strings.Repeat("1", 41))), "address_range", false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := Decode(mustHex(t, tt.hex))
			if tt.ok {
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}

				return
			}

			var be *BodyError
			if !errors.As(err, &be) || be.Status != StatusInvParLen || be.Field != tt.field {
				t.Fatalf("err = %v, want ESME_RINVPARLEN on %s", err, tt.field)
			}
		})
	}
}

func TestEncodeCStringLimits(t *testing.T) {
	tests := map[string]PDU{
		"system_id 16 characters": &Bind{ID: CmdBindTransmitter, SystemID: strings.Repeat("a", 16)},
		"password 9 characters":   &Bind{ID: CmdBindTransmitter, Password: "123456789"},
		"NULL in system_id":       &Bind{ID: CmdBindTransmitter, SystemID: "a\x00b"},
		"source_addr 21":          &SubmitSM{Message: Message{Source: Address{Addr: strings.Repeat("1", 21)}}},
		"service_type 6":          &SubmitSM{Message: Message{ServiceType: "ABCDEF"}},
		"validity_period 17":      &SubmitSM{Message: Message{ValidityPeriod: strings.Repeat("0", 17)}},
		// Table 3-1, Table 4-10: schedule_delivery_time and validity_period are "Fixed 1 or 17".
		"validity_period 5":         &SubmitSM{Message: Message{ValidityPeriod: "0001R"}},
		"schedule_delivery_time 15": &SubmitSM{Message: Message{ScheduleDeliveryTime: "000001000000000"}},
		// Tables 4-10 and 4-18: short_message and message_payload "must not be used simultaneously".
		"submit_sm both payloads": &SubmitSM{Message: Message{
			ShortMessage: []byte{1}, Options: Options{MessagePayload: []byte{2}},
		}},
		"deliver_sm both payloads": &DeliverSM{Message: Message{
			ShortMessage: []byte{1}, Options: Options{MessagePayload: []byte{}},
		}},
		"deliver_sm validity":     &DeliverSM{Message: Message{ValidityPeriod: "000001000000000R"}},
		"message_id 65":           &SubmitSMResp{MessageID: strings.Repeat("9", 65)},
		"deliver_sm_resp id":      &DeliverSMResp{MessageID: "1"},
		"short_message 255":       &SubmitSM{Message: Message{ShortMessage: make([]byte, 255)}},
		"receipted_message_id 65": &DeliverSM{Message: Message{Options: Options{ReceiptedMessageID: new(strings.Repeat("9", 65))}}},
		"bind with submit_sm ID":  &Bind{ID: CmdSubmitSM},
		"bind_resp with bind ID":  &BindResp{ID: CmdBindTransceiver},
	}

	for name, p := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(p, StatusOK, 1); !errors.Is(err, ErrInvalidField) {
				t.Fatalf("err = %v, want ErrInvalidField", err)
			}
		})
	}

	if _, err := Encode(&SubmitSMResp{MessageID: strings.Repeat("9", 64)}, StatusOK, 1); err != nil {
		t.Fatalf("message_id of 64 characters: %v", err)
	}

	if _, err := Encode(&SubmitSM{Message: Message{
		ScheduleDeliveryTime: "261028120000004+", ValidityPeriod: "000001000000000R",
		ShortMessage: []byte{}, Options: Options{MessagePayload: []byte{1}},
	}}, StatusOK, 1); err != nil {
		t.Fatalf("16-character times and message_payload with an empty short_message: %v", err)
	}
}

func TestEncodeHeaderRules(t *testing.T) {
	tests := map[string]struct {
		pdu    PDU
		status CommandStatus
		seq    uint32
		ok     bool
	}{
		// §3.2.1, Table 3-3: command_status "must contain a NULL value in an SMPP request PDU".
		"submit_sm with a status":    {&SubmitSM{}, StatusSysErr, 1, false},
		"enquire_link with a status": {&EnquireLink{}, StatusInvCmdLen, 1, false},
		"bind with a status":         {&Bind{ID: CmdBindTransceiver}, StatusBindFail, 1, false},
		"response with a status":     {&SubmitSMResp{}, StatusSysErr, 1, true},
		// §5.1.4: sequence_number is 0x00000001 to 0x7FFFFFFF.
		"sequence 0":          {&EnquireLink{}, StatusOK, 0, false},
		"sequence 0 response": {&EnquireLinkResp{}, StatusOK, 0, false},
		"sequence 0x80000000": {&EnquireLink{}, StatusOK, 0x80000000, false},
		"sequence 0xffffffff": {&GenericNack{}, StatusInvCmdLen, 0xffffffff, false},
		"sequence 1":          {&EnquireLink{}, StatusOK, 1, true},
		"sequence 0x7fffffff": {&EnquireLink{}, StatusOK, 0x7fffffff, true},
		// Table 4-9: generic_nack has sequence_number NULL when the original PDU cannot be decoded.
		"generic_nack sequence 0": {&GenericNack{}, StatusInvCmdLen, 0, true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Encode(tt.pdu, tt.status, tt.seq)
			if tt.ok != (err == nil) || (err != nil && !errors.Is(err, ErrInvalidField)) {
				t.Fatalf("err = %v, want ok %v", err, tt.ok)
			}
		})
	}
}

func TestEncodeBindRespError(t *testing.T) {
	// §4.1.2, §4.1.4: no body for transmitter and receiver. §4.1.6, Table 4-6: system_id, without optional parameters,
	// for transceiver.
	opts := Options{SCInterfaceVersion: new(byte(0x34))}
	tests := map[string]struct {
		pdu *BindResp
		hex string
	}{
		"bind_transmitter_resp": {
			&BindResp{ID: CmdBindTransmitterResp, SystemID: "EllaSMSC", Options: opts},
			"00000010" + "80000002" + "0000000d" + "00000001",
		},
		"bind_receiver_resp": {
			&BindResp{ID: CmdBindReceiverResp, SystemID: "EllaSMSC", Options: opts},
			"00000010" + "80000001" + "0000000d" + "00000001",
		},
		"bind_transceiver_resp": {
			&BindResp{ID: CmdBindTransceiverResp, SystemID: "EllaSMSC", Options: opts},
			"00000019" + "80000009" + "0000000d" + "00000001" + "456c6c61534d5343" + "00",
		},
		"bind_transceiver_resp NULL system_id": {
			&BindResp{ID: CmdBindTransceiverResp},
			"00000011" + "80000009" + "0000000d" + "00000001" + "00",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := Encode(tt.pdu, StatusBindFail, 1)
			if want := mustHex(t, tt.hex); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Encode = %x, %v\nwant     %x", got, err, want)
			}
		})
	}
}

func TestDecodeHeaderCommandLength(t *testing.T) {
	tests := map[string]struct {
		length uint32
		ok     bool
	}{
		"15":     {15, false},
		"16":     {16, true},
		"65,536": {65536, true},
		"65,903": {65903, true},
		"69,632": {69632, true},
		"69,633": {69633, false},
		"70,000": {70000, false},
		"0":      {0, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			b := mustHex(t, hex.EncodeToString([]byte{
				byte(tt.length >> 24), byte(tt.length >> 16), byte(tt.length >> 8), byte(tt.length),
			})+"00000015"+"00000000"+"00000001")

			h, err := DecodeHeader(b)
			if tt.ok {
				if err != nil || h.Length != tt.length || h.ID != CmdEnquireLink || h.Sequence != 1 {
					t.Fatalf("DecodeHeader = %+v, %v", h, err)
				}

				return
			}

			var he *HeaderError
			if !errors.As(err, &he) || he.Length != tt.length || he.Status() != StatusInvCmdLen {
				t.Fatalf("err = %v, want HeaderError for %d", err, tt.length)
			}

			if _, _, err := ReadPDU(bytes.NewReader(b)); !errors.As(err, &he) {
				t.Fatalf("ReadPDU err = %v, want HeaderError", err)
			}
		})
	}
}

func TestDecodeHeaderOtherCommands(t *testing.T) {
	// §3.3, §4.3: a command that is not supported is recognised by its header, to be answered with generic_nack
	// ESME_RINVCMDID.
	tests := map[string]struct {
		id      CommandID
		name    string
		defined bool
	}{
		"query_sm":           {0x00000003, "query_sm", true},
		"replace_sm":         {0x00000007, "replace_sm", true},
		"cancel_sm":          {0x00000008, "cancel_sm", true},
		"outbind":            {0x0000000b, "outbind", true},
		"submit_multi":       {0x00000021, "submit_multi", true},
		"alert_notification": {0x00000102, "alert_notification", true},
		"data_sm":            {0x00000103, "data_sm", true},
		"data_sm_resp":       {0x80000103, "data_sm_resp", true},
		"reserved 0x0a":      {0x0000000a, "reserved(0x0000000a)", false},
		"reserved 0x16":      {0x00000016, "reserved(0x00000016)", false},
		"vendor":             {0x00010200, "vendor(0x00010200)", false},
		"reserved high":      {0xffffffff, "reserved(0xffffffff)", false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			id := hex.EncodeToString([]byte{byte(tt.id >> 24), byte(tt.id >> 16), byte(tt.id >> 8), byte(tt.id)})
			b := mustHex(t, "00000014"+id+"00000000"+"00000009"+"00000000")

			h, err := DecodeHeader(b)
			if err != nil || h.ID != tt.id || h.Sequence != 9 {
				t.Fatalf("DecodeHeader = %+v, %v", h, err)
			}

			if h.ID.String() != tt.name || h.ID.Defined() != tt.defined || h.ID.Supported() {
				t.Fatalf("%v: Defined %v, Supported %v", h.ID, h.ID.Defined(), h.ID.Supported())
			}

			var be *BodyError
			if _, _, err := Decode(b); !errors.As(err, &be) || be.Status != StatusInvCmdID {
				t.Fatalf("Decode err = %v, want ESME_RINVCMDID", err)
			}
		})
	}
}

func TestDecodeBodyErrors(t *testing.T) {
	// A header-only submit_sm prefix: service_type, source, destination, esm_class … sm_default_msg_id.
	smPrefix := "00" + "000000" + "000000" + "000000" + "00" + "00" + "00000000"

	tests := map[string]struct {
		hex    string
		status CommandStatus
		field  string
	}{
		// §4.3: a command_length too long for a header-only PDU (Tables 4-28, 4-8, 4-9).
		"enquire_link with a body": {
			"00000011" + "00000015" + "00000000" + "00000001" + "00", StatusInvCmdLen, "body",
		},
		"unbind_resp with a body": {
			"00000014" + "80000006" + "00000000" + "00000001" + "00000000", StatusInvCmdLen, "body",
		},
		"generic_nack with a body": {
			"00000011" + "80000000" + "00000003" + "00000001" + "00", StatusInvCmdLen, "body",
		},
		// §3.2.4: the octets after the mandatory parameters are TLVs, here too few for a TLV header (§3.2.4.1).
		"submit_sm_resp with 2 octets after message_id": {
			"00000014" + "80000004" + "00000000" + "00000001" + "3100" + "0000", StatusInvOptParStream,
			"optional parameters",
		},
		"bind_transmitter with 1 octet after address_range": {
			"00000018" + "00000002" + "00000000" + "00000001" + "00" + "00" + "00" + "34" + "0000" + "00" + "ff",
			StatusInvOptParStream, "optional parameters",
		},
		"deliver_sm_resp TLV length runs past the body": {
			"00000016" + "80000005" + "00000000" + "00000001" + "00" + "001d" + "0002" + "41",
			StatusInvOptParStream, "additional_status_info_text",
		},
		"sm_length 255": {
			"00000023" + "00000004" + "00000000" + "00000001" + smPrefix + "ff" + "0000", StatusInvMsgLen, "sm_length",
		},
		"sm_length beyond the body": {
			"00000023" + "00000004" + "00000000" + "00000001" + smPrefix + "05" + "0000", StatusInvMsgLen, "sm_length",
		},
		"body ends inside the mandatory parameters": {
			"00000016" + "00000004" + "00000000" + "00000001" + "00" + "01" + "01" + "313233", StatusInvCmdLen,
			"source_addr",
		},
		"body ends before interface_version": {
			"00000013" + "00000002" + "00000000" + "00000001" + "00" + "00" + "00", StatusInvCmdLen,
			"interface_version",
		},
		"TLV length runs past the body": {
			"00000027" + "00000004" + "00000000" + "00000001" + smPrefix + "00" + "0204" + "0003" + "0007",
			StatusInvOptParStream, "user_message_reference",
		},
		"TLV of 3 octets": {
			"00000024" + "00000004" + "00000000" + "00000001" + smPrefix + "00" + "020400", StatusInvOptParStream,
			"optional parameters",
		},
		"user_message_reference of 1 octet": {
			"00000026" + "00000004" + "00000000" + "00000001" + smPrefix + "00" + "0204" + "0001" + "07",
			StatusInvParLen, "user_message_reference",
		},
		"sc_interface_version of 2 octets": {
			"00000017" + "80000009" + "00000000" + "00000001" + "00" + "0210" + "0002" + "0034", StatusInvParLen,
			"sc_interface_version",
		},
		"receipted_message_id without NULL": {
			"00000027" + "00000005" + "00000000" + "00000001" + smPrefix + "00" + "001e" + "0002" + "3432",
			StatusInvOptParamVal, "receipted_message_id",
		},
		"receipted_message_id of 66 octets": {
			"00000067" + "00000005" + "00000000" + "00000001" + smPrefix + "00" + "001e" + "0042" +
				strings.Repeat("39", 65) + "00",
			StatusInvParLen, "receipted_message_id",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := Decode(mustHex(t, tt.hex))

			var be *BodyError
			if !errors.As(err, &be) || be.Status != tt.status || be.Field != tt.field {
				t.Fatalf("err = %v, want %v on %s", err, tt.status, tt.field)
			}
		})
	}
}

func TestDecodeOptionsAfterMandatory(t *testing.T) {
	// §3.2.4, §3.3: the octets after the mandatory parameters of any PDU are optional parameters. An unknown one, or a
	// known one that the PDU's table does not list, is ignored: kept raw in Other where the PDU has Options, dropped
	// where it has none. A malformed value of a parameter that is not expected is not checked.
	smPrefix := "00" + "000000" + "000000" + "000000" + "00" + "00" + "00000000"

	tests := map[string]struct {
		hex string
		pdu PDU
	}{
		// Table 4-1 lists no optional parameters.
		"bind_transmitter with sc_interface_version": {
			"0000001c" + "00000002" + "00000000" + "00000001" + "00" + "00" + "00" + "34" + "0000" + "00" +
				"0210" + "0001" + "34",
			&Bind{ID: CmdBindTransmitter, InterfaceVersion: 0x34},
		},
		// Table 4-11 lists no optional parameters.
		"submit_sm_resp with additional_status_info_text": {
			"00000019" + "80000004" + "00000000" + "00000001" + "3100" + "001d" + "0003" + "414200",
			&SubmitSMResp{MessageID: "1"},
		},
		// Table 4-19 lists none; SMPP 5.0 adds some. A network_error_code of 2 octets is not checked.
		"deliver_sm_resp with network_error_code and a vendor TLV": {
			"0000001b" + "80000005" + "00000000" + "00000001" + "00" + "0423" + "0002" + "0300" + "1400" + "0000",
			&DeliverSMResp{},
		},
		// Table 4-10 does not list message_state: its 2 octets are kept raw.
		"submit_sm with message_state of 2 octets": {
			"00000027" + "00000004" + "00000000" + "00000001" + smPrefix + "00" + "0427" + "0002" + "0102",
			&SubmitSM{Message: Message{
				ShortMessage: []byte{},
				Options:      Options{Other: []TLV{{Tag: TagMessageState, Value: []byte{1, 2}}}},
			}},
		},
		// Table 4-18 does not list dest_addr_subunit: its empty value is kept raw.
		"deliver_sm with dest_addr_subunit of 0 octets": {
			"00000025" + "00000005" + "00000000" + "00000001" + smPrefix + "00" + "0005" + "0000",
			&DeliverSM{Message: Message{
				ShortMessage: []byte{},
				Options:      Options{Other: []TLV{{Tag: TagDestAddrSubunit, Value: []byte{}}}},
			}},
		},
		// Table 4-6 does not list receipted_message_id: one without its NULL is kept raw.
		"bind_transceiver_resp with receipted_message_id without NULL": {
			"00000017" + "80000009" + "00000000" + "00000001" + "4100" + "001e" + "0001" + "41",
			&BindResp{ID: CmdBindTransceiverResp, SystemID: "A", Options: Options{
				Other: []TLV{{Tag: TagReceiptedMessageID, Value: []byte{0x41}}},
			}},
		},
		// Table 4-6: an error response with system_id and sc_interface_version.
		"bind_transceiver_resp error with sc_interface_version": {
			"00000017" + "80000009" + "0000000d" + "00000001" + "4100" + "0210" + "0001" + "34",
			&BindResp{ID: CmdBindTransceiverResp, SystemID: "A", Options: Options{SCInterfaceVersion: new(byte(0x34))}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, p, err := Decode(mustHex(t, tt.hex))
			if err != nil || !reflect.DeepEqual(p, tt.pdu) {
				t.Fatalf("Decode = %#v, %v; want %#v", p, err, tt.pdu)
			}
		})
	}
}

func TestFullMessagePayload(t *testing.T) {
	// Table 4-10, §5.3.2.32: "Up to 64K octets" of message_payload, here 65,535, with the other mandatory parameters at
	// their maxima and the sar_* and port TLVs. MaxLength admits it; ReadPDU and DecodeBody read it whole.
	want := &SubmitSM{Message: Message{
		ServiceType:          "CMT12",
		Source:               Address{TON: 1, NPI: 1, Addr: strings.Repeat("1", 20)},
		Destination:          Address{TON: 1, NPI: 1, Addr: strings.Repeat("2", 20)},
		ScheduleDeliveryTime: "261028120000004+",
		ValidityPeriod:       "000001000000000R",
		ShortMessage:         []byte{},
		Options: Options{
			SourcePort:       new(uint16(2948)),
			DestinationPort:  new(uint16(9200)),
			SarMsgRefNum:     new(uint16(1)),
			SarTotalSegments: new(byte(1)),
			SarSegmentSeqnum: new(byte(1)),
			MessagePayload:   bytes.Repeat([]byte{0xab}, 0xffff),
		},
	}}

	b, err := Encode(want, StatusOK, 1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// 16 header, 6 + 23 + 23 + 3 + 17 + 17 + 4 + 1 mandatory, 3 × 6 + 2 × 5 ports and sar_*, 4 + 65,535 payload.
	if len(b) != 16+94+28+4+0xffff {
		t.Fatalf("PDU of %d octets", len(b))
	}

	h, body, err := ReadPDU(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("ReadPDU: %v", err)
	}

	p, err := DecodeBody(h, body)
	if err != nil || !reflect.DeepEqual(p, want) {
		t.Fatalf("DecodeBody: %v", err)
	}
}

func TestDecodeTruncated(t *testing.T) {
	full := mustHex(t, pduVectors["submit_sm_resp"].hex)

	if _, err := DecodeHeader(full[:15]); !errors.Is(err, ErrTruncated) {
		t.Fatalf("DecodeHeader of 15 octets: err = %v, want ErrTruncated", err)
	}

	if _, _, err := Decode(full[:len(full)-1]); !errors.Is(err, ErrTruncated) {
		t.Fatalf("Decode: err = %v, want ErrTruncated", err)
	}

	if _, _, err := Decode(append(full, 0)); err == nil {
		t.Fatal("Decode accepted an octet after the PDU")
	}

	if _, _, err := ReadPDU(bytes.NewReader(full[:len(full)-1])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadPDU inside the body: err = %v, want io.ErrUnexpectedEOF", err)
	}

	if _, _, err := ReadPDU(bytes.NewReader(full[:10])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadPDU inside the header: err = %v, want io.ErrUnexpectedEOF", err)
	}

	if _, _, err := ReadPDU(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadPDU of nothing: err = %v, want io.EOF", err)
	}
}

func TestReadPDUAfterOversizedSubmit(t *testing.T) {
	// A 5 KiB submit_sm: 5,000 octets of message_payload (§5.3.2.32). The codec reads it whole; the submit mapping
	// rejects it with ESME_RINVMSGLEN. Then a submit_sm with sm_length beyond its body, whose ESME_RINVMSGLEN comes from
	// the codec. The enquire_link after each is still read.
	payload := strings.Repeat("ab", 5000)
	big := "000013ad" + "00000004" + "00000000" + "00000001" +
		"00" + "000000" + "000000" + "00" + "00" + "00" + "00" + "00" + "00" + "00" + "04" + "00" + "00" +
		"0424" + "1388" + payload
	bad := "00000023" + "00000004" + "00000000" + "00000002" +
		"00" + "000000" + "000000" + "00" + "00" + "00" + "00" + "00" + "00" + "00" + "00" + "00" + "05" + "0000"
	link := pduVectors["enquire_link"].hex

	r := bytes.NewReader(mustHex(t, big+link+bad+link))

	h, body, err := ReadPDU(r)
	if err != nil || h.Length != 5037 || len(body) != 5021 {
		t.Fatalf("ReadPDU = %+v, %d octets, %v", h, len(body), err)
	}

	p, err := DecodeBody(h, body)
	if err != nil || len(p.(*SubmitSM).Options.MessagePayload) != 5000 {
		t.Fatalf("DecodeBody: %v", err)
	}

	if h, _, err := ReadPDU(r); err != nil || h.ID != CmdEnquireLink {
		t.Fatalf("next PDU = %+v, %v", h, err)
	}

	h, body, err = ReadPDU(r)
	if err != nil {
		t.Fatalf("ReadPDU: %v", err)
	}

	var be *BodyError
	if _, err := DecodeBody(h, body); !errors.As(err, &be) || be.Status != StatusInvMsgLen {
		t.Fatalf("DecodeBody err = %v, want ESME_RINVMSGLEN", err)
	}

	if h, _, err := ReadPDU(r); err != nil || h.ID != CmdEnquireLink || h.Sequence != 5 {
		t.Fatalf("next PDU = %+v, %v", h, err)
	}
}

func TestDecodeTolerances(t *testing.T) {
	tests := map[string]struct {
		hex string
		pdu PDU
	}{
		// Table 4-19 defines a NULL message_id; one set by the ESME is kept.
		"deliver_sm_resp with message_id": {
			"00000012" + "80000005" + "00000000" + "00000001" + "3100", &DeliverSMResp{MessageID: "1"},
		},
		"deliver_sm_resp without body": {
			"00000010" + "80000005" + "00000000" + "00000001", &DeliverSMResp{},
		},
		// The header-only form that §4.1.2 and §4.1.4 prescribe for the two other binds is accepted for bind_transceiver_resp
		// too, as is a body in their error responses.
		"bind_transceiver_resp error without body": {
			"00000010" + "80000009" + "0000000d" + "00000001", &BindResp{ID: CmdBindTransceiverResp},
		},
		"bind_transmitter_resp error with system_id": {
			"00000012" + "80000002" + "0000000d" + "00000001" + "4100", &BindResp{ID: CmdBindTransmitterResp, SystemID: "A"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, p, err := Decode(mustHex(t, tt.hex))
			if err != nil || !reflect.DeepEqual(p, tt.pdu) {
				t.Fatalf("Decode = %#v, %v; want %#v", p, err, tt.pdu)
			}
		})
	}
}

func TestStrings(t *testing.T) {
	tests := map[string]struct{ got, want string }{
		"command":        {CmdBindTransceiverResp.String(), "bind_transceiver_resp"},
		"generic_nack":   {CmdGenericNack.String(), "generic_nack"},
		"status ok":      {StatusOK.String(), "ESME_ROK"},
		"status":         {StatusInvParLen.String(), "ESME_RINVPARLEN"},
		"status appn":    {StatusXTAppn.String(), "ESME_RX_T_APPN"},
		"status vendor":  {CommandStatus(0x401).String(), "vendor(0x00000401)"},
		"status reserve": {CommandStatus(0x09).String(), "reserved(0x00000009)"},
		"tag":            {TagSCInterfaceVersion.String(), "sc_interface_version"},
		"tag unknown":    {Tag(0x1400).String(), "tag(0x1400)"},
		"state":          {StateUndeliverable.String(), "UNDELIVERABLE"},
		"state reserved": {MessageState(9).String(), "reserved(9)"},
	}

	for name, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: %q, want %q", name, tt.got, tt.want)
		}
	}

	if !CmdSubmitSM.Response().IsResponse() || CmdSubmitSM.Response() != CmdSubmitSMResp || CmdSubmitSM.IsResponse() {
		t.Error("Response/IsResponse")
	}
}
