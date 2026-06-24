package validation

// Builder is a fluent helper for constructing a Rules table programmatically
// without writing the raw string slices by hand:
//
//	rules := validation.New().
//	    Field("email", "required", "email").
//	    Field("age", "integer", "gte=18").
//	    Rules()
//	err := validation.Map(data, rules)
//
// It is purely sugar over the Rules map; both forms validate identically.
type Builder struct {
	rules Rules
}

// New returns an empty Builder.
func New() *Builder {
	return &Builder{rules: Rules{}}
}

// Field adds (or appends to) the rule list for name and returns the Builder for
// chaining. Calling Field twice for the same name appends rules.
func (b *Builder) Field(name string, rules ...string) *Builder {
	b.rules[name] = append(b.rules[name], rules...)
	return b
}

// Rules returns the accumulated Rules table.
func (b *Builder) Rules() Rules {
	return b.rules
}

// Validate runs the accumulated rules against data, equivalent to
// Map(data, b.Rules()).
func (b *Builder) Validate(data map[string]any) error {
	return Map(data, b.rules)
}
