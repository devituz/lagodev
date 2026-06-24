package validation

import (
	"reflect"
	"testing"
)

// runRule applies a single rule to a value via the map path and reports whether
// it produced an error message.
func runRule(t *testing.T, rule string, value any) string {
	t.Helper()
	err := Map(map[string]any{"f": value}, Rules{"f": {rule}})
	if err == nil {
		return ""
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	return ve.First("f")
}

func TestRequired(t *testing.T) {
	cases := []struct {
		name  string
		value any
		fail  bool
	}{
		{"empty string", "", true},
		{"whitespace", "   ", true},
		{"nil", nil, true},
		{"zero int", 0, true},
		{"empty slice", []int{}, true},
		{"non-empty string", "x", false},
		{"non-zero int", 5, false},
		{"slice with item", []int{1}, false},
		{"false bool is empty", false, true},
		{"true bool", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runRule(t, "required", c.value)
			if (got != "") != c.fail {
				t.Fatalf("required(%v): got %q, fail=%v", c.value, got, c.fail)
			}
		})
	}
}

func TestEmail(t *testing.T) {
	cases := []struct {
		value string
		fail  bool
	}{
		{"user@example.com", false},
		{"a.b+c@sub.example.co", false},
		{"", false}, // empty passes; combine with required
		{"notanemail", true},
		{"@example.com", true},
		{"user@", true},
		{"user example.com", true},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			got := runRule(t, "email", c.value)
			if (got != "") != c.fail {
				t.Fatalf("email(%q): got %q, fail=%v", c.value, got, c.fail)
			}
		})
	}
}

func TestURL(t *testing.T) {
	cases := []struct {
		value string
		fail  bool
	}{
		{"https://example.com", false},
		{"http://a.b/c?d=e", false},
		{"", false},
		{"example.com", true},
		{"ftp://", true},
		{"://nohost", true},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			got := runRule(t, "url", c.value)
			if (got != "") != c.fail {
				t.Fatalf("url(%q): got %q, fail=%v", c.value, got, c.fail)
			}
		})
	}
}

func TestMinMaxLen(t *testing.T) {
	cases := []struct {
		rule  string
		value any
		fail  bool
	}{
		{"min=3", "abc", false},
		{"min=3", "ab", true},
		{"min=3", 5, false},
		{"min=3", 2, true},
		{"min=2", []int{1, 2}, false},
		{"min=2", []int{1}, true},
		{"max=5", "hello", false},
		{"max=5", "helloo", true},
		{"max=10", 11, true},
		{"max=10", 9, false},
		{"len=3", "abc", false},
		{"len=3", "abcd", true},
		{"len=3", 3, false},
		{"len=3", 4, true},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%v): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestNumericComparisons(t *testing.T) {
	cases := []struct {
		rule  string
		value any
		fail  bool
	}{
		{"gte=18", 18, false},
		{"gte=18", 17, true},
		{"lte=120", 120, false},
		{"lte=120", 121, true},
		{"gt=0", 1, false},
		{"gt=0", 0, true},
		{"lt=10", 9, false},
		{"lt=10", 10, true},
		{"gte=2.5", 3.0, false},
		{"gte=2.5", 2.0, true},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%v): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestNumericInteger(t *testing.T) {
	cases := []struct {
		rule  string
		value string
		fail  bool
	}{
		{"numeric", "3.14", false},
		{"numeric", "42", false},
		{"numeric", "abc", true},
		{"integer", "42", false},
		{"integer", "3.14", true},
		{"integer", "-7", false},
		{"integer", "x", true},
	}
	for _, c := range cases {
		t.Run(c.rule+"/"+c.value, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%q): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestAlphaAlphanumUUID(t *testing.T) {
	cases := []struct {
		rule  string
		value string
		fail  bool
	}{
		{"alpha", "abcDEF", false},
		{"alpha", "abc1", true},
		{"alphanumeric", "abc123", false},
		{"alphanumeric", "abc 123", true},
		{"uuid", "550e8400-e29b-41d4-a716-446655440000", false},
		{"uuid", "not-a-uuid", true},
	}
	for _, c := range cases {
		t.Run(c.rule+"/"+c.value, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%q): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestBoolean(t *testing.T) {
	cases := []struct {
		value any
		fail  bool
	}{
		{true, false},
		{false, false},
		{"true", false},
		{"0", false},
		{"1", false},
		{"yes", true},
		{"maybe", true},
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			got := runRule(t, "boolean", c.value)
			if (got != "") != c.fail {
				t.Fatalf("boolean(%v): got %q, fail=%v", c.value, got, c.fail)
			}
		})
	}
}

func TestInNotIn(t *testing.T) {
	cases := []struct {
		rule  string
		value string
		fail  bool
	}{
		{"in=admin|user|guest", "user", false},
		{"in=admin|user|guest", "root", true},
		{"notin=root|system", "user", false},
		{"notin=root|system", "root", true},
	}
	for _, c := range cases {
		t.Run(c.rule+"/"+c.value, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%q): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestRegex(t *testing.T) {
	cases := []struct {
		rule  string
		value string
		fail  bool
	}{
		{`regex=^[a-z]+$`, "abc", false},
		{`regex=^[a-z]+$`, "Abc", true},
		{`regex=^\d{3}-\d{4}$`, "123-4567", false},
		{`regex=^\d{3}-\d{4}$`, "12-4567", true},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%q): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestDatetime(t *testing.T) {
	cases := []struct {
		rule  string
		value string
		fail  bool
	}{
		{"datetime=2006-01-02", "2024-01-15", false},
		{"datetime=2006-01-02", "15/01/2024", true},
		{"datetime", "2024-01-15T10:30:00Z", false}, // RFC3339 default
		{"datetime", "nope", true},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			got := runRule(t, c.rule, c.value)
			if (got != "") != c.fail {
				t.Fatalf("%s(%q): got %q, fail=%v", c.rule, c.value, got, c.fail)
			}
		})
	}
}

func TestSplitRulesPreservesRegexCommas(t *testing.T) {
	// regex must be last; it swallows the rest of the tag (commas and all).
	got := splitRules(`required,max=20,regex=^\d{3},\d{4}$`)
	want := []string{"required", "max=20", `regex=^\d{3},\d{4}$`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitRules: got %#v, want %#v", got, want)
	}
}
