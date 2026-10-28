package esme

import (
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf16"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/smpp"
)

// The message types of esm_class in deliver_sm (SMPP 3.4 section 5.2.12).
const (
	esmTypeReceipt   = 0x04 // xx0001xx, SMSC Delivery Receipt
	esmTypeSIMAnswer = 0x10 // xx0100xx, SME Manual/User Acknowledgement
)

// receiptTime is the format of the dates of a receipt, YYMMDDhhmm (SMPP 3.4 Appendix B, Table B-1), in UTC.
const receiptTime = "0601021504"

// errNoUserData is a SIM answer whose SMS-DELIVER-REPORT has no TP-User-Data (TS 23.040 clause 9.2.3.27), or does
// not decode.
var errNoUserData = errors.New("the SMS-DELIVER-REPORT has no user data")

// stat is the final state of a message in a receipt (SMPP 3.4 Appendix B, Table B-2), by message_state (SMPP 3.4
// section 5.2.28).
var stat = map[smpp.MessageState]string{
	smpp.StateDelivered:     "DELIVRD",
	smpp.StateExpired:       "EXPIRED",
	smpp.StateDeleted:       "DELETED",
	smpp.StateUndeliverable: "UNDELIV",
}

// deliverSM returns the deliver_sm of a notification. Both kinds go from the original destination to the original
// source (SMPP 3.4 section 2.11), and carry the message ID and the ESME's user_message_reference (SMPP 3.4 Table
// 4-18).
func deliverSM(n db.Notification, alphabet db.Alphabet) (*smpp.DeliverSM, error) {
	m := n.Message
	id := strconv.FormatInt(m.ID, 10)

	d := &smpp.DeliverSM{Message: smpp.Message{
		Source:      smpp.Address{TON: m.Recipient.TypeOfNumber, NPI: m.Recipient.NumberingPlan, Addr: m.Recipient.Digits},
		Destination: smpp.Address{TON: m.Originator.TypeOfNumber, NPI: m.Originator.NumberingPlan, Addr: m.Originator.Digits},
		Options:     smpp.Options{ReceiptedMessageID: &id, UserMessageReference: n.UserMessageReference},
	}}

	if n.Type == db.NotificationReceipt {
		receipt(d, n)
		return d, nil
	}

	return d, simAnswer(d, n, alphabet)
}

// receipt makes d an SMSC Delivery Receipt (SMPP 3.4 section 2.11), with the text of SMPP 3.4 Appendix B, which is
// vendor specific: the message ID not padded, the dates in UTC, err the TP-Status of the outcome (TS 23.040 clause
// 9.2.3.15) in decimal, and text empty, since the content may be binary. data_coding is IA5, which does not depend on
// what the ESME takes 0x00 for.
func receipt(d *smpp.DeliverSM, n db.Notification) {
	m := n.Message
	state := smpp.MessageState(n.MessageState)

	delivered := "000"
	if state == smpp.StateDelivered {
		delivered = "001"
	}

	// The submit date of a replaced message is when it was replaced (SMPP 3.4 Appendix B, Table B-1).
	submitted := m.SubmittedAt
	if state == smpp.StateDeleted {
		submitted = m.UpdatedAt
	}

	d.ESMClass = esmTypeReceipt
	d.DataCoding = dcIA5
	d.ShortMessage = fmt.Appendf(nil, "id:%d sub:001 dlvrd:%s submit date:%s done date:%s stat:%s err:%03d text:",
		m.ID, delivered, submitted.UTC().Format(receiptTime), m.UpdatedAt.UTC().Format(receiptTime), stat[state], n.TPStatus)
	d.Options.MessageState = &state
}

// simAnswer makes d the SIM's answer: the TP-User-Data of the SMS-DELIVER-REPORT of the last delivery attempt, in
// RP-ACK or RP-ERROR (TS 23.040 clause 9.2.2.1a), as an SME Manual/User Acknowledgement, "an application generated
// reply message sent in response to an application request message" (SMPP 3.4 section 2.11). That type, and
// receipted_message_id, which SMPP 3.4 section 5.3.2.12 defines for receipts, are Ella's choices: SMPP 3.4 has no
// field for the answer.
func simAnswer(d *smpp.DeliverSM, n db.Notification, alphabet db.Alphabet) error {
	r, err := tpdu.DecodeDeliverReport(n.DeliverReport, n.Ack)
	if err != nil || r.UserDataLength == nil {
		return errNoUserData
	}

	original, err := tpdu.DecodeSubmit(n.Message.TPDU)
	if err != nil {
		return fmt.Errorf("decode the message: %w", err)
	}

	// The ME answers a (U)SIM data download with the TP-PID and TP-DCS of the message (TS 31.111 clause 7.1.1.1).
	d.ProtocolID = n.Message.ProtocolIdentifier
	if r.ProtocolIdentifier != nil {
		d.ProtocolID = *r.ProtocolIdentifier
	}

	// Without a TP-DCS, the TP-UD is in the GSM 7 bit default alphabet (TS 23.040 clause 9.2.3.27).
	var dcs byte
	if r.DataCodingScheme != nil {
		dcs = *r.DataCodingScheme
	}

	header, _, err := tpdu.SplitUserData(r.UserDataHeader, r.UserData)
	if err != nil {
		return errNoUserData
	}

	d.ESMClass = esmTypeSIMAnswer

	// An SMSC that delivers a UDH sets UDHI (SMPP 3.4 section 5.2.12, notes). A (U)SIM cannot set it on a Response
	// Packet, which is to be read as if it were set (TS 31.115 clause 4.1): for (U)SIM data download, the SMSC sets it
	// when the TP-UD starts with a well-formed UDH, so that an ESME never gets a UDHI on user data without one.
	if r.UserDataHeader || original.ProtocolIdentifier == tpdu.ProtocolUSIMDataDownload && startsWithHeader(r.UserData) {
		d.ESMClass |= esmUDHI
	}

	switch {
	case tpdu.IsSeptetCoded(dcs):
		septets := tpdu.UnpackSeptets(r.UserData, len(header), int(*r.UserDataLength))
		d.DataCoding, d.ShortMessage = septetsFor(header, septets, alphabet)
	case tpdu.IsUCS2(dcs):
		d.DataCoding, d.ShortMessage = dcUCS2, r.UserData
	default:
		d.DataCoding, d.ShortMessage = octetCoding(n.DataCoding), r.UserData
	}

	return nil
}

// septetsFor returns the data_coding and short_message of GSM 7 bit user data: unpacked septets as data_coding 0x00
// for an ESME that takes 0x00 for the GSM 7 bit default alphabet, and the text in UCS2 for one that takes it for
// Latin-1.
func septetsFor(header, septets []byte, alphabet db.Alphabet) (byte, []byte) {
	if alphabet != db.AlphabetLatin1 {
		return dcDefault, append(append([]byte{}, header...), septets...)
	}

	out := append([]byte{}, header...)
	for _, u := range utf16.Encode([]rune(tpdu.SeptetsText(septets))) {
		out = append(out, byte(u>>8), byte(u))
	}

	return dcUCS2, out
}

// octetCoding returns the data_coding of 8-bit user data: the submit_sm's when it codes 8-bit data, as pySim-smpp2sim
// echoes it, else 0x04 (SMPP 3.4 section 5.2.19).
func octetCoding(submitted byte) byte {
	switch {
	case submitted == dcOctet, submitted == dcOctetA, submitted >= 0xf4 && submitted <= 0xf7:
		return submitted
	default:
		return dcOctetA
	}
}

// retryDelay is how long a notification waits after its attempts-th failed attempt: 1 minute, doubling, at most 1
// hour.
func retryDelay(attempts int) time.Duration {
	const (
		first = time.Minute
		most  = time.Hour
	)

	if attempts >= 6 {
		return most
	}

	return min(first<<attempts, most)
}

// startsWithHeader reports whether user data start with a well-formed user data header (TS 23.040 clause 9.2.3.24).
func startsWithHeader(ud []byte) bool {
	return len(ud) > 0 && int(ud[0])+1 <= len(ud) && tpdu.ValidUserDataHeader(ud[:1+int(ud[0])])
}
