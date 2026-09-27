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
		switch {
		case p.InternationalPrefix != "" && strings.HasPrefix(digits, p.InternationalPrefix):
			return strings.TrimPrefix(digits, p.InternationalPrefix), nil
		case p.NationalPrefix != "" && strings.HasPrefix(digits, p.NationalPrefix):
			return p.CountryCode + strings.TrimPrefix(digits, p.NationalPrefix), nil
		default:
			return digits, nil
		}
	default:
		return "", fmt.Errorf("%w: type of number %d", ErrUnsupportedNumber, typeOfNumber)
	}
}
