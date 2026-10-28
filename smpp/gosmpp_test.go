package smpp

// Interoperability with an independent codec, github.com/linxGnu/gosmpp (Apache-2.0), a test-only dependency: PDUs
// encoded by gosmpp are decoded here, and the reverse.

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/linxGnu/gosmpp/data"
	"github.com/linxGnu/gosmpp/pdu"
)

func gosmppBytes(t *testing.T, p pdu.PDU, seq int32) []byte {
	t.Helper()

	p.SetSequenceNumber(seq)

	b := pdu.NewBuffer(nil)
	p.Marshal(b)

	return b.Bytes()
}

func gosmppParse(t *testing.T, b []byte) pdu.PDU {
	t.Helper()

	p, err := pdu.Parse(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gosmpp Parse(%x): %v", b, err)
	}

	return p
}

// gosmppAddress returns an international E.164 address (TON 1, NPI 1).
func gosmppAddress(t *testing.T, addr string) pdu.Address {
	t.Helper()

	a, err := pdu.NewAddressWithTonNpiAddr(1, 1, addr)
	if err != nil {
		t.Fatal(err)
	}

	return a
}

// otaCommand is the user data of a submit_sm from an OTA platform: a command packet header without the UDH.
var otaCommand = []byte{0x00, 0x1a, 0x15, 0x06, 0x01, 0x25, 0x25, 0xb0, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00, 0x01}

func TestGosmppBindTransceiver(t *testing.T) {
	g := pdu.NewBindTransceiver().(*pdu.BindRequest)
	g.SystemID, g.Password, g.SystemType = "ota", "secret", "OTA"
	g.AddressRange = pdu.NewAddressRangeWithTonNpi(1, 1)

	b := gosmppBytes(t, g, 2)

	// gosmpp's octets are those of the hand-assembled §4.1.5 vector.
	if want := mustHex(t, pduVectors["bind_transceiver"].hex); !bytes.Equal(b, want) {
		t.Fatalf("gosmpp = %x\nvector   %x", b, want)
	}

	h, p, err := Decode(b)
	if err != nil || h.Sequence != 2 || !reflect.DeepEqual(p, pduVectors["bind_transceiver"].pdu) {
		t.Fatalf("Decode = %+v, %#v, %v", h, p, err)
	}

	ours, err := Encode(&Bind{
		ID: CmdBindTransceiver, SystemID: "ota", Password: "secret", SystemType: "OTA", InterfaceVersion: 0x34,
		AddrTON: 1, AddrNPI: 1, AddressRange: "^1555",
	}, StatusOK, 9)
	if err != nil {
		t.Fatal(err)
	}

	r, ok := gosmppParse(t, ours).(*pdu.BindRequest)
	if !ok || r.BindingType != pdu.Transceiver || r.SystemID != "ota" || r.Password != "secret" ||
		r.SystemType != "OTA" || r.InterfaceVersion != 0x34 || r.AddressRange.Ton != 1 || r.AddressRange.Npi != 1 ||
		r.AddressRange.AddressRange != "^1555" || r.SequenceNumber != 9 {
		t.Fatalf("gosmpp parsed %+v", r)
	}
}

func TestGosmppBindTransceiverResp(t *testing.T) {
	ours, err := Encode(&BindResp{
		ID: CmdBindTransceiverResp, SystemID: "EllaSMSC", Options: Options{SCInterfaceVersion: new(byte(0x34))},
	}, StatusOK, 2)
	if err != nil {
		t.Fatal(err)
	}

	r, ok := gosmppParse(t, ours).(*pdu.BindResp)
	if !ok || r.SystemID != "EllaSMSC" || !r.IsOk() {
		t.Fatalf("gosmpp parsed %+v", r)
	}

	if v := r.OptionalParameters[pdu.Tag(TagSCInterfaceVersion)]; !bytes.Equal(v.Data, []byte{0x34}) {
		t.Fatalf("sc_interface_version = %x", v.Data)
	}

	// §4.1.6, Table 4-6: a failed bind_transceiver_resp keeps its system_id, unlike the header-only failures of §4.1.2
	// and §4.1.4. gosmpp reads a system_id in every bind_transceiver_resp, and parses Ella's.
	nack, err := Encode(&BindResp{ID: CmdBindTransceiverResp, SystemID: "EllaSMSC"}, StatusBindFail, 2)
	if err != nil {
		t.Fatal(err)
	}

	r, ok = gosmppParse(t, nack).(*pdu.BindResp)
	if !ok || r.CommandStatus != data.ESME_RBINDFAIL || r.SystemID != "EllaSMSC" || r.SequenceNumber != 2 {
		t.Fatalf("gosmpp parsed %+v", r)
	}

	g := pdu.NewBindTransceiver().(*pdu.BindRequest).GetResponse().(*pdu.BindResp)
	g.CommandStatus = data.ESME_RBINDFAIL
	g.SystemID = "SMSC"

	_, p, err := Decode(gosmppBytes(t, g, 3))
	if err != nil || !reflect.DeepEqual(p, &BindResp{ID: CmdBindTransceiverResp, SystemID: "SMSC"}) {
		t.Fatalf("Decode = %#v, %v", p, err)
	}
}

func TestGosmppSubmitSM(t *testing.T) {
	// An OTA submit: UDH 027000 (TS 31.115 command packet IEI 0x70), binary data_coding 0x02, PID 0x7F.
	g := pdu.NewSubmitSM().(*pdu.SubmitSM)
	g.SourceAddr = gosmppAddress(t, "15550001000")
	g.DestAddr = gosmppAddress(t, "15551230002")
	g.EsmClass = 0x40
	g.ProtocolID = 0x7f
	g.RegisteredDelivery = 0x01
	g.ValidityPeriod = "000001000000000R"

	msg, err := pdu.NewBinaryShortMessageWithEncoding(otaCommand, data.BINARY8BIT1)
	if err != nil {
		t.Fatal(err)
	}

	msg.SetUDH(pdu.UDH{{ID: 0x70, Data: []byte{}}})
	g.Message = msg
	g.RegisterOptionalParam(pdu.Field{Tag: pdu.TagUserMessageReference, Data: []byte{0x00, 0x07}})

	h, p, err := Decode(gosmppBytes(t, g, 4))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	want := &SubmitSM{Message: Message{
		Source:             Address{TON: 1, NPI: 1, Addr: "15550001000"},
		Destination:        Address{TON: 1, NPI: 1, Addr: "15551230002"},
		ESMClass:           0x40,
		ProtocolID:         0x7f,
		ValidityPeriod:     "000001000000000R",
		RegisteredDelivery: 0x01,
		DataCoding:         0x02,
		ShortMessage:       append([]byte{0x02, 0x70, 0x00}, otaCommand...),
		Options:            Options{UserMessageReference: new(uint16(7))},
	}}
	if h.ID != CmdSubmitSM || h.Sequence != 4 || !reflect.DeepEqual(p, want) {
		t.Fatalf("Decode = %+v, %#v\nwant %#v", h, p, want)
	}

	ours, err := Encode(want, StatusOK, 5)
	if err != nil {
		t.Fatal(err)
	}

	r, ok := gosmppParse(t, ours).(*pdu.SubmitSM)
	if !ok {
		t.Fatal("not a gosmpp SubmitSM")
	}

	ud, _ := r.Message.GetMessageData()
	udh, _ := r.Message.UDH().MarshalBinary()

	if r.SourceAddr.Address() != "15550001000" || r.DestAddr.Address() != "15551230002" || r.DestAddr.Ton() != 1 ||
		r.EsmClass != 0x40 || r.ProtocolID != 0x7f || r.RegisteredDelivery != 1 ||
		r.ValidityPeriod != "000001000000000R" || r.Message.Encoding().DataCoding() != 0x02 ||
		!bytes.Equal(udh, []byte{0x02, 0x70, 0x00}) || !bytes.Equal(ud, otaCommand) || r.SequenceNumber != 5 {
		t.Fatalf("gosmpp parsed %+v, UDH %x, UD %x", r, udh, ud)
	}

	if v := r.OptionalParameters[pdu.TagUserMessageReference]; !bytes.Equal(v.Data, []byte{0, 7}) {
		t.Fatalf("user_message_reference = %x", v.Data)
	}
}

func TestGosmppSubmitSMResp(t *testing.T) {
	ours, err := Encode(&SubmitSMResp{MessageID: "42"}, StatusOK, 5)
	if err != nil {
		t.Fatal(err)
	}

	if r, ok := gosmppParse(t, ours).(*pdu.SubmitSMResp); !ok || r.MessageID != "42" || !r.IsOk() {
		t.Fatalf("gosmpp parsed %+v", r)
	}

	nack, err := Encode(&SubmitSMResp{}, StatusThrottled, 6)
	if err != nil {
		t.Fatal(err)
	}

	if r, ok := gosmppParse(t, nack).(*pdu.SubmitSMResp); !ok || r.CommandStatus != data.ESME_RTHROTTLED {
		t.Fatalf("gosmpp parsed %+v", r)
	}
}

func TestGosmppDeliverSM(t *testing.T) {
	// A receipt with the TLVs of §7.2 of the plan, a network_error_code and a vendor-specific TLV. gosmpp writes its
	// TLVs in map order, so only the decoded fields are compared.
	g := pdu.NewDeliverSM().(*pdu.DeliverSM)
	g.SourceAddr = gosmppAddress(t, "15551230002")
	g.DestAddr = gosmppAddress(t, "15550001000")
	g.EsmClass = 0x04

	msg, err := pdu.NewShortMessageWithEncoding("id:42 stat:DELIVRD", data.ASCII)
	if err != nil {
		t.Fatal(err)
	}

	g.Message = msg
	g.RegisterOptionalParam(pdu.Field{Tag: pdu.TagReceiptedMessageID, Data: []byte("42\x00")})
	g.RegisterOptionalParam(pdu.Field{Tag: pdu.TagMessageStateOption, Data: []byte{2}})
	g.RegisterOptionalParam(pdu.Field{Tag: pdu.TagUserMessageReference, Data: []byte{0, 7}})
	g.RegisterOptionalParam(pdu.Field{Tag: pdu.TagNetworkErrorCode, Data: []byte{3, 0, 0x22}})
	g.RegisterOptionalParam(pdu.Field{Tag: 0x1401, Data: []byte{0xee}})

	_, p, err := Decode(gosmppBytes(t, g, 8))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	want := &DeliverSM{Message: Message{
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
			Other:                []TLV{{Tag: 0x1401, Value: []byte{0xee}}},
		},
	}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("Decode = %#v\nwant %#v", p, want)
	}

	// The SIM answer of §7.3 of the plan: esm_class 0x50, PID 0x7F, the TP-UD with its UDH.
	answer := &DeliverSM{Message: Message{
		Source:       Address{TON: 1, NPI: 1, Addr: "15551230002"},
		Destination:  Address{TON: 1, NPI: 1, Addr: "15550001000"},
		ESMClass:     0x50,
		ProtocolID:   0x7f,
		DataCoding:   0x02,
		ShortMessage: []byte{0x02, 0x71, 0x00, 0x00, 0x0b, 0x0a, 0xb0, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00},
		Options:      Options{ReceiptedMessageID: new("42"), UserMessageReference: new(uint16(7))},
	}}

	ours, err := Encode(answer, StatusOK, 9)
	if err != nil {
		t.Fatal(err)
	}

	r, ok := gosmppParse(t, ours).(*pdu.DeliverSM)
	if !ok {
		t.Fatal("not a gosmpp DeliverSM")
	}

	ud, _ := r.Message.GetMessageData()
	udh, _ := r.Message.UDH().MarshalBinary()

	if r.EsmClass != 0x50 || r.ProtocolID != 0x7f || r.Message.Encoding().DataCoding() != 0x02 ||
		!bytes.Equal(append(udh, ud...), answer.ShortMessage) || r.SourceAddr.Address() != "15551230002" {
		t.Fatalf("gosmpp parsed %+v, UDH %x, UD %x", r, udh, ud)
	}

	id := r.OptionalParameters[pdu.TagReceiptedMessageID]
	if id.String() != "42" || !bytes.Equal(id.Data, []byte("42\x00")) {
		t.Fatalf("receipted_message_id = %x", id.Data)
	}

	if v := r.OptionalParameters[pdu.TagUserMessageReference]; !bytes.Equal(v.Data, []byte{0, 7}) {
		t.Fatalf("user_message_reference = %x", v.Data)
	}
}

func TestGosmppDeliverSMResp(t *testing.T) {
	g := pdu.NewDeliverSM().GetResponse()

	_, p, err := Decode(gosmppBytes(t, g, 9))
	if err != nil || !reflect.DeepEqual(p, &DeliverSMResp{}) {
		t.Fatalf("Decode = %#v, %v", p, err)
	}
}

func TestGosmppEnquireLink(t *testing.T) {
	for _, g := range []pdu.PDU{pdu.NewEnquireLink(), pdu.NewEnquireLink().GetResponse()} {
		b := gosmppBytes(t, g, 5)

		h, _, err := Decode(b)
		if err != nil {
			t.Fatalf("Decode(%x): %v", b, err)
		}

		var want []byte

		switch h.ID {
		case CmdEnquireLink:
			want = mustHex(t, pduVectors["enquire_link"].hex)
		case CmdEnquireLinkResp:
			want = mustHex(t, pduVectors["enquire_link_resp"].hex)
		default:
			t.Fatalf("command_id %v", h.ID)
		}

		if !bytes.Equal(b, want) {
			t.Fatalf("gosmpp = %x, vector %x", b, want)
		}
	}

	for _, p := range []PDU{&EnquireLink{}, &EnquireLinkResp{}} {
		ours, err := Encode(p, StatusOK, 11)
		if err != nil {
			t.Fatal(err)
		}

		g := gosmppParse(t, ours)
		if uint32(g.GetHeader().CommandID) != uint32(p.CommandID()) || g.GetSequenceNumber() != 11 {
			t.Fatalf("gosmpp parsed %+v", g.GetHeader())
		}
	}
}

func TestGosmppEmptyTLV(t *testing.T) {
	// Disagreement: gosmpp leaves out a TLV whose value is empty. §3.2.4.1 allows a Length of 0, and §5.3.2.32 sets no
	// minimum for message_payload, so this codec writes it (0424 0000) and decodes it as present and empty.
	g := pdu.NewDeliverSM().(*pdu.DeliverSM)
	g.RegisterOptionalParam(pdu.Field{Tag: pdu.TagMessagePayload, Data: []byte{}})

	_, p, err := Decode(gosmppBytes(t, g, 1))
	if err != nil || p.(*DeliverSM).Options.MessagePayload != nil {
		t.Fatalf("Decode = %#v, %v; gosmpp now writes an empty TLV", p, err)
	}

	ours, err := Encode(&DeliverSM{Message: Message{Options: Options{MessagePayload: []byte{}}}}, StatusOK, 1)
	if err != nil || !bytes.HasSuffix(ours, []byte{0x04, 0x24, 0x00, 0x00}) {
		t.Fatalf("Encode = %x, %v", ours, err)
	}

	_, p, err = Decode(ours)
	if err != nil || p.(*DeliverSM).Options.MessagePayload == nil {
		t.Fatalf("Decode = %#v, %v", p, err)
	}
}
