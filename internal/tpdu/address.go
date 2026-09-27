package tpdu

import (
	"errors"
	"fmt"
)

const (
	TypeOfNumberInternational = 0x1
	TypeOfNumberAlphanumeric  = 0x5
	NumberingPlanISDN         = 0x1
)

var ErrUnsupportedAddress = errors.New("unsupported address type")

type Address struct {
	TypeOfNumber  uint8
	NumberingPlan uint8
	Digits        string
}

const bcdDigits = "0123456789*#abc"

func decodeAddress(b []byte) (Address, int, error) {
	if len(b) < 2 {
		return Address{}, 0, fmt.Errorf("address: %w", errTruncated)
	}

	length := int(b[0])
	toa := b[1]
	valueOctets := (length + 1) / 2

	if len(b) < 2+valueOctets {
		return Address{}, 0, fmt.Errorf("address: %w", errTruncated)
	}

	addr := Address{
		TypeOfNumber:  (toa >> 4) & 0x7,
		NumberingPlan: toa & 0xf,
	}

	if addr.TypeOfNumber == TypeOfNumberAlphanumeric {
		return Address{}, 0, fmt.Errorf("address: %w: alphanumeric", ErrUnsupportedAddress)
	}

	digits := make([]byte, 0, length)

	for i := range length {
		nibble := b[2+i/2] >> (4 * (i % 2)) & 0xf
		if int(nibble) >= len(bcdDigits) {
			return Address{}, 0, fmt.Errorf("address: invalid semi-octet 0x%x", nibble)
		}

		digits = append(digits, bcdDigits[nibble])
	}

	addr.Digits = string(digits)

	return addr, 2 + valueOctets, nil
}

func (a Address) encode() ([]byte, error) {
	if a.TypeOfNumber == TypeOfNumberAlphanumeric {
		return nil, fmt.Errorf("address: %w: alphanumeric", ErrUnsupportedAddress)
	}

	if a.TypeOfNumber > 0x7 || a.NumberingPlan > 0xf {
		return nil, fmt.Errorf("address: invalid type of address %d/%d", a.TypeOfNumber, a.NumberingPlan)
	}

	if len(a.Digits) > 20 {
		return nil, fmt.Errorf("address: %d digits exceeds 20", len(a.Digits))
	}

	out := make([]byte, 2, 2+(len(a.Digits)+1)/2)
	out[0] = byte(len(a.Digits))
	out[1] = 0x80 | a.TypeOfNumber<<4 | a.NumberingPlan

	for i := 0; i < len(a.Digits); i += 2 {
		low, err := semiOctet(a.Digits[i])
		if err != nil {
			return nil, err
		}

		high := byte(0xf)

		if i+1 < len(a.Digits) {
			high, err = semiOctet(a.Digits[i+1])
			if err != nil {
				return nil, err
			}
		}

		out = append(out, high<<4|low)
	}

	return out, nil
}

func semiOctet(c byte) (byte, error) {
	for i := range len(bcdDigits) {
		if bcdDigits[i] == c {
			return byte(i), nil
		}
	}

	return 0, fmt.Errorf("address: invalid digit %q", c)
}
