// Package validation provides a dependency-free, Laravel-FormRequest-grade
// request validator for structs and maps.
//
// Two entry points cover most needs:
//
//   - Validate(v) walks a struct's `validate:"..."` tags and applies rules.
//   - Map(data, rules) validates a map[string]any (e.g. decoded JSON) against
//     a programmatic Rules table — no struct required.
//
// Field names in errors use the `json` tag when present, otherwise the struct
// field name.
//
// Struct example:
//
//	type RegisterRequest struct {
//	    Name                 string `json:"name" validate:"required,max=255"`
//	    Email                string `json:"email" validate:"required,email"`
//	    Age                  int    `json:"age" validate:"gte=18,lte=120"`
//	    Password             string `json:"password" validate:"required,min=8"`
//	    PasswordConfirmation string `json:"password_confirmation"`
//	    Role                 string `json:"role" validate:"in=admin|user|guest"`
//	}
//
//	var req RegisterRequest
//	if err := validation.Validate(&req); err != nil {
//	    var ve validation.ValidationErrors
//	    if errors.As(err, &ve) {
//	        // c.JSON(422, validation.Response(ve))
//	    }
//	}
//
// The "confirmed" rule pairs a field with "<Field>Confirmation" (or its json
// "<field>_confirmation"), matching Laravel's password/password_confirmation
// convention.
//
// Map example (no struct):
//
//	data := map[string]any{"email": "bad", "age": 5}
//	err := validation.Map(data, validation.Rules{
//	    "email": {"required", "email"},
//	    "age":   {"required", "integer", "gte=18"},
//	})
//
// Web integration. validation is a leaf package and never imports web, so to
// avoid an import cycle it exposes only the response *shape*. Callers wire it:
//
//	func (h *Handler) Store(c *web.Context) (any, error) {
//	    var req RegisterRequest
//	    if err := c.Bind(&req); err != nil {
//	        return nil, err
//	    }
//	    if err := validation.Validate(&req); err != nil {
//	        var ve validation.ValidationErrors
//	        if errors.As(err, &ve) {
//	            c.JSON(422, validation.Response(ve))
//	            return nil, nil
//	        }
//	        return nil, err
//	    }
//	    return req, nil
//	}
package validation

import (
	"sort"
	"strings"
)

// ValidationErrors maps a field name to the list of human-readable messages
// that failed for it. It implements the error interface, so it can flow through
// ordinary `error` return values and be recovered with errors.As.
type ValidationErrors map[string][]string

// Error implements the error interface. The message is deterministic (fields
// sorted) so it is stable in logs and tests.
func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "validation failed"
	}
	fields := e.Fields()
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, f+": "+strings.Join(e[f], ", "))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Has reports whether the given field has at least one error.
func (e ValidationErrors) Has(field string) bool {
	return len(e[field]) > 0
}

// First returns the first error message for field, or "" if there is none.
func (e ValidationErrors) First(field string) string {
	if msgs := e[field]; len(msgs) > 0 {
		return msgs[0]
	}
	return ""
}

// Fields returns the field names that have errors, sorted for determinism.
func (e ValidationErrors) Fields() []string {
	fields := make([]string, 0, len(e))
	for f := range e {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	return fields
}

// Add appends a message for field.
func (e ValidationErrors) Add(field, msg string) {
	e[field] = append(e[field], msg)
}

// ToMap returns the field -> messages map. It is a copy, safe for callers to
// mutate or serialise.
func (e ValidationErrors) ToMap() map[string][]string {
	out := make(map[string][]string, len(e))
	for f, msgs := range e {
		cp := make([]string, len(msgs))
		copy(cp, msgs)
		out[f] = cp
	}
	return out
}

// ErrorResponse is the JSON shape for a 422 Unprocessable Entity response.
// It is a plain struct with no web dependency, so any HTTP layer can emit it
// (e.g. c.JSON(422, validation.Response(ve))).
type ErrorResponse struct {
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
}

// Response builds an ErrorResponse from ValidationErrors, suitable for a 422
// body. The Message mirrors Laravel's default validation response message.
func Response(e ValidationErrors) ErrorResponse {
	return ErrorResponse{
		Message: "The given data was invalid.",
		Errors:  e.ToMap(),
	}
}
