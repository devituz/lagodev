package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/devituz/lagodev/internal/inflect"
)

// FieldSpec describes one field of a model. It is parsed from the CLI's
// --fields flag — e.g. `--fields=name:string,email:string:unique,age:int`.
type FieldSpec struct {
	// Column is the SQL column name (snake_case). This is the canonical
	// name — the model field name is computed from it (PascalCase).
	Column string
	// Type is the portable type name (string, text, int, bigint, bool,
	// float, decimal, json, uuid, date, datetime, time).
	Type string
	// Modifiers carries flags like "unique", "nullable", "index".
	Modifiers []string
	// Default is the default value literal (raw — emitted as-is into the
	// migration); empty means no default.
	Default string
}

// FieldName returns the Go struct field name (PascalCase), e.g. "user_id"
// → "UserID" — preserving common acronyms via the inflect package.
func (f FieldSpec) FieldName() string {
	return inflect.Pascal(f.Column)
}

// GoType returns the Go type the field is mapped to.
func (f FieldSpec) GoType() string {
	switch f.Type {
	case "string", "text", "longtext", "uuid", "char":
		return "string"
	case "int", "integer", "smallint", "tinyint":
		return "int"
	case "bigint", "biginteger":
		return "int64"
	case "uint", "unsignedint":
		return "uint64"
	case "bool", "boolean":
		return "bool"
	case "float":
		return "float32"
	case "double":
		return "float64"
	case "decimal":
		return "float64"
	case "json", "jsonb":
		return "any"
	case "date", "datetime", "timestamp", "time":
		return "time.Time"
	case "binary":
		return "[]byte"
	}
	return "string"
}

// NeedsTimeImport reports whether the field uses a time.* Go type.
func (f FieldSpec) NeedsTimeImport() bool {
	switch f.Type {
	case "date", "datetime", "timestamp", "time":
		return true
	}
	return false
}

// MigrationCall returns the schema.Blueprint method call for this field —
// e.g. `t.String("email").Unique()` — used by the migration stub.
func (f FieldSpec) MigrationCall() string {
	var sb strings.Builder
	switch f.Type {
	case "string":
		fmt.Fprintf(&sb, `t.String(%q)`, f.Column)
	case "char":
		fmt.Fprintf(&sb, `t.Char(%q)`, f.Column)
	case "text":
		fmt.Fprintf(&sb, `t.Text(%q)`, f.Column)
	case "longtext":
		fmt.Fprintf(&sb, `t.LongText(%q)`, f.Column)
	case "int", "integer":
		fmt.Fprintf(&sb, `t.Integer(%q)`, f.Column)
	case "smallint":
		fmt.Fprintf(&sb, `t.SmallInteger(%q)`, f.Column)
	case "tinyint":
		fmt.Fprintf(&sb, `t.TinyInteger(%q)`, f.Column)
	case "bigint", "biginteger":
		fmt.Fprintf(&sb, `t.BigInteger(%q)`, f.Column)
	case "uint":
		fmt.Fprintf(&sb, `t.UnsignedInteger(%q)`, f.Column)
	case "bool", "boolean":
		fmt.Fprintf(&sb, `t.Boolean(%q)`, f.Column)
	case "float":
		fmt.Fprintf(&sb, `t.Float(%q)`, f.Column)
	case "double":
		fmt.Fprintf(&sb, `t.Double(%q)`, f.Column)
	case "decimal":
		fmt.Fprintf(&sb, `t.Decimal(%q)`, f.Column)
	case "uuid":
		fmt.Fprintf(&sb, `t.UUID(%q)`, f.Column)
	case "json":
		fmt.Fprintf(&sb, `t.JSON(%q)`, f.Column)
	case "jsonb":
		fmt.Fprintf(&sb, `t.JSONB(%q)`, f.Column)
	case "date":
		fmt.Fprintf(&sb, `t.Date(%q)`, f.Column)
	case "datetime":
		fmt.Fprintf(&sb, `t.DateTime(%q)`, f.Column)
	case "timestamp":
		fmt.Fprintf(&sb, `t.Timestamp(%q)`, f.Column)
	case "time":
		fmt.Fprintf(&sb, `t.Time(%q)`, f.Column)
	case "binary":
		fmt.Fprintf(&sb, `t.Binary(%q)`, f.Column)
	default:
		fmt.Fprintf(&sb, `t.String(%q)`, f.Column)
	}
	if f.Default != "" {
		fmt.Fprintf(&sb, ".Default(%s)", goDefaultLiteral(f.Default))
	}
	for _, m := range f.Modifiers {
		switch m {
		case "unique":
			sb.WriteString(".Unique()")
		case "index":
			sb.WriteString(".Index()")
		case "nullable":
			sb.WriteString(".Nullable()")
		case "primary":
			sb.WriteString(".Primary()")
		}
	}
	return sb.String()
}

// FakerCall picks an appropriate faker method for this field, based on
// type + a few well-known column names. Returns "" when no convenient
// faker exists, in which case the generator emits a TODO comment.
func (f FieldSpec) FakerCall() string {
	switch f.Type {
	case "bool", "boolean":
		return "f.Bool()"
	case "int", "integer", "smallint", "tinyint":
		return "f.IntRange(1, 1000)"
	case "bigint", "biginteger", "uint":
		return "int64(f.IntRange(1, 1000000))"
	case "float", "double", "decimal":
		return "f.Float64Range(0, 1000)"
	case "uuid":
		return "f.UUID()"
	case "date", "datetime", "timestamp", "time":
		return "time.Now()"
	}
	// String-like types: pick a sensible faker based on column name.
	switch f.Column {
	case "name", "full_name":
		return "f.Name()"
	case "first_name":
		return "f.FirstName()"
	case "last_name":
		return "f.LastName()"
	case "email":
		return "f.Email()"
	case "phone", "phone_number":
		return "f.Phone()"
	case "address":
		return "f.Address()"
	case "city":
		return "f.City()"
	case "country":
		return "f.Country()"
	case "username":
		return "f.UserName()"
	case "password":
		return "f.Password()"
	case "url", "website":
		return "f.URL()"
	case "title":
		return `f.Sentence(5)`
	case "body", "content", "description", "bio":
		return `f.Paragraph(2, 3, 5, " ")`
	}
	return "f.Word()"
}

// goDefaultLiteral rewrites a default()-argument so it compiles as Go.
// SQL-style single quotes ('user') are converted to Go double quotes;
// numeric/boolean literals pass through; anything else is emitted as a
// double-quoted string so the migration still compiles.
func goDefaultLiteral(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return `""`
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strconv.Quote(s[1 : len(s)-1])
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s
	}
	switch s {
	case "true", "false", "nil":
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	return strconv.Quote(s)
}

// ParseFields parses a `--fields=name:string,email:string:unique` spec
// into a slice of FieldSpec. Whitespace around tokens is tolerated.
//
// Default values use `default(X)` syntax — `is_admin:bool:default(false)`
// — to avoid colliding with the modifier separator.
func ParseFields(spec string) ([]FieldSpec, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var out []FieldSpec
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ":")
		if len(parts) < 2 {
			return nil, fmt.Errorf("field spec %q: expected name:type[:modifier...]", raw)
		}
		col := inflect.Snake(strings.TrimSpace(parts[0]))
		if col == "" {
			return nil, fmt.Errorf("field spec %q: empty column name", raw)
		}
		typ := strings.ToLower(strings.TrimSpace(parts[1]))
		fs := FieldSpec{Column: col, Type: typ}
		for _, mod := range parts[2:] {
			mod = strings.TrimSpace(mod)
			if mod == "" {
				continue
			}
			if strings.HasPrefix(mod, "default(") && strings.HasSuffix(mod, ")") {
				fs.Default = strings.TrimSuffix(strings.TrimPrefix(mod, "default("), ")")
				continue
			}
			fs.Modifiers = append(fs.Modifiers, strings.ToLower(mod))
		}
		out = append(out, fs)
	}
	return out, nil
}
