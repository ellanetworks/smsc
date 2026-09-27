package tbcd

import (
	"errors"
	"fmt"
)

var ErrInvalid = errors.New("invalid TBCD string")

func Decode(b []byte) (string, error) {
	if len(b) == 0 {
		return "", fmt.Errorf("%w: empty", ErrInvalid)
	}

	digits := make([]byte, 0, 2*len(b))

	for i, octet := range b {
		low, high := octet&0xf, octet>>4

		if low > 9 {
			return "", fmt.Errorf("%w: octet %d", ErrInvalid, i)
		}

		digits = append(digits, '0'+low)

		if high == 0xf && i == len(b)-1 {
			break
		}

		if high > 9 {
			return "", fmt.Errorf("%w: octet %d", ErrInvalid, i)
		}

		digits = append(digits, '0'+high)
	}

	return string(digits), nil
}

func Encode(digits string) ([]byte, error) {
	if digits == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalid)
	}

	out := make([]byte, 0, (len(digits)+1)/2)

	for i := 0; i < len(digits); i += 2 {
		low := digits[i]
		if low < '0' || low > '9' {
			return nil, fmt.Errorf("%w: digit %q", ErrInvalid, low)
		}

		high := byte(0xf)

		if i+1 < len(digits) {
			d := digits[i+1]
			if d < '0' || d > '9' {
				return nil, fmt.Errorf("%w: digit %q", ErrInvalid, d)
			}

			high = d - '0'
		}

		out = append(out, high<<4|(low-'0'))
	}

	return out, nil
}
