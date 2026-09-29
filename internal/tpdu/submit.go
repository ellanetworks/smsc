package tpdu

import (
	"errors"
	"fmt"
)

const (
	mtiDeliver = 0x0
	mtiSubmit  = 0x1
)

const MessageTypeCommand = 0x2

const (
	ValidityPeriodAbsent   = 0x0
	ValidityPeriodEnhanced = 0x1
	ValidityPeriodRelative = 0x2
	ValidityPeriodAbsolute = 0x3
)

var (
	errTruncated                 = errors.New("truncated")
	ErrUnsupportedMessageType    = errors.New("unsupported message type")
	ErrUnsupportedValidityPeriod = errors.New("unsupported validity period")
	ErrUserDataTooLong           = errors.New("user data exceeds 140 octets")
)

const maxUserDataOctets = 140

func MessageType(b []byte) (uint8, bool) {
	if len(b) == 0 {
		return 0, false
	}

	return b[0] & 0x3, true
}

type Submit struct {
	RejectDuplicates     bool
	ValidityPeriodFormat uint8
	StatusReportRequest  bool
	UserDataHeader       bool
	ReplyPath            bool
	MessageReference     uint8
	Destination          Address
	ProtocolIdentifier   byte
	DataCodingScheme     byte
	ValidityPeriod       []byte
	UserDataLength       uint8
	UserData             []byte
}

func DecodeSubmit(b []byte) (Submit, error) {
	if len(b) < 2 {
		return Submit{}, fmt.Errorf("sms-submit: %w", errTruncated)
	}

	first := b[0]
	if first&0x3 != mtiSubmit {
		return Submit{}, fmt.Errorf("sms-submit: message type indicator %d: %w", first&0x3, ErrUnsupportedMessageType)
	}

	s := Submit{
		RejectDuplicates:     first&0x04 != 0,
		ValidityPeriodFormat: (first >> 3) & 0x3,
		StatusReportRequest:  first&0x20 != 0,
		UserDataHeader:       first&0x40 != 0,
		ReplyPath:            first&0x80 != 0,
		MessageReference:     b[1],
	}

	dest, n, err := decodeAddress(b[2:])
	if err != nil {
		return Submit{}, fmt.Errorf("sms-submit: destination %w", err)
	}

	s.Destination = dest
	rest := b[2+n:]

	vpLength := map[uint8]int{
		ValidityPeriodAbsent:   0,
		ValidityPeriodRelative: 1,
		ValidityPeriodEnhanced: 7,
		ValidityPeriodAbsolute: 7,
	}[s.ValidityPeriodFormat]

	if len(rest) < 2+vpLength+1 {
		return Submit{}, fmt.Errorf("sms-submit: %w", errTruncated)
	}

	s.ProtocolIdentifier = rest[0]
	s.DataCodingScheme = rest[1]

	if vpLength > 0 {
		s.ValidityPeriod = append([]byte(nil), rest[2:2+vpLength]...)
	}

	if s.ValidityPeriodFormat == ValidityPeriodEnhanced {
		if err := validateEnhancedValidityPeriod(s.ValidityPeriod); err != nil {
			return Submit{}, fmt.Errorf("sms-submit: %w", err)
		}
	}

	rest = rest[2+vpLength:]
	s.UserDataLength = rest[0]
	rest = rest[1:]

	udOctets := userDataOctets(s.DataCodingScheme, s.UserDataLength)
	if udOctets > maxUserDataOctets {
		return Submit{}, fmt.Errorf("sms-submit: %w", ErrUserDataTooLong)
	}

	if len(rest) < udOctets {
		return Submit{}, fmt.Errorf("sms-submit: user data %w", errTruncated)
	}

	if len(rest) > udOctets {
		return Submit{}, fmt.Errorf("sms-submit: %d trailing octets", len(rest)-udOctets)
	}

	if udOctets > 0 {
		s.UserData = append([]byte(nil), rest...)
	}

	return s, nil
}

func (s Submit) Encode() ([]byte, error) {
	if len(s.UserData) != userDataOctets(s.DataCodingScheme, s.UserDataLength) {
		return nil, fmt.Errorf("sms-submit: user data is %d octets, length %d implies %d",
			len(s.UserData), s.UserDataLength, userDataOctets(s.DataCodingScheme, s.UserDataLength))
	}

	if len(s.UserData) > maxUserDataOctets {
		return nil, fmt.Errorf("sms-submit: %w", ErrUserDataTooLong)
	}

	da, err := s.Destination.encode()
	if err != nil {
		return nil, fmt.Errorf("sms-submit: destination %w", err)
	}

	first := byte(mtiSubmit) | (s.ValidityPeriodFormat&0x3)<<3

	if s.RejectDuplicates {
		first |= 0x04
	}

	if s.StatusReportRequest {
		first |= 0x20
	}

	if s.UserDataHeader {
		first |= 0x40
	}

	if s.ReplyPath {
		first |= 0x80
	}

	out := make([]byte, 0, 2+len(da)+2+len(s.ValidityPeriod)+1+len(s.UserData))
	out = append(out, first, s.MessageReference)
	out = append(out, da...)
	out = append(out, s.ProtocolIdentifier, s.DataCodingScheme)
	out = append(out, s.ValidityPeriod...)
	out = append(out, s.UserDataLength)
	out = append(out, s.UserData...)

	return out, nil
}

func validateEnhancedValidityPeriod(vp []byte) error {
	indicator := vp[0]

	if indicator&0x80 != 0 {
		return fmt.Errorf("%w: extended functionality indicator", ErrUnsupportedValidityPeriod)
	}

	switch indicator & 0x7 {
	case 0x0, 0x1, 0x3:
		return nil
	case 0x2:
		if vp[1] == 0 {
			return fmt.Errorf("%w: relative period of zero seconds", ErrUnsupportedValidityPeriod)
		}

		return nil
	default:
		return fmt.Errorf("%w: reserved format %d", ErrUnsupportedValidityPeriod, indicator&0x7)
	}
}
