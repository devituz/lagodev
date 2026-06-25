package validation

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// allBuiltinRules lists every registered rule together with a representative
// argument (empty where the rule takes none). Used to blast every rule with
// adversarial inputs and assert none panic.
var allBuiltinRules = []struct {
	name string
	arg  string
}{
	{"required", ""},
	{"email", ""},
	{"url", ""},
	{"min", "3"},
	{"max", "5"},
	{"len", "4"},
	{"gte", "10"},
	{"lte", "10"},
	{"gt", "10"},
	{"lt", "10"},
	{"numeric", ""},
	{"integer", ""},
	{"alpha", ""},
	{"alphanumeric", ""},
	{"uuid", ""},
	{"boolean", ""},
	{"in", "a|b|c"},
	{"notin", "a|b|c"},
	{"regex", "^[a-z]+$"},
	{"datetime", time.RFC3339},
	{"eqfield", "other"},
	{"nefield", "other"},
	{"confirmed", ""},
}

// adversarialValues is a broad set of hostile inputs covering empty/nil,
// wrong-type, extreme numbers, very long strings, and unicode/RTL.
func adversarialValues() []any {
	long := strings.Repeat("x", 1<<16) // 64 KiB string
	type weird struct{ X int }
	return []any{
		nil,
		"",
		"   ",
		"abc",
		"ABC123",
		"00000000-0000-0000-0000-000000000000",
		"not-a-uuid",
		"user@example.com",
		"@@bad@@",
		"https://example.com/path?q=1",
		"ht!tp://[::bad",
		"true", "false", "0", "1", "maybe",
		"2026-06-25T00:00:00Z",
		"2026-13-99",
		int(0), int(-1), int(1 << 62),
		int8(-128), int16(0), int32(1 << 30), int64(-1 << 62),
		uint(0), uint8(255), uint64(1 << 63),
		float64(0), float64(-1e308), float64(1e308),
		float32(3.14),
		true, false,
		[]int{}, []int{1, 2, 3},
		[]string{"a", "b"},
		map[string]int{}, map[string]int{"k": 1},
		[3]int{1, 2, 3},
		long,
		"السلام عليكم", // Arabic / RTL
		"‮evil‬",       // RTL override embedded
		"日本語テスト",       // CJK
		"emoji 🚀🔥💥",    // multibyte emoji
		"\x00\x01\x02null bytes",
		weird{X: 1},
		&weird{X: 2},
		(*weird)(nil),
		complex(1, 2),
	}
}

// TestEveryRuleNoPanic runs every built-in rule against every adversarial value
// and asserts none panic. The result (pass/fail) is irrelevant here; survival is.
func TestEveryRuleNoPanic(t *testing.T) {
	values := adversarialValues()
	for _, r := range allBuiltinRules {
		for _, v := range values {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("rule %q arg %q panicked on %#v: %v", r.name, r.arg, v, rec)
					}
				}()
				expr := r.name
				if r.arg != "" {
					expr += "=" + r.arg
				}
				// siblings present so cross-field rules have peers to resolve.
				_ = Map(map[string]any{
					"f":              v,
					"other":          v,
					"f_confirmation": v,
				}, Rules{"f": {expr}})
			}()
		}
	}
}

// TestRulePassFailSane spot-checks that representative rules return the expected
// pass/fail on clean inputs, guarding against a rule silently always-passing.
func TestRulePassFailSane(t *testing.T) {
	cases := []struct {
		rule  string
		value any
		fail  bool
	}{
		{"email", "user@example.com", false},
		{"email", "not-an-email", true},
		{"url", "https://x.io", false},
		{"url", "://nope", true},
		{"uuid", "00000000-0000-0000-0000-000000000000", false},
		{"uuid", "deadbeef", true},
		{"integer", "42", false},
		{"integer", "4.2", true},
		{"numeric", "4.2", false},
		{"numeric", "abc", true},
		{"alpha", "abcXYZ", false},
		{"alpha", "abc1", true},
		{"alphanumeric", "abc123", false},
		{"alphanumeric", "abc-123", true},
		{"in=a|b|c", "b", false},
		{"in=a|b|c", "z", true},
		{"notin=a|b|c", "z", false},
		{"notin=a|b|c", "a", true},
		{"min=3", "ab", true},
		{"min=3", "abc", false},
		{"max=3", "abcd", true},
		{"max=3", "abc", false},
		{"len=3", "abc", false},
		{"len=3", "ab", true},
		{"gte=18", 17, true},
		{"gte=18", 18, false},
		{"lt=10", 10, true},
		{"lt=10", 9, false},
		{"boolean", "maybe", true},
		{"boolean", "true", false},
		{"regex=^[0-9]+$", "12345", false},
		{"regex=^[0-9]+$", "12a45", true},
		{"datetime=2006-01-02", "2026-06-25", false},
		{"datetime=2006-01-02", "25-06-2026", true},
	}
	for _, c := range cases {
		got := runRule(t, c.rule, c.value)
		if (got != "") != c.fail {
			t.Errorf("rule %q value %#v: got msg %q, wantFail=%v", c.rule, c.value, got, c.fail)
		}
	}
}

// TestRegexBoundedTime asserts that a pathological pattern + input that would
// trigger catastrophic backtracking in a PCRE engine stays bounded. Go's
// regexp is RE2 (no backtracking) so this is linear, but we assert it anyway as
// a ReDoS regression guard.
func TestRegexBoundedTime(t *testing.T) {
	// Classic ReDoS pattern: (a+)+$ against a long non-matching input.
	pattern := `^(a+)+$`
	input := strings.Repeat("a", 5000) + "!"
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Map(map[string]any{"f": input}, Rules{"f": {"regex=" + pattern}})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("regex %q took >2s on adversarial input (possible ReDoS)", pattern)
	}
}

// TestRegexCacheConcurrent exercises the regex cache from many goroutines. With
// the unsynchronised map this raced (and could panic with concurrent map
// writes); under -race it must now be clean.
func TestRegexCacheConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			pat := "regex=^[a-z" + string(rune('a'+n%5)) + "]+$"
			for j := 0; j < 50; j++ {
				_ = Map(map[string]any{"f": "abcde"}, Rules{"f": {pat}})
			}
		}(i)
	}
	wg.Wait()
}

// TestMalformedTagsNoPanic feeds garbage struct-tag rule strings and asserts the
// struct path never panics: unknown rules are skipped, malformed args error
// cleanly (rule returns "" or a message), nothing crashes.
func TestMalformedTagsNoPanic(t *testing.T) {
	tags := []string{
		"",
		",",
		",,,,",
		"=",
		"==",
		"unknownrule",
		"unknownrule=foo",
		"min",            // missing arg
		"min=",           // empty arg
		"min=notanumber", // non-numeric arg
		"max=1=2=3",      // extra =
		"gte=",
		"gte=abc",
		"in=",         // empty choices
		"in=|||",      // only separators
		"regex=[",     // invalid regex
		"regex=(a+)+", // valid but heavy
		"datetime=garbageformat",
		"required,,email",
		"required,unknown,email",
		"  required  ,  email  ", // whitespace
		"eqfield=", "nefield=", "confirmed",
		strings.Repeat("required,", 100), // many rules
	}
	for _, tag := range tags {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("tag %q panicked: %v", tag, rec)
				}
			}()
			// Build a struct value dynamically is awkward; instead drive the
			// same parse + dispatch path through Map using split rules.
			var rules []string
			for _, r := range splitRules(tag) {
				rules = append(rules, r)
			}
			_ = Map(map[string]any{"f": "value", "f_confirmation": "value"}, Rules{"f": rules})
		}()
	}
}

// TestStructTagPathMalformed drives the actual struct reflection path with
// hostile tags via a generated type set, ensuring validateStruct itself is
// panic-safe.
func TestStructTagPathMalformed(t *testing.T) {
	type T1 struct {
		F string `json:"f" validate:""`
	}
	type T2 struct {
		F string `json:"f" validate:",,,"`
	}
	type T3 struct {
		F string `json:"f" validate:"unknown=x,min=notnum,max="`
	}
	type T4 struct {
		F string `json:"f" validate:"regex=["`
	}
	type T5 struct {
		// nil pointer field with rules
		P *string `json:"p" validate:"required,min=3"`
	}
	type T6 struct {
		// dive on a non-divable scalar
		N int `json:"n" validate:"dive"`
	}
	cases := []any{
		T1{}, &T1{},
		T2{}, T3{}, T4{},
		T5{}, T6{},
		nil, "scalar", 42, []int{1, 2}, map[string]int{"a": 1},
	}
	for _, c := range cases {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("Validate(%#v) panicked: %v", c, rec)
				}
			}()
			_ = Validate(c)
		}()
	}
}

// TestNestedDepthAndNilPointers exercises nested structs, slices of structs,
// pointer fields (nil and set), and maps, asserting no panic and correct
// dotted/indexed error paths.
func TestNestedDepthAndNilPointers(t *testing.T) {
	type inner struct {
		Name string `json:"name" validate:"required"`
	}
	type mid struct {
		Inner    inner   `json:"inner" validate:"dive"`
		InnerPtr *inner  `json:"inner_ptr" validate:"dive"` // nil pointer dive
		List     []inner `json:"list" validate:"dive"`
	}
	type outer struct {
		Mid mid `json:"mid" validate:"dive"`
	}

	// nil InnerPtr must not panic; List elements report indexed paths.
	v := outer{Mid: mid{
		Inner: inner{Name: ""},           // fails
		List:  []inner{{Name: "ok"}, {}}, // index 1 fails
	}}
	err := Validate(&v)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if !ve.Has("mid.inner.name") {
		t.Errorf("missing mid.inner.name error; got %v", ve.Fields())
	}
	if !ve.Has("mid.list.1.name") {
		t.Errorf("missing mid.list.1.name error; got %v", ve.Fields())
	}

	// Deeply nested pointer chain, all nil — must not panic.
	type rec struct {
		Next *rec   `json:"next" validate:"dive"`
		Val  string `json:"val" validate:"required"`
	}
	deep := &rec{Val: "x", Next: &rec{Val: "y", Next: &rec{Val: ""}}}
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("deep nested validate panicked: %v", r)
			}
		}()
		_ = Validate(deep)
	}()
}

// FuzzValidateMap fuzzes field values (as strings) and a rule expression,
// asserting Map never panics for any combination.
func FuzzValidateMap(f *testing.F) {
	seeds := []struct {
		val  string
		rule string
	}{
		{"", "required"},
		{"user@x.com", "email"},
		{"http://x", "url"},
		{"abc", "min=2"},
		{"12345", "regex=^[0-9]+$"},
		{"2026-06-25", "datetime=2006-01-02"},
		{"السلام", "alpha"},
		{strings.Repeat("a", 10000), "max=5"},
		{"\x00\xff", "boolean"},
		{"a|b", "in=a|b|c"},
	}
	for _, s := range seeds {
		f.Add(s.val, s.rule)
	}
	f.Fuzz(func(t *testing.T, val, rule string) {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("Map panicked: val=%q rule=%q: %v", val, rule, rec)
			}
		}()
		_ = Map(map[string]any{
			"f":              val,
			"other":          val,
			"f_confirmation": val,
		}, Rules{"f": {rule}})
	})
}

// FuzzStructTag fuzzes the tag string itself through the split/parse/dispatch
// path, asserting no panic for arbitrary tag content.
func FuzzStructTag(f *testing.F) {
	seeds := []string{
		"required,email",
		"min=3,max=255",
		"regex=^[a-z]+$",
		"in=a|b|c",
		"gte=18,lte=120",
		",,,=,==,",
		"unknown=foo,bar=baz",
		"datetime=2006-01-02",
		"regex=(((((",
		strings.Repeat("min=1,", 50),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tag string) {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("tag dispatch panicked: tag=%q: %v", tag, rec)
			}
		}()
		rules := splitRules(tag)
		siblings := map[string]reflect.Value{
			"other":          reflect.ValueOf("x"),
			"f_confirmation": reflect.ValueOf("x"),
		}
		// Drive through the dispatch path identical to validateStruct/Map.
		for _, r := range rules {
			if r == "" || r == "dive" {
				continue
			}
			name, arg := parseRule(r)
			if fn, ok := registry[name]; ok {
				fld := field{
					name:     "f",
					value:    reflect.ValueOf("sample"),
					siblings: siblings,
				}
				_ = fn(fld, arg)
			}
		}
	})
}
