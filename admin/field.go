package admin

import (
	"reflect"
	"strings"
	"time"

	"github.com/devituz/lagodev/internal/inflect"
)

// Field is the admin's metadata for a single model attribute, derived by
// reflecting over the struct definition and its `column:` / `orm:` tags. It
// drives both the list table (which columns to show) and the form (which
// inputs to render and their kind).
type Field struct {
	// Name is the Go struct field name (e.g. "CreatedAt").
	Name string
	// Column is the storage/data-source key. It comes from the `column:` tag,
	// or the snake_case of Name when the tag is absent.
	Column string
	// Label is the human-readable header/label (Title Case of Name).
	Label string
	// Kind classifies the input control to render: "text", "number", "bool",
	// "datetime", or "text" as the catch-all.
	Kind string

	// IsPrimary marks the primary-key field (the row identifier).
	IsPrimary bool
	// IsCreatedAt / IsUpdatedAt / IsDeletedAt mark managed timestamp columns.
	IsCreatedAt bool
	IsUpdatedAt bool
	IsDeletedAt bool
	// IsRelation marks slice/pointer-to-struct fields (skipped by default).
	IsRelation bool

	// InList is true when the field appears in the list table. Populated from
	// Resource.Columns (or the default) at registration.
	InList bool
	// Editable is true when the field is rendered on the create/edit form.
	Editable bool
}

var timeType = reflect.TypeOf(time.Time{})

// reflectFields walks a struct type (recursing into embedded structs) and
// returns admin Field metadata for every exported, non-skipped field. The
// derivation mirrors the framework's ORM tag conventions: `orm:"-"` and
// `column:"-"` skip a field; `column:"name"` overrides the storage key;
// ID/CreatedAt/UpdatedAt/DeletedAt are recognised by name.
func reflectFields(t reflect.Type) []Field {
	var out []Field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)

		// Embedded anonymous structs are flattened (e.g. a shared Model mixin).
		if sf.Anonymous {
			ft := indirectType(sf.Type)
			if ft != nil && ft.Kind() == reflect.Struct && ft != timeType {
				out = append(out, reflectFields(ft)...)
				continue
			}
		}
		if sf.PkgPath != "" { // unexported
			continue
		}
		f, ok := buildField(sf)
		if !ok {
			continue
		}
		out = append(out, f)
	}
	return out
}

// buildField translates a single struct field into a Field, returning ok=false
// when the field is explicitly skipped.
func buildField(sf reflect.StructField) (Field, bool) {
	if sf.Tag.Get("orm") == "-" {
		return Field{}, false
	}
	col := sf.Tag.Get("column")
	if col == "-" {
		return Field{}, false
	}
	if col == "" {
		col = inflect.Snake(sf.Name)
	}

	f := Field{
		Name:   sf.Name,
		Column: col,
		Label:  humanize(sf.Name),
		Kind:   kindOf(sf.Type),
	}

	switch sf.Name {
	case "ID":
		f.IsPrimary = true
	case "CreatedAt":
		if sf.Type == timeType {
			f.IsCreatedAt = true
		}
	case "UpdatedAt":
		if sf.Type == timeType {
			f.IsUpdatedAt = true
		}
	case "DeletedAt":
		if sf.Type == timeType || sf.Type == reflect.PtrTo(timeType) {
			f.IsDeletedAt = true
		}
	}

	// The `orm:"primary"` tag also designates a primary key.
	if hasOrmFlag(sf.Tag.Get("orm"), "primary") {
		f.IsPrimary = true
	}

	switch indirectType(sf.Type).Kind() {
	case reflect.Slice:
		elem := indirectType(sf.Type.Elem())
		if elem != nil && elem.Kind() == reflect.Struct && elem != timeType {
			f.IsRelation = true
		}
	case reflect.Struct:
		if indirectType(sf.Type) != timeType && sf.Type != timeType {
			// A non-time nested struct is treated as a relation, hidden by
			// default to keep the table flat.
			f.IsRelation = true
		}
	}

	return f, true
}

// kindOf maps a Go type to a form-input classification.
func kindOf(t reflect.Type) string {
	if t == timeType || t == reflect.PtrTo(timeType) {
		return "datetime"
	}
	switch indirectType(t).Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	default:
		return "text"
	}
}

// hasOrmFlag reports whether a semicolon-separated `orm:` tag contains flag.
func hasOrmFlag(tag, flag string) bool {
	for _, part := range strings.Split(tag, ";") {
		if strings.EqualFold(strings.TrimSpace(part), flag) {
			return true
		}
	}
	return false
}

// humanize turns a CamelCase struct field name into a Title Case label
// ("CreatedAt" -> "Created At", "ID" -> "ID").
func humanize(name string) string {
	if name == "" {
		return name
	}
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && isUpper(r) && !isUpper(runes[i-1]) {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
