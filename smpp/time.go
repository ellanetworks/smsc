package smpp

import (
	"errors"
	"fmt"
	"time"
)

var ErrInvalidTime = errors.New("smpp: invalid time")

const (
	timeLength        = 16
	maxQuarterHours   = 48
	yearWindowHalf    = 50
	secondsPerQuarter = 15 * 60
)

// SMPP 3.4 section 7.1.1.2
type RelativePeriod struct {
	Years, Months, Days, Hours, Minutes, Seconds int
}

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

// SMPP 3.4 section 5.2.15
// SMPP 3.4 section 5.2.16
// SMPP 3.4 section 7.1.1.1
// SMPP 3.4 section 7.1.1.2
type Time struct {
	Relative bool
	Absolute time.Time
	Period   RelativePeriod
}

func (t Time) At(now time.Time) time.Time {
	if t.Relative {
		return t.Period.From(now)
	}

	return t.Absolute
}

// SMPP 3.4 section 7.1.1
// SMPP 3.4 section 7.1.1.2
func ParseTime(s string, now time.Time) (Time, error) {
	if len(s) != timeLength {
		return Time{}, fmt.Errorf("%w: %q is %d characters, want %d", ErrInvalidTime, s, len(s), timeLength)
	}

	p := s[15]
	if p != 'R' && p != '+' && p != '-' {
		return Time{}, fmt.Errorf("%w: %q ends in %q, want '+', '-' or 'R'", ErrInvalidTime, s, p)
	}

	n := 7
	if p == 'R' {
		n = 6
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

// SMPP 3.4 section 7.1.1.1
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

// SMPP 3.4 section 7.1.1.2
func FormatRelative(p RelativePeriod) (string, error) {
	fields := []int{p.Years, p.Months, p.Days, p.Hours, p.Minutes, p.Seconds}
	for _, v := range fields {
		if v < 0 || v > 99 {
			return "", fmt.Errorf("%w: relative period %+v has a field outside 0…99", ErrInvalidTime, p)
		}
	}

	return fmt.Sprintf("%02d%02d%02d%02d%02d%02d000R", p.Years, p.Months, p.Days, p.Hours, p.Minutes, p.Seconds), nil
}
