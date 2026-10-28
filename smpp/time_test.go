package smpp

import (
	"errors"
	"testing"
	"time"
)

func zone(quarters int) *time.Location {
	return time.FixedZone("", quarters*15*60)
}

func TestParseTimeAbsolute(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	// §7.1.1: "YYMMDDhhmmsstnnp", local time nn quarter hours ahead of ('+') or behind ('-') UTC.
	tests := map[string]struct {
		s    string
		want time.Time
	}{
		"+ 1 hour":      {"261028120000004+", time.Date(2026, 10, 28, 12, 0, 0, 0, zone(4))},
		"- 2 hours":     {"261028120000308-", time.Date(2026, 10, 28, 12, 0, 0, 300_000_000, zone(-8))},
		"nn 00":         {"261231235959900+", time.Date(2026, 12, 31, 23, 59, 59, 900_000_000, time.UTC)},
		"nn 48 +":       {"270101000000048+", time.Date(2027, 1, 1, 0, 0, 0, 0, zone(48))},
		"nn 48 -":       {"270101000000048-", time.Date(2027, 1, 1, 0, 0, 0, 0, zone(-48))},
		"half hour":     {"261028120000022+", time.Date(2026, 10, 28, 12, 0, 0, 0, zone(22))},
		"leap day":      {"280229120000000+", time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)},
		"year window 0": {"000101000000000+", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTime(tt.s, now)
			if err != nil {
				t.Fatalf("ParseTime: %v", err)
			}

			if got.Relative || !got.Absolute.Equal(tt.want) || !got.At(now).Equal(tt.want) {
				t.Fatalf("ParseTime = %+v, want %v", got, tt.want)
			}

			_, off := got.Absolute.Zone()
			if _, want := tt.want.Zone(); off != want {
				t.Fatalf("offset %d s, want %d s", off, want)
			}

			s, err := FormatAbsolute(got.Absolute)
			if err != nil || s != tt.s {
				t.Fatalf("FormatAbsolute = %q, %v; want %q", s, err, tt.s)
			}
		})
	}
}

func TestParseTimeRelative(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		s    string
		p    RelativePeriod
		want time.Time
	}{
		// §14 of the plan: one day.
		"one day": {"000001000000000R", RelativePeriod{Days: 1}, now.Add(24 * time.Hour)},
		// §7.1.1.2: the example of 2 years, 6 months, 10 days, 23:34:29.
		"spec example": {
			"020610233429000R",
			RelativePeriod{Years: 2, Months: 6, Days: 10, Hours: 23, Minutes: 34, Seconds: 29},
			time.Date(2029, 5, 9, 11, 34, 29, 0, time.UTC),
		},
		// §7.1.1.2: t and nn are ignored.
		"t and nn ignored":        {"000000010000549R", RelativePeriod{Hours: 1}, now.Add(time.Hour)},
		"non-digit t and nn":      {"00000100000000AR", RelativePeriod{Days: 1}, now.Add(24 * time.Hour)},
		"non-digit t, nn of 99":   {"000000000001x99R", RelativePeriod{Seconds: 1}, now.Add(time.Second)},
		"non-digit nn, no digits": {"000000000000 ??R", RelativePeriod{}, now},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTime(tt.s, now)
			if err != nil {
				t.Fatalf("ParseTime: %v", err)
			}

			if !got.Relative || got.Period != tt.p || !got.At(now).Equal(tt.want) {
				t.Fatalf("ParseTime = %+v, at %v; want %+v, %v", got, got.At(now), tt.p, tt.want)
			}
		})
	}

	s, err := FormatRelative(RelativePeriod{Days: 1})
	if err != nil || s != "000001000000000R" {
		t.Fatalf("FormatRelative = %q, %v", s, err)
	}

	if _, err := FormatRelative(RelativePeriod{Hours: 100}); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("FormatRelative of 100 hours: err = %v", err)
	}
}

func TestParseTimeErrors(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	tests := map[string]string{
		"nn 49":            "261028120000049+",
		"nn 99":            "261028120000099-",
		"month 13":         "261328120000000+",
		"month 00":         "260028120000000+",
		"day 00":           "261000120000000+",
		"30 February":      "260230120000000+",
		"29 February 2026": "260229120000000+",
		"hour 24":          "261028240000000+",
		"minute 60":        "261028126000000+",
		"second 60":        "261028120060000+",
		"missing p":        "261028120000000",
		"p is a digit":     "2610281200000000",
		"p is Z":           "261028120000000Z",
		"too long":         "261028120000000+0",
		"empty":            "",
		"non-digit":        "26102812000a000+",
		"non-digit tenth":  "2610281200000x0+",
		"non-digit nn":     "26102812000000x+",
		"relative day 0a":  "00000a000000000R",
		"relative second":  "00000000000x000R",
		"short YYMMDDhhmm": "2610281200",
	}

	for name, s := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseTime(s, now); !errors.Is(err, ErrInvalidTime) {
				t.Fatalf("ParseTime(%q) = %+v, %v; want ErrInvalidTime", s, got, err)
			}
		})
	}
}

func TestParseTimeYearWindow(t *testing.T) {
	// Two-digit years are read in now-49…now+50.
	tests := map[string]struct {
		now  int
		yy   string
		want int
	}{
		"2026 26": {2026, "26", 2026},
		"2026 76": {2026, "76", 2076},
		"2026 77": {2026, "77", 1977},
		"2026 99": {2026, "99", 1999},
		"2026 00": {2026, "00", 2000},
		"2090 40": {2090, "40", 2140},
		"2090 41": {2090, "41", 2041},
		"2090 89": {2090, "89", 2089},
		"2000 50": {2000, "50", 2050},
		"2000 51": {2000, "51", 1951},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			now := time.Date(tt.now, 6, 1, 0, 0, 0, 0, time.UTC)

			got, err := ParseTime(tt.yy+"0101000000000+", now)
			if err != nil || got.Absolute.Year() != tt.want {
				t.Fatalf("ParseTime = %+v, %v; want year %d", got, err, tt.want)
			}
		})
	}
}

func TestFormatAbsoluteOffsets(t *testing.T) {
	tests := map[string]struct {
		t    time.Time
		want string
		ok   bool
	}{
		"UTC":            {time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "260102030405000+", true},
		"tenths":         {time.Date(2026, 1, 2, 3, 4, 5, 999_999_999, time.UTC), "260102030405900+", true},
		"-5:45":          {time.Date(2026, 1, 2, 3, 4, 5, 0, zone(-23)), "260102030405023-", true},
		"+12:00":         {time.Date(2026, 1, 2, 3, 4, 5, 0, zone(48)), "260102030405048+", true},
		"+13:00":         {time.Date(2026, 1, 2, 3, 4, 5, 0, zone(52)), "", false},
		"not a quarter":  {time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("", 7*60)), "", false},
		"-12:15 too far": {time.Date(2026, 1, 2, 3, 4, 5, 0, zone(-49)), "", false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := FormatAbsolute(tt.t)
			if tt.ok != (err == nil) || got != tt.want {
				t.Fatalf("FormatAbsolute = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestRelativePeriodMonthEnd(t *testing.T) {
	// §7.1.1.2: a period of years, months, days, hours, minutes and seconds from the current SMSC time. A day that the
	// target month does not have is clamped to its last day before the days and the time are added.
	tests := map[string]struct {
		now  time.Time
		s    string
		want time.Time
	}{
		"31 Jan + 1 month":          {date(2026, 1, 31), "000100000000000R", date(2026, 2, 28)},
		"31 Jan + 1 month, leap":    {date(2028, 1, 31), "000100000000000R", date(2028, 2, 29)},
		"31 Jan + 1 month 1 day":    {date(2026, 1, 31), "000101000000000R", date(2026, 3, 1)},
		"31 Aug + 1 month":          {date(2026, 8, 31), "000100000000000R", date(2026, 9, 30)},
		"31 Dec + 2 months":         {date(2026, 12, 31), "000200000000000R", date(2027, 2, 28)},
		"29 Feb + 1 year":           {date(2028, 2, 29), "010000000000000R", date(2029, 2, 28)},
		"29 Feb + 4 years":          {date(2028, 2, 29), "040000000000000R", date(2032, 2, 29)},
		"31 Mar + 99 months":        {date(2026, 3, 31), "009900000000000R", date(2034, 6, 30)},
		"30 Jan + 1 month 12 hours": {date(2026, 1, 30), "000100120000000R", date(2026, 2, 28).Add(12 * time.Hour)},
		"15 Jan + 1 month":          {date(2026, 1, 15), "000100000000000R", date(2026, 2, 15)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ParseTime(tt.s, tt.now)
			if err != nil || !got.At(tt.now).Equal(tt.want) {
				t.Fatalf("ParseTime = %+v, %v, at %v; want %v", got, err, got.At(tt.now), tt.want)
			}
		})
	}
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 9, 30, 0, 0, time.UTC)
}
