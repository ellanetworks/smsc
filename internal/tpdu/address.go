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
	// Digits holds the characters of the address, not digits, when TypeOfNumber is TypeOfNumberAlphanumeric.
	Digits string
}

const bcdDigits = "0123456789*#abc"

// maxAddressDigits is the most digits of an address field (TS 23.040 clause 9.1.2.5).
const maxAddressDigits = 20

// maxAlphanumericChars is the most characters of an alphanumeric address: the 12-octet address field leaves 10
// Address-Value octets (TS 23.040 clause 9.1.2.5).
const maxAlphanumericChars = 11

func decodeAddress(b []byte) (Address, int, error) {
	if len(b) < 2 {
		return Address{}, 0, fmt.Errorf("address: %w", errTruncated)
	}

	length := int(b[0])
	toa := b[1]
	valueOctets := (length + 1) / 2

	if length > maxAddressDigits {
		return Address{}, 0, fmt.Errorf("address: %w: %d digits exceeds %d", ErrUnsupportedAddress, length, maxAddressDigits)
	}

	if len(b) < 2+valueOctets {
		return Address{}, 0, fmt.Errorf("address: %w", errTruncated)
	}

	addr := Address{
		TypeOfNumber:  (toa >> 4) & 0x7,
		NumberingPlan: toa & 0xf,
	}

	if addr.TypeOfNumber == TypeOfNumberAlphanumeric {
		addr.Digits = decodeAlphanumeric(b[2:2+valueOctets], length)
		return addr, 2 + valueOctets, nil
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

// Encode returns the address field of TS 23.040 clause 9.1.2.5.
func (a Address) Encode() ([]byte, error) {
	if a.TypeOfNumber == TypeOfNumberAlphanumeric {
		return a.encodeAlphanumeric()
	}

	if a.TypeOfNumber > 0x7 || a.NumberingPlan > 0xf {
		return nil, fmt.Errorf("address: invalid type of address %d/%d", a.TypeOfNumber, a.NumberingPlan)
	}

	if len(a.Digits) > maxAddressDigits {
		return nil, fmt.Errorf("address: %d digits exceeds %d", len(a.Digits), maxAddressDigits)
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

// encodeAlphanumeric returns the address field of TS 23.040 clause 9.1.2.5 for Type-of-number 101: the characters of
// the default alphabet packed as in TS 23.038 clause 6.1.2.1.1, with Address-Length in useful semi-octets.
func (a Address) encodeAlphanumeric() ([]byte, error) {
	if a.NumberingPlan != 0 {
		return nil, fmt.Errorf("address: invalid type of address %d/%d", a.TypeOfNumber, a.NumberingPlan)
	}

	septets := make([]byte, 0, maxAlphanumericChars)

	for _, r := range a.Digits {
		s, ok := gsm7BasicIndex[r]
		if !ok {
			return nil, fmt.Errorf("address: %q is not in the GSM 7-bit default alphabet", r)
		}

		septets = append(septets, s)
	}

	if len(septets) == 0 || len(septets) > maxAlphanumericChars {
		return nil, fmt.Errorf("address: %d characters, want 1 to %d", len(septets), maxAlphanumericChars)
	}

	value := gsm7Part(nil, septets).UserData

	return append([]byte{byte((7*len(septets) + 3) / 4), 0x80 | TypeOfNumberAlphanumeric<<4}, value...), nil
}

// decodeAlphanumeric returns the characters of an alphanumeric Address-Value of length useful semi-octets. The count
// comes from the length, not the octets, so that fill bits are not read as '@'. An escape is read as the basic
// character at its index.
func decodeAlphanumeric(value []byte, length int) string {
	runes := make([]rune, length*4/7)
	for i := range runes {
		runes[i] = gsm7Basic[septetAt(value, i)]
	}

	return string(runes)
}
