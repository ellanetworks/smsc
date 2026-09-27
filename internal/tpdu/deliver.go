package tpdu

import (
	"fmt"
	"time"
)

type Deliver struct {
	MoreMessagesToSend     bool
	StatusReportIndication bool
	UserDataHeader         bool
	ReplyPath              bool
	Originator             Address
	ProtocolIdentifier     byte
	DataCodingScheme       byte
	ServiceCentreTimeStamp time.Time
	UserDataLength         uint8
	UserData               []byte
}

func DeliverFromSubmit(s Submit, originator Address, receivedAt time.Time) Deliver {
	return Deliver{
		StatusReportIndication: s.StatusReportRequest,
		UserDataHeader:         s.UserDataHeader,
		Originator:             originator,
		ProtocolIdentifier:     s.ProtocolIdentifier,
		DataCodingScheme:       s.DataCodingScheme,
		ServiceCentreTimeStamp: receivedAt,
		UserDataLength:         s.UserDataLength,
		UserData:               s.UserData,
	}
}

func (d Deliver) Encode() ([]byte, error) {
	if len(d.UserData) != userDataOctets(d.DataCodingScheme, d.UserDataLength) {
		return nil, fmt.Errorf("sms-deliver: user data is %d octets, length %d implies %d",
			len(d.UserData), d.UserDataLength, userDataOctets(d.DataCodingScheme, d.UserDataLength))
	}

	oa, err := d.Originator.encode()
	if err != nil {
		return nil, fmt.Errorf("sms-deliver: originator %w", err)
	}

	scts, err := encodeTimeStamp(d.ServiceCentreTimeStamp)
	if err != nil {
		return nil, fmt.Errorf("sms-deliver: %w", err)
	}

	first := byte(mtiDeliver)
	if !d.MoreMessagesToSend {
		first |= 0x04
	}

	if d.StatusReportIndication {
		first |= 0x20
	}

	if d.UserDataHeader {
		first |= 0x40
	}

	if d.ReplyPath {
		first |= 0x80
	}

	out := make([]byte, 0, 1+len(oa)+2+len(scts)+1+len(d.UserData))
	out = append(out, first)
	out = append(out, oa...)
	out = append(out, d.ProtocolIdentifier, d.DataCodingScheme)
	out = append(out, scts...)
	out = append(out, d.UserDataLength)
	out = append(out, d.UserData...)

	return out, nil
}

func encodeTimeStamp(t time.Time) ([]byte, error) {
	_, offset := t.Zone()
	if offset%(15*60) != 0 {
		return nil, fmt.Errorf("time zone offset %ds is not a whole quarter hour", offset)
	}

	quarters := offset / (15 * 60)

	var sign byte
	if quarters < 0 {
		sign = 0x08
		quarters = -quarters
	}

	if quarters > 79 {
		return nil, fmt.Errorf("time zone offset %ds is out of range", offset)
	}

	return []byte{
		swappedBCD(t.Year() % 100),
		swappedBCD(int(t.Month())),
		swappedBCD(t.Day()),
		swappedBCD(t.Hour()),
		swappedBCD(t.Minute()),
		swappedBCD(t.Second()),
		swappedBCD(quarters) | sign,
	}, nil
}

func swappedBCD(v int) byte {
	return byte(v%10)<<4 | byte(v/10)
}
