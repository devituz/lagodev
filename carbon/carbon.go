// Package carbon is a small ergonomic wrapper around time.Time
// modelled on Laravel's Carbon helper.
//
// The standard library is already excellent — this package only adds
// the methods callers tend to wish stdlib had: human-readable diffs,
// boundary helpers (StartOfDay, EndOfMonth, …), Parse with multiple
// layouts, and chainable Add helpers.
//
// A Carbon is just `time.Time` with methods, so callers can drop in
// any place a stdlib time is accepted:
//
//	t := carbon.Now()
//	t = t.AddDays(7).StartOfDay()
//	fmt.Println(t.DiffForHumans(carbon.Now())) // "1 week from now"
package carbon

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Carbon wraps time.Time. The zero value is a zero Carbon (use IsZero
// to check).
type Carbon struct {
	t time.Time
}

// Now returns the current instant in the local timezone.
func Now() Carbon { return Carbon{t: time.Now()} }

// NowIn returns the current instant in the given location.
func NowIn(loc *time.Location) Carbon { return Carbon{t: time.Now().In(loc)} }

// From wraps an existing time.Time.
func From(t time.Time) Carbon { return Carbon{t: t} }

// Time returns the underlying time.Time.
func (c Carbon) Time() time.Time { return c.t }

// IsZero reports whether the wrapped time is the zero value.
func (c Carbon) IsZero() bool { return c.t.IsZero() }

// Equal compares two Carbons by wall-clock instant.
func (c Carbon) Equal(o Carbon) bool { return c.t.Equal(o.t) }

// Before / After delegate to time.Time.
func (c Carbon) Before(o Carbon) bool { return c.t.Before(o.t) }
func (c Carbon) After(o Carbon) bool  { return c.t.After(o.t) }

// ParseLayouts is the list of layouts Parse tries in order. The
// default set covers the common shapes lagodev apps see: RFC3339,
// SQL datetime, plain date, and ISO date.
var ParseLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02",
	"02/01/2006",
	"01/02/2006",
}

// Parse returns the first layout in ParseLayouts that successfully
// decodes s.
func Parse(s string) (Carbon, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Carbon{}, errors.New("carbon: empty input")
	}
	var lastErr error
	for _, layout := range ParseLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return Carbon{t: t}, nil
		} else {
			lastErr = err
		}
	}
	return Carbon{}, fmt.Errorf("carbon: cannot parse %q: %w", s, lastErr)
}

// MustParse panics on error. Use only with hard-coded inputs.
func MustParse(s string) Carbon {
	c, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return c
}

// Format delegates to time.Time.Format.
func (c Carbon) Format(layout string) string { return c.t.Format(layout) }

// ISO8601 is shorthand for Format(time.RFC3339).
func (c Carbon) ISO8601() string { return c.t.Format(time.RFC3339) }

// Date returns YYYY-MM-DD.
func (c Carbon) Date() string { return c.t.Format("2006-01-02") }

// DateTime returns YYYY-MM-DD HH:MM:SS.
func (c Carbon) DateTime() string { return c.t.Format("2006-01-02 15:04:05") }

// --- arithmetic ---------------------------------------------------------

func (c Carbon) Add(d time.Duration) Carbon  { return Carbon{t: c.t.Add(d)} }
func (c Carbon) Sub(o Carbon) time.Duration  { return c.t.Sub(o.t) }
func (c Carbon) AddSeconds(n int) Carbon     { return c.Add(time.Duration(n) * time.Second) }
func (c Carbon) AddMinutes(n int) Carbon     { return c.Add(time.Duration(n) * time.Minute) }
func (c Carbon) AddHours(n int) Carbon       { return c.Add(time.Duration(n) * time.Hour) }
func (c Carbon) AddDays(n int) Carbon        { return Carbon{t: c.t.AddDate(0, 0, n)} }
func (c Carbon) AddWeeks(n int) Carbon       { return Carbon{t: c.t.AddDate(0, 0, n*7)} }
func (c Carbon) AddMonths(n int) Carbon      { return Carbon{t: c.t.AddDate(0, n, 0)} }
func (c Carbon) AddYears(n int) Carbon       { return Carbon{t: c.t.AddDate(n, 0, 0)} }
func (c Carbon) SubDays(n int) Carbon        { return c.AddDays(-n) }
func (c Carbon) SubMonths(n int) Carbon      { return c.AddMonths(-n) }
func (c Carbon) SubYears(n int) Carbon       { return c.AddYears(-n) }

// --- boundaries ---------------------------------------------------------

// StartOfDay sets the time to 00:00:00 on the same day.
func (c Carbon) StartOfDay() Carbon {
	y, m, d := c.t.Date()
	return Carbon{t: time.Date(y, m, d, 0, 0, 0, 0, c.t.Location())}
}

// EndOfDay sets the time to 23:59:59.999999999 on the same day.
func (c Carbon) EndOfDay() Carbon {
	y, m, d := c.t.Date()
	return Carbon{t: time.Date(y, m, d, 23, 59, 59, int(time.Second-time.Nanosecond), c.t.Location())}
}

// StartOfMonth sets the time to the first day of the month at 00:00.
func (c Carbon) StartOfMonth() Carbon {
	y, m, _ := c.t.Date()
	return Carbon{t: time.Date(y, m, 1, 0, 0, 0, 0, c.t.Location())}
}

// EndOfMonth returns the last instant of the month.
func (c Carbon) EndOfMonth() Carbon {
	first := c.StartOfMonth()
	return first.AddMonths(1).Add(-time.Nanosecond)
}

// StartOfYear sets the time to Jan 1 00:00 in the same year.
func (c Carbon) StartOfYear() Carbon {
	y := c.t.Year()
	return Carbon{t: time.Date(y, time.January, 1, 0, 0, 0, 0, c.t.Location())}
}

// EndOfYear returns the last instant of the year.
func (c Carbon) EndOfYear() Carbon {
	return c.StartOfYear().AddYears(1).Add(-time.Nanosecond)
}

// --- queries ------------------------------------------------------------

func (c Carbon) IsPast() bool   { return c.t.Before(time.Now()) }
func (c Carbon) IsFuture() bool { return c.t.After(time.Now()) }
func (c Carbon) IsToday() bool  { return c.StartOfDay().Equal(Now().StartOfDay()) }

// DiffInDays returns the difference in calendar days (rounded toward
// zero) between c and o (c - o).
func (c Carbon) DiffInDays(o Carbon) int {
	return int(c.t.Sub(o.t) / (24 * time.Hour))
}

// DiffForHumans renders a relative description of (c - o):
//
//	"5 seconds ago"
//	"3 minutes from now"
//	"1 hour ago"
//	"2 days from now"
func (c Carbon) DiffForHumans(o Carbon) string {
	d := c.t.Sub(o.t)
	abs := d
	suffix := " from now"
	if d < 0 {
		abs = -d
		suffix = " ago"
	}
	switch {
	case abs < time.Minute:
		return plural(int(abs/time.Second), "second") + suffix
	case abs < time.Hour:
		return plural(int(abs/time.Minute), "minute") + suffix
	case abs < 24*time.Hour:
		return plural(int(abs/time.Hour), "hour") + suffix
	case abs < 7*24*time.Hour:
		return plural(int(abs/(24*time.Hour)), "day") + suffix
	case abs < 30*24*time.Hour:
		return plural(int(abs/(7*24*time.Hour)), "week") + suffix
	case abs < 365*24*time.Hour:
		return plural(int(abs/(30*24*time.Hour)), "month") + suffix
	default:
		return plural(int(abs/(365*24*time.Hour)), "year") + suffix
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
