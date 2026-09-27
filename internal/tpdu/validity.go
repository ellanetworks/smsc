package tpdu

import (
	"fmt"
	"time"
)

func (s Submit) SingleShot() bool {
	return s.ValidityPeriodFormat == ValidityPeriodEnhanced && s.ValidityPeriod[0]&0x40 != 0
}

func (s Submit) Expiry(receivedAt time.Time) (time.Time, bool, error) {
	switch s.ValidityPeriodFormat {
	case ValidityPeriodRelative:
		return receivedAt.Add(relativePeriod(s.ValidityPeriod[0])), true, nil
	case ValidityPeriodAbsolute:
		t, err := decodeTimeStamp(s.ValidityPeriod)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("%w: %w", ErrUnsupportedValidityPeriod, err)
		}

		return t, true, nil
	case ValidityPeriodEnhanced:
		return enhancedExpiry(s.ValidityPeriod, receivedAt)
	default:
		return time.Time{}, false, nil
	}
}

func relativePeriod(v byte) time.Duration {
	switch {
	case v <= 143:
		return time.Duration(int(v)+1) * 5 * time.Minute
	case v <= 167:
		return 12*time.Hour + time.Duration(int(v)-143)*30*time.Minute
	case v <= 196:
		return time.Duration(int(v)-166) * 24 * time.Hour
	default:
		return time.Duration(int(v)-192) * 7 * 24 * time.Hour
	}
}

func enhancedExpiry(vp []byte, receivedAt time.Time) (time.Time, bool, error) {
	switch vp[0] & 0x7 {
	case 0x1:
		return receivedAt.Add(relativePeriod(vp[1])), true, nil
	case 0x2:
		return receivedAt.Add(time.Duration(vp[1]) * time.Second), true, nil
	case 0x3:
		hours, errH := semiOctetValue(vp[1])
		minutes, errM := semiOctetValue(vp[2])
		seconds, errS := semiOctetValue(vp[3])

		if errH != nil || errM != nil || errS != nil {
			return time.Time{}, false, fmt.Errorf("%w: non-decimal relative time", ErrUnsupportedValidityPeriod)
		}

		d := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second

		return receivedAt.Add(d), true, nil
	default:
		return time.Time{}, false, nil
	}
}

func decodeTimeStamp(b []byte) (time.Time, error) {
	if len(b) != 7 {
		return time.Time{}, fmt.Errorf("time stamp of %d octets", len(b))
	}

	var fields [6]int

	for i := range fields {
		v, err := semiOctetValue(b[i])
		if err != nil {
			return time.Time{}, err
		}

		fields[i] = v
	}

	quarters, err := semiOctetValue(b[6] &^ 0x08)
	if err != nil {
		return time.Time{}, err
	}

	offset := quarters * 15 * 60
	if b[6]&0x08 != 0 {
		offset = -offset
	}

	t := time.Date(2000+fields[0], time.Month(fields[1]), fields[2], fields[3], fields[4], fields[5], 0, time.FixedZone("", offset))
	if t.Month() != time.Month(fields[1]) || t.Day() != fields[2] || fields[3] > 23 || fields[4] > 59 || fields[5] > 59 {
		return time.Time{}, fmt.Errorf("invalid calendar time %v", fields)
	}

	return t, nil
}

func semiOctetValue(b byte) (int, error) {
	low, high := b&0xf, b>>4
	if low > 9 || high > 9 {
		return 0, fmt.Errorf("non-decimal semi-octet 0x%02x", b)
	}

	return int(low)*10 + int(high), nil
}
