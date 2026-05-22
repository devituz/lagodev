package lagogin

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// ValidationError is the error type respond() recognizes and maps to 422.
// The Fields map is keyed by JSON field name (or struct field name when no
// `json:"..."` tag is present); each value is a human-readable message.
type ValidationError struct {
	Message string
	Fields  map[string]string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if e == nil || len(e.Fields) == 0 {
		return "validation failed"
	}
	parts := make([]string, 0, len(e.Fields))
	for k, v := range e.Fields {
		parts = append(parts, k+": "+v)
	}
	return e.Message + " (" + strings.Join(parts, "; ") + ")"
}

// Validate inspects struct tags on dst and runs lightweight validators
// against each field. Supported tag form:
//
//	`validate:"required,min=3,max=200,email,oneof=admin user"`
//
// Supported rules:
//
//	required             — disallow zero values (including empty strings)
//	min=N                — int/float ≥ N, string length ≥ N, slice len ≥ N
//	max=N                — opposite of min
//	email                — basic RFC-ish email shape
//	url                  — must start with http:// or https://
//	oneof=a b c          — value must equal one of the listed tokens
//	alpha                — letters only
//	alphanumeric         — letters and digits only
//	uuid                 — 8-4-4-4-12 hex pattern
//
// On failure Validate returns *ValidationError; respond() maps it to a
// 422 Unprocessable Entity with `{"errors": {...}}`.
func Validate(dst any) error {
	v := reflect.ValueOf(dst)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	errs := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("validate")
		if tag == "" {
			continue
		}
		name := jsonName(sf)
		fv := v.Field(i)
		for _, rule := range strings.Split(tag, ",") {
			rule = strings.TrimSpace(rule)
			if rule == "" {
				continue
			}
			if msg := applyRule(rule, fv); msg != "" {
				errs[name] = msg
				break
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Message: "validation failed", Fields: errs}
}

// BindAndValidate combines Bind() and Validate(). Returns the same
// *ValidationError on rule failure (mapped to 422 by respond), or the
// underlying decoder error on malformed JSON (handled as 400 by Bind).
func (c *Ctx) BindAndValidate(dst any) error {
	if err := c.Bind(dst); err != nil {
		return err
	}
	return Validate(dst)
}

func jsonName(sf reflect.StructField) string {
	if tag := sf.Tag.Get("json"); tag != "" && tag != "-" {
		if i := strings.Index(tag, ","); i >= 0 {
			return tag[:i]
		}
		return tag
	}
	return sf.Name
}

func applyRule(rule string, v reflect.Value) string {
	name, arg, _ := strings.Cut(rule, "=")
	name = strings.TrimSpace(name)
	arg = strings.TrimSpace(arg)
	switch name {
	case "required":
		if isZero(v) {
			return "is required"
		}
	case "min":
		n, _ := strconv.Atoi(arg)
		if !meetsMin(v, n) {
			return fmt.Sprintf("must be at least %d", n)
		}
	case "max":
		n, _ := strconv.Atoi(arg)
		if !meetsMax(v, n) {
			return fmt.Sprintf("must be at most %d", n)
		}
	case "email":
		if v.Kind() == reflect.String && !emailRE.MatchString(v.String()) {
			return "must be a valid email"
		}
	case "url":
		if v.Kind() == reflect.String {
			s := v.String()
			if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
				return "must be a valid URL"
			}
		}
	case "oneof":
		if v.Kind() == reflect.String {
			s := v.String()
			for _, tok := range strings.Fields(arg) {
				if tok == s {
					return ""
				}
			}
			return "must be one of: " + arg
		}
	case "alpha":
		if v.Kind() == reflect.String && !alphaRE.MatchString(v.String()) {
			return "must contain only letters"
		}
	case "alphanumeric":
		if v.Kind() == reflect.String && !alphanumRE.MatchString(v.String()) {
			return "must contain only letters and digits"
		}
	case "uuid":
		if v.Kind() == reflect.String && !uuidRE.MatchString(v.String()) {
			return "must be a valid UUID"
		}
	}
	return ""
}

func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return strings.TrimSpace(v.String()) == ""
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() == 0
	}
	return v.IsZero()
}

func meetsMin(v reflect.Value, n int) bool {
	switch v.Kind() {
	case reflect.String:
		return len(v.String()) >= n
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() >= n
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() >= int64(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() >= uint64(n)
	case reflect.Float32, reflect.Float64:
		return v.Float() >= float64(n)
	}
	return true
}

func meetsMax(v reflect.Value, n int) bool {
	switch v.Kind() {
	case reflect.String:
		return len(v.String()) <= n
	case reflect.Slice, reflect.Map, reflect.Array:
		return v.Len() <= n
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() <= int64(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() <= uint64(n)
	case reflect.Float32, reflect.Float64:
		return v.Float() <= float64(n)
	}
	return true
}

var (
	emailRE    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	alphaRE    = regexp.MustCompile(`^[A-Za-z]+$`)
	alphanumRE = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)
