package smpp

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidTime is returned for a string that is not a time in the format of §7.1.1.
var ErrInvalidTime = errors.New("smpp: invalid time")

const (
	timeLength        = 16 // "YYMMDDhhmmsstnnp"
	maxQuarterHours   = 48 // §7.1.1: nn is 00-48
	yearWindowHalf    = 50
	secondsPerQuarter = 15 * 60
)

// RelativePeriod is a relative time (§7.1.1.2): a number of years, months, days, hours, minutes and seconds from the
// current SMSC time, each 0 to 99.
type RelativePeriod struct {
	Years, Months, Days, Hours, Minutes, Seconds int
}

// From returns t advanced by the period, in calendar units: years and months first, with the day of the month clamped
// to the last day of the target month (31 January + 1 month is 28 or 29 February, 29 February + 1 year is 28
// February), then days, then hours, minutes and seconds. §7.1.1.2 gives no rule for a day that the target month does
// not have; the clamp keeps the expiry inside the month the ESME named.
func (p RelativePeriod) From(t time.Time) time.Time {
	year, month, day := t.Date()
	target := time.Date(year+p.Years, month+time.Month(p.Months), 1, 0, 0, 0, 0, t.Location())

	if last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day(); day > last {
		day = last
	}

	d := time.Duration(p.Hours)*time.Hour + time.Duration(p.Minutes)*time.Minute + time.Duration(p.Seconds)*time.Second

	return time.Date(target.Year(), target.Month(), day+p.Days, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(),
		t.Location()).Add(d)
}

// Time is a schedule_delivery_time or validity_period (§5.2.15, §5.2.16): an absolute time (§7.1.1.1) or a period
// relative to the current SMSC time (§7.1.1.2).
type Time struct {
	Relative bool
	// Absolute is the time when Relative is false. Its location is a fixed zone of the ±nn offset.
	Absolute time.Time
	// Period is the period when Relative is true.
	Period RelativePeriod
}

// At returns the absolute time, or now advanced by the relative period.
func (t Time) At(now time.Time) time.Time {
	if t.Relative {
		return t.Period.From(now)
	}

	return t.Absolute
}

// ParseTime parses "YYMMDDhhmmsstnnp" (§7.1.1). p is '+' or '-' for an absolute time whose local time is nn quarter
// hours ahead of or behind UTC, nn 00 to 48, or 'R' for a relative time, whose t and nn are ignored, digits or not
// (§7.1.1.2). The two-digit
// year of an absolute time is read as the year of the century nearest to now: from 49 years before now's year to 50
// years after it. An empty C-Octet String means no time; the caller handles it before calling ParseTime.
func ParseTime(s string, now time.Time) (Time, error) {
	if len(s) != timeLength {
		return Time{}, fmt.Errorf("%w: %q is %d characters, want %d", ErrInvalidTime, s, len(s), timeLength)
	}

	p := s[15]
	if p != 'R' && p != '+' && p != '-' {
		return Time{}, fmt.Errorf("%w: %q ends in %q, want '+', '-' or 'R'", ErrInvalidTime, s, p)
	}

	n := 7 // YY MM DD hh mm ss nn
	if p == 'R' {
		n = 6 // t and nn are ignored
	}

	var f [7]int

	for i := range n {
		pos := 2 * i
		if i == 6 {
			pos = 13
		}

		v, ok := twoDigits(s[pos : pos+2])
		if !ok {
			return Time{}, fmt.Errorf("%w: %q has a non-digit at %d", ErrInvalidTime, s, pos)
		}

		f[i] = v
	}

	if p == 'R' {
		return Time{Relative: true, Period: RelativePeriod{
			Years: f[0], Months: f[1], Days: f[2], Hours: f[3], Minutes: f[4], Seconds: f[5],
		}}, nil
	}

	if s[12] < '0' || s[12] > '9' {
		return Time{}, fmt.Errorf("%w: %q has a non-digit tenth of second", ErrInvalidTime, s)
	}

	tenths := int(s[12] - '0')

	if f[6] > maxQuarterHours {
		return Time{}, fmt.Errorf("%w: %q has an offset of %d quarter hours, more than %d", ErrInvalidTime, s, f[6],
			maxQuarterHours)
	}

	offset := f[6] * secondsPerQuarter
	if p == '-' {
		offset = -offset
	}

	year := windowYear(f[0], now.UTC().Year())
	month, day, hour, minute, second := f[1], f[2], f[3], f[4], f[5]

	t := time.Date(year, time.Month(month), day, hour, minute, second, tenths*int(100*time.Millisecond),
		time.FixedZone("", offset))
	if month < 1 || month > 12 || t.Day() != day || hour > 23 || minute > 59 || second > 59 {
		return Time{}, fmt.Errorf("%w: %q is not a calendar time", ErrInvalidTime, s)
	}

	return Time{Absolute: t}, nil
}

// windowYear returns the year ending in yy nearest to now: in now-49…now+50.
func windowYear(yy, now int) int {
	y := now - now%100 + yy

	switch {
	case y > now+yearWindowHalf:
		y -= 100
	case y <= now-yearWindowHalf:
		y += 100
	}

	return y
}

func twoDigits(s string) (int, bool) {
	if s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, false
	}

	return int(s[0]-'0')*10 + int(s[1]-'0'), true
}

// FormatAbsolute formats t as an absolute time (§7.1.1.1) in t's own location, whose offset from UTC must be a whole
// number of quarter hours, at most 48. Tenths of a second are truncated.
func FormatAbsolute(t time.Time) (string, error) {
	_, offset := t.Zone()
	if offset%secondsPerQuarter != 0 || offset > maxQuarterHours*secondsPerQuarter ||
		offset < -maxQuarterHours*secondsPerQuarter {
		return "", fmt.Errorf("%w: UTC offset of %d s is not 0 to 48 quarter hours", ErrInvalidTime, offset)
	}

	sign := byte('+')
	if offset < 0 {
		sign, offset = '-', -offset
	}

	return fmt.Sprintf("%02d%02d%02d%02d%02d%02d%d%02d%c", t.Year()%100, int(t.Month()), t.Day(), t.Hour(),
		t.Minute(), t.Second(), t.Nanosecond()/int(100*time.Millisecond), offset/secondsPerQuarter, sign), nil
}

// FormatRelative formats p as a relative time (§7.1.1.2), with t "0" and nn "00".
func FormatRelative(p RelativePeriod) (string, error) {
	fields := []int{p.Years, p.Months, p.Days, p.Hours, p.Minutes, p.Seconds}
	for _, v := range fields {
		if v < 0 || v > 99 {
			return "", fmt.Errorf("%w: relative period %+v has a field outside 0…99", ErrInvalidTime, p)
		}
	}

	return fmt.Sprintf("%02d%02d%02d%02d%02d%02d000R", p.Years, p.Months, p.Days, p.Hours, p.Minutes, p.Seconds), nil
}
