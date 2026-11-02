package esme

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ellanetworks/smsc/internal/db"
	"github.com/ellanetworks/smsc/internal/tpdu"
	"github.com/ellanetworks/smsc/smpp"
)

// SMPP 3.4 section 5.2.12
const (
	esmTypeReceipt   = 0x04
	esmTypeSIMAnswer = 0x10
)

// SMPP 3.4 appendix B table B-1
const receiptTime = "0601021504"

// TS 23.040 clause 9.2.3.27
var errNoUserData = errors.New("the SMS-DELIVER-REPORT has no user data")

// SMPP 3.4 appendix B table B-2
// SMPP 3.4 section 5.2.28
var stat = map[smpp.MessageState]string{
	smpp.StateDelivered:     "DELIVRD",
	smpp.StateExpired:       "EXPIRED",
	smpp.StateDeleted:       "DELETED",
	smpp.StateUndeliverable: "UNDELIV",
}

// SMPP 3.4 section 2.11
// SMPP 3.4 table 4-18
func deliverSM(n db.Notification) (*smpp.DeliverSM, error) {
	m := n.Message
	id := strconv.FormatInt(m.ID, 10)

	d := &smpp.DeliverSM{Message: smpp.Message{
		Source:      smpp.Address{TON: m.Recipient.TypeOfNumber, NPI: m.Recipient.NumberingPlan, Addr: m.Recipient.Digits},
		Destination: smpp.Address{TON: m.Originator.TypeOfNumber, NPI: n.SourceNPI, Addr: m.Originator.Digits},
		Options:     smpp.Options{ReceiptedMessageID: &id, UserMessageReference: n.UserMessageReference},
	}}

	if n.Type == db.NotificationReceipt {
		receipt(d, n)
		return d, nil
	}

	return d, simAnswer(d, n)
}

// SMPP 3.4 section 2.11
// SMPP 3.4 appendix B
// TS 23.040 clause 9.2.3.15
func receipt(d *smpp.DeliverSM, n db.Notification) {
	m := n.Message
	state := smpp.MessageState(n.MessageState)

	delivered := "000"
	if state == smpp.StateDelivered {
		delivered = "001"
	}

	d.ESMClass = esmTypeReceipt
	d.DataCoding = dcIA5
	d.ShortMessage = fmt.Appendf(nil, "id:%d sub:001 dlvrd:%s submit date:%s done date:%s stat:%s err:%03d text:",
		m.ID, delivered, m.SubmittedAt.UTC().Format(receiptTime), m.UpdatedAt.UTC().Format(receiptTime), stat[state], n.TPStatus)
	d.Options.MessageState = &state
}

// TS 23.040 clause 9.2.2.1a
// SMPP 3.4 section 2.11
// SMPP 3.4 section 5.3.2.12
func simAnswer(d *smpp.DeliverSM, n db.Notification) error {
	r, err := tpdu.DecodeDeliverReport(n.DeliverReport, n.Ack)
	if err != nil || r.UserDataLength == nil {
		return errNoUserData
	}

	original, err := tpdu.DecodeSubmit(n.Message.TPDU)
	if err != nil {
		return fmt.Errorf("decode the message: %w", err)
	}

	d.ProtocolID = n.Message.ProtocolIdentifier
	if r.ProtocolIdentifier != nil {
		d.ProtocolID = *r.ProtocolIdentifier
	}

	var dcs byte
	if r.DataCodingScheme != nil {
		dcs = *r.DataCodingScheme
	}

	header, _, err := tpdu.SplitUserData(r.UserDataHeader, r.UserData)
	if err != nil {
		return errNoUserData
	}

	d.ESMClass = esmTypeSIMAnswer

	if r.UserDataHeader || original.ProtocolIdentifier == tpdu.ProtocolUSIMDataDownload && startsWithHeader(r.UserData) {
		d.ESMClass |= esmUDHI
	}

	switch {
	case tpdu.IsSeptetCoded(dcs):
		septets := tpdu.UnpackSeptets(r.UserData, len(header), int(*r.UserDataLength))
		d.DataCoding, d.ShortMessage = dcDefault, append(append([]byte{}, header...), septets...)
	case tpdu.IsUCS2(dcs):
		d.DataCoding, d.ShortMessage = dcUCS2, r.UserData
	default:
		d.DataCoding, d.ShortMessage = octetCoding(n.DataCoding), r.UserData
	}

	return nil
}

// SMPP 3.4 section 5.2.19
func octetCoding(submitted byte) byte {
	switch {
	case submitted == dcOctet, submitted == dcOctetA, submitted >= 0xf4 && submitted <= 0xf7:
		return submitted
	default:
		return dcOctetA
	}
}

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

// TS 23.040 clause 9.2.3.24
func startsWithHeader(ud []byte) bool {
	return len(ud) > 0 && int(ud[0])+1 <= len(ud) && tpdu.ValidUserDataHeader(ud[:1+int(ud[0])])
}
