package numbering

import (
	"errors"
	"fmt"
	"strings"
)

const (
	typeUnknown       = 0x0
	typeInternational = 0x1
	typeNational      = 0x2
)

var ErrUnsupportedNumber = errors.New("number cannot be converted to international format")

type Plan struct {
	CountryCode         string
	NationalPrefix      string
	InternationalPrefix string
}

func (p Plan) International(typeOfNumber uint8, digits string) (string, error) {
	switch typeOfNumber {
	case typeInternational:
		return digits, nil
	case typeNational:
		return p.CountryCode + digits, nil
	case typeUnknown:
		// Digits follow the dialling plan, so they may start with a prefix. When
		// both prefixes match, the longer one is the one that was dialled.
		international := p.InternationalPrefix != "" && strings.HasPrefix(digits, p.InternationalPrefix)
		national := p.NationalPrefix != "" && strings.HasPrefix(digits, p.NationalPrefix)

		switch {
		case international && (!national || len(p.InternationalPrefix) > len(p.NationalPrefix)):
			return strings.TrimPrefix(digits, p.InternationalPrefix), nil
		case national:
			return p.CountryCode + strings.TrimPrefix(digits, p.NationalPrefix), nil
		default:
			return p.CountryCode + digits, nil
		}
	default:
		return "", fmt.Errorf("%w: type of number %d", ErrUnsupportedNumber, typeOfNumber)
	}
}
