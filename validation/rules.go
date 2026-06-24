package validation

import (
	"fmt"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// field carries the value under validation plus the data needed by cross-field
// rules (the sibling values keyed by their reported field name) so that rules
// like eqfield/confirmed can look up other fields.
type field struct {
	name     string                   // reported field name (json tag or struct name)
	value    reflect.Value            // the value under validation
	siblings map[string]reflect.Value // peer fields, keyed by reported name
}

// ruleFunc validates f and returns a human-readable message on failure, or ""
// on success. arg is the text after "=" in the rule (may be empty).
type ruleFunc func(f field, arg string) string

// registry is the built-in rule table. It is read-only after init.
var registry = map[string]ruleFunc{}

func register(name string, fn ruleFunc) { registry[name] = fn }

// parseRule splits "max=255" into ("max", "255"). A bare rule yields an empty
// arg. Only the first "=" is significant so regex= can carry "=" in its value.
func parseRule(rule string) (name, arg string) {
	name, arg, _ = strings.Cut(rule, "=")
	return strings.TrimSpace(name), strings.TrimSpace(arg)
}

// splitRules splits a comma-separated tag into individual rules. A regex=...
// rule is special: its argument may contain commas, so once a "regex=" token is
// reached at a rule boundary the remainder of the tag is taken verbatim as its
// argument. For this reason regex=... must be the LAST rule in a tag.
func splitRules(tag string) []string {
	var out []string
	var b strings.Builder
	for i := 0; i < len(tag); i++ {
		// At a rule boundary, a "regex=" token swallows the rest of the tag.
		if (i == 0 || tag[i-1] == ',') && strings.HasPrefix(tag[i:], "regex=") {
			if b.Len() > 0 {
				out = append(out, strings.TrimSpace(b.String()))
				b.Reset()
			}
			out = append(out, strings.TrimSpace(tag[i:]))
			return out
		}
		if tag[i] == ',' {
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteByte(tag[i])
	}
	if b.Len() > 0 {
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}

// ---------------------------------------------------------------------------
// reflect helpers
// ---------------------------------------------------------------------------

// deref unwraps pointers and interfaces to the concrete value.
func deref(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return v
		}
		v = v.Elem()
	}
	return v
}

// isEmpty reports whether v is "empty" for the required rule: empty/whitespace
// string, zero-length slice/map/array, nil pointer, or the type's zero value.
func isEmpty(v reflect.Value) bool {
	v = deref(v)
	switch v.Kind() {
	case reflect.String:
		return strings.TrimSpace(v.String()) == ""
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Invalid:
		return true
	}
	return v.IsZero()
}

// asString returns the string form of v when it is a string, plus ok.
func asString(v reflect.Value) (string, bool) {
	v = deref(v)
	if v.Kind() == reflect.String {
		return v.String(), true
	}
	return "", false
}

// numericLen returns a comparable "size": numeric value for numbers, element
// count for string/slice/map/array. The second result is false for unsupported
// kinds so size-based rules can skip them.
func numericLen(v reflect.Value) (float64, bool) {
	v = deref(v)
	switch v.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return float64(v.Len()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	}
	return 0, false
}

// equalValues compares two values for the cross-field eqfield/nefield rules.
// Strings and numbers compare by content; everything else by reflect.DeepEqual.
func equalValues(a, b reflect.Value) bool {
	a, b = deref(a), deref(b)
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if sa, ok := asString(a); ok {
		if sb, ok := asString(b); ok {
			return sa == sb
		}
	}
	na, oka := numericLen(a)
	nb, okb := numericLen(b)
	if oka && okb {
		return na == nb
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// ---------------------------------------------------------------------------
// shared regexes
// ---------------------------------------------------------------------------

var (
	alphaRE    = regexp.MustCompile(`^[A-Za-z]+$`)
	alphanumRE = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

	// regexCache memoises compiled user patterns from regex=... rules so the
	// same pattern is not recompiled per value.
	regexCache = map[string]*regexp.Regexp{}
)

func compileRegex(pattern string) (*regexp.Regexp, error) {
	if re, ok := regexCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache[pattern] = re
	return re, nil
}

// ---------------------------------------------------------------------------
// rule registration
// ---------------------------------------------------------------------------

func init() {
	register("required", func(f field, _ string) string {
		if isEmpty(f.value) {
			return "is required"
		}
		return ""
	})

	register("email", func(f field, _ string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		if _, err := mail.ParseAddress(s); err != nil {
			return "must be a valid email address"
		}
		return ""
	})

	register("url", func(f field, _ string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		u, err := url.ParseRequestURI(s)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "must be a valid URL"
		}
		return ""
	})

	register("min", func(f field, arg string) string {
		n, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return ""
		}
		size, ok := numericLen(f.value)
		if !ok || size >= n {
			return ""
		}
		return sizeMessage(f.value, "must be at least %s", "must be at least %s characters", arg)
	})

	register("max", func(f field, arg string) string {
		n, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return ""
		}
		size, ok := numericLen(f.value)
		if !ok || size <= n {
			return ""
		}
		return sizeMessage(f.value, "must not be greater than %s", "must not be greater than %s characters", arg)
	})

	register("len", func(f field, arg string) string {
		n, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return ""
		}
		size, ok := numericLen(f.value)
		if !ok || size == n {
			return ""
		}
		return sizeMessage(f.value, "must equal %s", "must be exactly %s characters", arg)
	})

	register("gte", numCompare(">=", func(a, b float64) bool { return a >= b }))
	register("lte", numCompare("<=", func(a, b float64) bool { return a <= b }))
	register("gt", numCompare(">", func(a, b float64) bool { return a > b }))
	register("lt", numCompare("<", func(a, b float64) bool { return a < b }))

	register("numeric", func(f field, _ string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return "must be a number"
		}
		return ""
	})

	register("integer", func(f field, _ string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "must be an integer"
		}
		return ""
	})

	register("alpha", reMatch(alphaRE, "must contain only letters"))
	register("alphanumeric", reMatch(alphanumRE, "must contain only letters and digits"))
	register("uuid", reMatch(uuidRE, "must be a valid UUID"))

	register("boolean", func(f field, _ string) string {
		v := deref(f.value)
		if v.Kind() == reflect.Bool {
			return ""
		}
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		switch strings.ToLower(s) {
		case "true", "false", "1", "0":
			return ""
		}
		return "must be true or false"
	})

	register("in", func(f field, arg string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		for _, tok := range strings.Split(arg, "|") {
			if tok == s {
				return ""
			}
		}
		return "must be one of: " + strings.ReplaceAll(arg, "|", ", ")
	})

	register("notin", func(f field, arg string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		for _, tok := range strings.Split(arg, "|") {
			if tok == s {
				return "must not be one of: " + strings.ReplaceAll(arg, "|", ", ")
			}
		}
		return ""
	})

	register("regex", func(f field, arg string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		re, err := compileRegex(arg)
		if err != nil {
			return "has an invalid format"
		}
		if !re.MatchString(s) {
			return "has an invalid format"
		}
		return ""
	})

	register("datetime", func(f field, arg string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		if arg == "" {
			arg = time.RFC3339
		}
		if _, err := time.Parse(arg, s); err != nil {
			return "must match the date format " + arg
		}
		return ""
	})

	// Cross-field rules.
	register("eqfield", func(f field, arg string) string {
		other, ok := f.siblings[arg]
		if !ok {
			return ""
		}
		if !equalValues(f.value, other) {
			return "must match " + arg
		}
		return ""
	})

	register("nefield", func(f field, arg string) string {
		other, ok := f.siblings[arg]
		if !ok {
			return ""
		}
		if equalValues(f.value, other) {
			return "must be different from " + arg
		}
		return ""
	})

	register("confirmed", func(f field, _ string) string {
		confName := f.name + "_confirmation"
		other, ok := f.siblings[confName]
		if !ok {
			// also try CamelCase fallback (FieldConfirmation)
			other, ok = f.siblings[f.name+"Confirmation"]
		}
		if !ok {
			return "confirmation does not match"
		}
		if !equalValues(f.value, other) {
			return "confirmation does not match"
		}
		return ""
	})
}

// numCompare builds a numeric comparison rule (gte/lte/gt/lt). Unlike
// min/max/len, these are always numeric: a numeric string ("20") compares by
// its parsed value, not its length. Non-numeric, non-sizable kinds pass.
func numCompare(sym string, ok func(a, b float64) bool) ruleFunc {
	return func(f field, arg string) string {
		n, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return ""
		}
		size, sizable := numericValue(f.value)
		if !sizable || ok(size, n) {
			return ""
		}
		return fmt.Sprintf("must be %s %s", sym, arg)
	}
}

// numericValue is like numericLen but, for strings, parses the string as a
// number instead of using its length. Used by the gte/lte/gt/lt rules where a
// numeric comparison is always intended.
func numericValue(v reflect.Value) (float64, bool) {
	v = deref(v)
	if v.Kind() == reflect.String {
		n, err := strconv.ParseFloat(strings.TrimSpace(v.String()), 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return numericLen(v)
}

// reMatch builds a rule that requires a string field to match re. Empty strings
// pass (use required to forbid them).
func reMatch(re *regexp.Regexp, msg string) ruleFunc {
	return func(f field, _ string) string {
		s, ok := asString(f.value)
		if !ok || s == "" {
			return ""
		}
		if !re.MatchString(s) {
			return msg
		}
		return ""
	}
}

// sizeMessage picks a numeric vs length-oriented message based on the kind of v.
func sizeMessage(v reflect.Value, numFmt, lenFmt, arg string) string {
	v = deref(v)
	switch v.Kind() {
	case reflect.String, reflect.Slice, reflect.Map, reflect.Array:
		return fmt.Sprintf(lenFmt, arg)
	default:
		return fmt.Sprintf(numFmt, arg)
	}
}
