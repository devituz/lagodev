package validation

import (
	"reflect"
	"strings"
)

// Rules maps a field name to a list of rule expressions, e.g.
//
//	Rules{"email": {"required", "email"}, "age": {"integer", "gte=18"}}
//
// It is the programmatic counterpart to struct `validate:"..."` tags and is
// used by Map for validating decoded JSON without a struct.
type Rules map[string][]string

// Messages overrides the default message for a given "field.rule" or "rule"
// key. The most specific key wins: "email.required" beats "required".
//
//	Messages{"email.required": "We need your email", "required": "is mandatory"}
type Messages map[string]string

// fieldName resolves the reported name of a struct field: the json tag if set
// (and not "-"), otherwise the Go field name.
func fieldName(sf reflect.StructField) string {
	if tag := sf.Tag.Get("json"); tag != "" && tag != "-" {
		if i := strings.IndexByte(tag, ','); i >= 0 {
			tag = tag[:i]
		}
		if tag != "" {
			return tag
		}
	}
	return sf.Name
}

// Validate reflects over the struct v (or pointer to struct) and applies the
// rules declared in each field's `validate:"..."` tag. It returns nil when all
// fields pass, or a ValidationErrors otherwise.
//
// Nested structs and slices of structs tagged `validate:"dive"` are validated
// recursively; nested errors are reported with dotted/indexed paths such as
// "address.city" or "items.0.sku".
//
// Field names in errors use the json tag when present, else the Go field name.
func Validate(v any) error {
	return ValidateWith(v, nil)
}

// ValidateWith is Validate with per-rule message overrides. See Messages.
func ValidateWith(v any, msgs Messages) error {
	errs := ValidationErrors{}
	validateStruct(reflect.ValueOf(v), "", errs, msgs)
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// validateStruct walks one struct value, prefixing reported field names with
// prefix (for nested dives). Unexported and untagged fields are skipped.
func validateStruct(v reflect.Value, prefix string, errs ValidationErrors, msgs Messages) {
	v = deref(v)
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()

	// Build the sibling map up front so cross-field rules can resolve peers by
	// their reported name regardless of declaration order.
	siblings := make(map[string]reflect.Value, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if sf := t.Field(i); sf.IsExported() {
			siblings[fieldName(sf)] = v.Field(i)
		}
	}

	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("validate")
		if tag == "" {
			continue
		}
		name := fieldName(sf)
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		fv := v.Field(i)

		for _, rule := range splitRules(tag) {
			if rule == "" {
				continue
			}
			if rule == "dive" {
				dive(fv, full, errs, msgs)
				continue
			}
			ruleName, arg := parseRule(rule)
			fn, ok := registry[ruleName]
			if !ok {
				continue
			}
			f := field{name: name, value: fv, siblings: siblings}
			if msg := fn(f, arg); msg != "" {
				errs.Add(full, resolveMessage(msgs, name, ruleName, msg))
			}
		}
	}
}

// dive validates the elements of a struct, slice/array of structs, or pointer
// to struct, recursing into validateStruct with an indexed/dotted prefix.
func dive(fv reflect.Value, prefix string, errs ValidationErrors, msgs Messages) {
	fv = deref(fv)
	switch fv.Kind() {
	case reflect.Struct:
		validateStruct(fv, prefix, errs, msgs)
	case reflect.Slice, reflect.Array:
		for i := 0; i < fv.Len(); i++ {
			el := deref(fv.Index(i))
			if el.Kind() == reflect.Struct {
				validateStruct(el, prefix+"."+itoa(i), errs, msgs)
			}
		}
	}
}

// Map validates a map[string]any (e.g. decoded JSON) against a Rules table.
// Cross-field rules (eqfield/nefield/confirmed) resolve against other keys in
// the same map. Returns nil or a ValidationErrors.
func Map(data map[string]any, rules Rules) error {
	return MapWith(data, rules, nil)
}

// MapWith is Map with per-rule message overrides.
func MapWith(data map[string]any, rules Rules, msgs Messages) error {
	errs := ValidationErrors{}

	siblings := make(map[string]reflect.Value, len(data))
	for k, val := range data {
		siblings[k] = reflect.ValueOf(val)
	}

	for name, exprs := range rules {
		raw, present := data[name]
		fv := reflect.ValueOf(raw)
		_ = present // absent keys read as an invalid/zero Value, handled by isEmpty

		for _, rule := range exprs {
			rule = strings.TrimSpace(rule)
			if rule == "" {
				continue
			}
			ruleName, arg := parseRule(rule)
			fn, ok := registry[ruleName]
			if !ok {
				continue
			}
			f := field{name: name, value: fv, siblings: siblings}
			if msg := fn(f, arg); msg != "" {
				errs.Add(name, resolveMessage(msgs, name, ruleName, msg))
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// resolveMessage returns the overridden message for "field.rule" or "rule" if
// present in msgs, otherwise the default.
func resolveMessage(msgs Messages, field, rule, def string) string {
	if msgs == nil {
		return def
	}
	if m, ok := msgs[field+"."+rule]; ok {
		return m
	}
	if m, ok := msgs[rule]; ok {
		return m
	}
	return def
}

// itoa is a tiny strconv.Itoa to avoid importing strconv solely for indices.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
