package carbon

import (
	"strings"
	"testing"
	"time"
)

func TestNow_NonZero(t *testing.T) {
	if Now().IsZero() {
		t.Fatal("Now() must not be zero")
	}
}

func TestParse_MultipleLayouts(t *testing.T) {
	cases := []string{
		"2026-05-30T12:34:56Z",
		"2026-05-30 12:34:56",
		"2026-05-30",
	}
	for _, s := range cases {
		c, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		if c.t.Year() != 2026 {
			t.Fatalf("Parse(%q) year = %d", s, c.t.Year())
		}
	}
}

func TestParse_Empty(t *testing.T) {
	if _, err := Parse(""); err == nil {
		t.Fatal("Parse empty must error")
	}
}

func TestParse_Unrecognised(t *testing.T) {
	if _, err := Parse("not a date"); err == nil {
		t.Fatal("Parse garbage must error")
	}
}

func TestMustParse_PanicsOnError(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustParse must panic on bad input")
		}
	}()
	_ = MustParse("garbage")
}

func TestArithmetic_AddDaysMonthsYears(t *testing.T) {
	c := MustParse("2026-01-31")
	if got := c.AddDays(1).Date(); got != "2026-02-01" {
		t.Fatalf("AddDays(1) = %s", got)
	}
	// Adding a month to Jan 31 rolls over to Mar 3 (Feb has 28 days
	// in 2026 — not a leap year). That matches time.AddDate semantics.
	if got := c.AddMonths(1).Date(); got != "2026-03-03" {
		t.Fatalf("AddMonths(1) Jan 31 = %s", got)
	}
	if got := c.AddYears(2).Date(); got != "2028-01-31" {
		t.Fatalf("AddYears(2) = %s", got)
	}
}

func TestBoundaries_StartEndOfDay(t *testing.T) {
	c := MustParse("2026-05-30T15:45:00Z")
	if got := c.StartOfDay().DateTime(); got != "2026-05-30 00:00:00" {
		t.Fatalf("StartOfDay = %s", got)
	}
	end := c.EndOfDay()
	if end.Format("15:04:05") != "23:59:59" {
		t.Fatalf("EndOfDay = %s", end.DateTime())
	}
}

func TestBoundaries_StartEndOfMonth(t *testing.T) {
	c := MustParse("2026-02-14")
	if got := c.StartOfMonth().Date(); got != "2026-02-01" {
		t.Fatalf("StartOfMonth = %s", got)
	}
	if got := c.EndOfMonth().Date(); got != "2026-02-28" {
		t.Fatalf("EndOfMonth Feb 2026 = %s", got)
	}
}

func TestBoundaries_StartEndOfYear(t *testing.T) {
	c := MustParse("2026-06-15")
	if got := c.StartOfYear().Date(); got != "2026-01-01" {
		t.Fatalf("StartOfYear = %s", got)
	}
	if got := c.EndOfYear().Date(); got != "2026-12-31" {
		t.Fatalf("EndOfYear = %s", got)
	}
}

func TestDiffInDays(t *testing.T) {
	a := MustParse("2026-05-30")
	b := MustParse("2026-05-25")
	if got := a.DiffInDays(b); got != 5 {
		t.Fatalf("DiffInDays = %d", got)
	}
	if got := b.DiffInDays(a); got != -5 {
		t.Fatalf("reverse DiffInDays = %d", got)
	}
}

func TestDiffForHumans(t *testing.T) {
	base := Now()
	cases := []struct {
		offset time.Duration
		want   string
	}{
		{-30 * time.Second, "30 seconds ago"},
		{-5 * time.Minute, "5 minutes ago"},
		{-2 * time.Hour, "2 hours ago"},
		{-3 * 24 * time.Hour, "3 days ago"},
		{2 * time.Hour, "2 hours from now"},
		{14 * 24 * time.Hour, "2 weeks from now"},
		{60 * 24 * time.Hour, "2 months from now"},
		{2 * 365 * 24 * time.Hour, "2 years from now"},
	}
	for _, c := range cases {
		got := base.Add(c.offset).DiffForHumans(base)
		if got != c.want {
			t.Errorf("offset %s: got %q, want %q", c.offset, got, c.want)
		}
	}
}

func TestDiffForHumans_Singular(t *testing.T) {
	base := Now()
	got := base.Add(-1 * time.Minute).DiffForHumans(base)
	if got != "1 minute ago" {
		t.Fatalf("singular = %q", got)
	}
}

func TestQueries_TodayPastFuture(t *testing.T) {
	if !Now().IsToday() {
		t.Fatal("Now() must be today")
	}
	if !Now().Add(-time.Hour).IsPast() {
		t.Fatal("past must be past")
	}
	if !Now().Add(time.Hour).IsFuture() {
		t.Fatal("future must be future")
	}
}

func TestFormatHelpers(t *testing.T) {
	c := MustParse("2026-05-30T12:34:56Z")
	if !strings.HasPrefix(c.ISO8601(), "2026-05-30T12:34:56") {
		t.Fatalf("ISO8601 = %s", c.ISO8601())
	}
	if c.Date() != "2026-05-30" {
		t.Fatalf("Date = %s", c.Date())
	}
	if c.DateTime() != "2026-05-30 12:34:56" {
		t.Fatalf("DateTime = %s", c.DateTime())
	}
}

func TestFrom_Roundtrips(t *testing.T) {
	tm := time.Now()
	if !From(tm).Time().Equal(tm) {
		t.Fatal("From -> Time must roundtrip")
	}
}

func TestEqualBeforeAfter(t *testing.T) {
	a := MustParse("2026-05-30")
	b := MustParse("2026-05-30")
	c := MustParse("2026-05-31")
	if !a.Equal(b) {
		t.Fatal("a == b")
	}
	if !a.Before(c) {
		t.Fatal("a < c")
	}
	if !c.After(a) {
		t.Fatal("c > a")
	}
}
