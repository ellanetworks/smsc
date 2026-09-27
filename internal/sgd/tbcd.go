package sgd

import (
	"errors"
	"fmt"
)

var errInvalidTBCD = errors.New("invalid TBCD string")

func decodeTBCD(b []byte) (string, error) {
	if len(b) == 0 {
		return "", fmt.Errorf("%w: empty", errInvalidTBCD)
	}

	digits := make([]byte, 0, 2*len(b))

	for i, octet := range b {
		low, high := octet&0xf, octet>>4

		if low > 9 {
			return "", fmt.Errorf("%w: octet %d", errInvalidTBCD, i)
		}

		digits = append(digits, '0'+low)

		if high == 0xf && i == len(b)-1 {
			break
		}

		if high > 9 {
			return "", fmt.Errorf("%w: octet %d", errInvalidTBCD, i)
		}

		digits = append(digits, '0'+high)
	}

	return string(digits), nil
}
