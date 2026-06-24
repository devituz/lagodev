package openapi

import (
	"strconv"
	"strings"
)

// validateRules holds the parsed validate-tag rules of a single struct field,
// keyed by rule name to its argument (empty for bare rules like "required").
type validateRules map[string]string

func (r validateRules) has(name string) bool {
	_, ok := r[name]
	return ok
}

// parseValidate splits a `validate:"required,email,max=255"` tag into a rule
// table. It mirrors validation.splitRules' comma split (regex= is treated as a
// terminal rule whose value runs to the end of the tag).
func parseValidate(tag string) validateRules {
	out := validateRules{}
	if tag == "" {
		return out
	}
	parts := strings.Split(tag, ",")
	for i := 0; i < len(parts); i++ {
		rule := strings.TrimSpace(parts[i])
		if rule == "" {
			continue
		}
		if strings.HasPrefix(rule, "regex=") {
			// regex value may contain commas; take the rest verbatim.
			rule = strings.Join(parts[i:], ",")
			out["regex"] = strings.TrimPrefix(rule, "regex=")
			break
		}
		name, arg, _ := strings.Cut(rule, "=")
		out[strings.TrimSpace(name)] = strings.TrimSpace(arg)
	}
	return out
}

// applyValidate maps validation rules onto JSON Schema keywords on s. It covers
// the rules that have a schema-level meaning: formats (email/url/uuid/datetime),
// numeric bounds vs string length (min/max/len, gte/lte), enums (in=), and
// pattern (regex=). "required" is handled by the caller at the object level.
//
// Whether min/max constrain length or magnitude depends on the schema type,
// matching validation's own numericLen behaviour: strings/arrays use length,
// numbers use value.
func applyValidate(s *Schema, r validateRules) {
	if s == nil {
		return
	}
	isString := s.hasType("string")
	isNumber := s.hasType("integer") || s.hasType("number")
	isArray := s.hasType("array")

	for name, arg := range r {
		switch name {
		case "email":
			if isString {
				s.Format = "email"
			}
		case "url":
			if isString {
				s.Format = "uri"
			}
		case "uuid":
			if isString {
				s.Format = "uuid"
			}
		case "datetime":
			if isString {
				s.Format = "date-time"
			}
		case "alpha":
			if isString && s.Pattern == "" {
				s.Pattern = "^[A-Za-z]+$"
			}
		case "alphanumeric":
			if isString && s.Pattern == "" {
				s.Pattern = "^[A-Za-z0-9]+$"
			}
		case "regex":
			if isString {
				s.Pattern = arg
			}
		case "min":
			applyBound(s, arg, true, isString, isNumber, isArray)
		case "max":
			applyBound(s, arg, false, isString, isNumber, isArray)
		case "len":
			applyBound(s, arg, true, isString, isNumber, isArray)
			applyBound(s, arg, false, isString, isNumber, isArray)
		case "gte":
			if n, err := strconv.ParseFloat(arg, 64); err == nil {
				s.Minimum = &n
			}
		case "lte":
			if n, err := strconv.ParseFloat(arg, 64); err == nil {
				s.Maximum = &n
			}
		case "gt":
			if n, err := strconv.ParseFloat(arg, 64); err == nil {
				v := n
				s.Minimum = &v
				s.ExclusiveMinimum = true
			}
		case "lt":
			if n, err := strconv.ParseFloat(arg, 64); err == nil {
				v := n
				s.Maximum = &v
				s.ExclusiveMaximum = true
			}
		case "in":
			s.Enum = enumValues(arg, isNumber)
		}
	}
}

// applyBound routes a min/max argument to length (string/array) or magnitude
// (number) keywords, matching how validation interprets the same rule.
func applyBound(s *Schema, arg string, isMin, isString, isNumber, isArray bool) {
	if isString || isArray {
		n, err := strconv.Atoi(arg)
		if err != nil {
			return
		}
		v := n
		if isString {
			if isMin {
				s.MinLength = &v
			} else {
				s.MaxLength = &v
			}
		} else { // array
			if isMin {
				s.MinItems = &v
			} else {
				s.MaxItems = &v
			}
		}
		return
	}
	if isNumber {
		n, err := strconv.ParseFloat(arg, 64)
		if err != nil {
			return
		}
		v := n
		if isMin {
			s.Minimum = &v
		} else {
			s.Maximum = &v
		}
	}
}

// enumValues splits an in= argument ("admin|user|guest") into enum members.
// validation's in rule separates choices with "|". When the field is numeric,
// values are parsed as numbers so the enum matches the JSON type.
func enumValues(arg string, numeric bool) []any {
	if arg == "" {
		return nil
	}
	toks := strings.Split(arg, "|")
	out := make([]any, 0, len(toks))
	for _, tok := range toks {
		tok = strings.TrimSpace(tok)
		if numeric {
			if n, err := strconv.ParseFloat(tok, 64); err == nil {
				out = append(out, n)
				continue
			}
		}
		out = append(out, tok)
	}
	return out
}
