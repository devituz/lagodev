// Package reflectutil provides a thread-safe, allocation-amortized reflection
// cache. Models are parsed once on first use; subsequent accesses are O(1).
//
// It is the single source of truth for: column name resolution, primary-key
// discovery, soft-delete column detection, fillable/hidden lists, cast tags,
// and embedded base-model handling.
package reflectutil

import (
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/devituz/lagodev/internal/inflect"
)

// Field describes one persistent struct field.
type Field struct {
	// Name is the Go struct field name (e.g. "CreatedAt").
	Name string
	// Column is the database column name (e.g. "created_at"), resolved from
	// `column:"..."` tag or snake_case of Name.
	Column string
	// Index is the index path for reflect.Value.FieldByIndex.
	Index []int
	// Type is the field's Go type.
	Type reflect.Type
	// IsPrimary indicates the field is (part of) the primary key.
	IsPrimary bool
	// IsAutoIncrement marks an integer primary key as auto-increment.
	IsAutoIncrement bool
	// IsCreatedAt / IsUpdatedAt / IsDeletedAt mark timestamp columns.
	IsCreatedAt bool
	IsUpdatedAt bool
	IsDeletedAt bool
	// IsZeroOnInsert means "send NULL/skip when zero on insert".
	IsZeroOnInsert bool
	// Hidden columns are never serialized via ToMap.
	Hidden bool
	// Fillable indicates the column accepts mass assignment.
	Fillable bool
	// Cast is the registered cast name (e.g. "json", "date", "bool").
	Cast string
	// HasDefault is true when the column has a struct-tag default.
	HasDefault bool
	// Default is the literal default value as written in the tag.
	Default string
	// Nullable as declared on the field tag.
	Nullable bool
	// Skip means: ignore this field for persistence (`column:"-"`).
	Skip bool
	// IsRelation marks the field as a relation (slice of struct / pointer to
	// struct) discovered automatically; relations are not persisted as columns.
	IsRelation bool
}

// Schema describes a parsed model type.
type Schema struct {
	Type        reflect.Type
	Table       string
	Fields      []*Field
	byName      map[string]*Field
	byColumn    map[string]*Field
	PrimaryKey  *Field
	CreatedAt   *Field
	UpdatedAt   *Field
	DeletedAt   *Field
	SoftDeletes bool
}

// FieldByName returns the Field with matching Go name, or nil.
func (s *Schema) FieldByName(name string) *Field {
	return s.byName[name]
}

// FieldByColumn returns the Field with matching column name, or nil.
func (s *Schema) FieldByColumn(col string) *Field {
	return s.byColumn[col]
}

// Columns returns all persistent column names (excludes relations & skips).
func (s *Schema) Columns() []string {
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.Skip || f.IsRelation {
			continue
		}
		out = append(out, f.Column)
	}
	return out
}

// InsertableColumns returns columns to include on insert (skips relations,
// skip-marked, and zero-valued auto fields).
func (s *Schema) InsertableColumns(v reflect.Value) ([]string, []any) {
	cols := make([]string, 0, len(s.Fields))
	vals := make([]any, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.Skip || f.IsRelation {
			continue
		}
		fv := v.FieldByIndex(f.Index)
		if f.IsAutoIncrement && fv.IsZero() {
			continue
		}
		if f.IsDeletedAt {
			continue
		}
		cols = append(cols, f.Column)
		vals = append(vals, fv.Interface())
	}
	return cols, vals
}

var cache sync.Map // map[reflect.Type]*Schema

// Parse returns the cached Schema for v's underlying struct type. It
// transparently dereferences pointers and slices.
func Parse(v any) *Schema {
	t := indirectType(reflect.TypeOf(v))
	if cached, ok := cache.Load(t); ok {
		return cached.(*Schema)
	}
	s := parseType(t)
	actual, _ := cache.LoadOrStore(t, s)
	return actual.(*Schema)
}

// ParseType returns the cached Schema for a reflect.Type directly.
func ParseType(t reflect.Type) *Schema {
	t = indirectType(t)
	if cached, ok := cache.Load(t); ok {
		return cached.(*Schema)
	}
	s := parseType(t)
	actual, _ := cache.LoadOrStore(t, s)
	return actual.(*Schema)
}

func parseType(t reflect.Type) *Schema {
	if t.Kind() != reflect.Struct {
		return &Schema{Type: t}
	}
	s := &Schema{
		Type:     t,
		Table:    inflect.Table(t.Name()),
		byName:   make(map[string]*Field),
		byColumn: make(map[string]*Field),
	}
	// Allow models to opt out of pluralization or override via TableName().
	collect(s, t, nil)
	for _, f := range s.Fields {
		s.byName[f.Name] = f
		s.byColumn[f.Column] = f
		switch {
		case f.IsPrimary && s.PrimaryKey == nil:
			s.PrimaryKey = f
		}
		if f.IsCreatedAt {
			s.CreatedAt = f
		}
		if f.IsUpdatedAt {
			s.UpdatedAt = f
		}
		if f.IsDeletedAt {
			s.DeletedAt = f
			s.SoftDeletes = true
		}
	}
	return s
}

func collect(s *Schema, t reflect.Type, index []int) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		idx := append(append([]int(nil), index...), i)

		// Recurse into anonymous embedded structs (orm.Model, orm.Timestamps, ...).
		if sf.Anonymous && indirectType(sf.Type).Kind() == reflect.Struct {
			collect(s, indirectType(sf.Type), idx)
			continue
		}

		f := buildField(sf, idx)
		if f == nil {
			continue
		}
		s.Fields = append(s.Fields, f)
	}
}

func buildField(sf reflect.StructField, idx []int) *Field {
	tag := sf.Tag.Get("orm")
	if tag == "-" {
		return nil
	}
	col := sf.Tag.Get("column")
	f := &Field{
		Name:   sf.Name,
		Column: col,
		Index:  idx,
		Type:   sf.Type,
	}

	if f.Column == "" {
		f.Column = inflect.Snake(sf.Name)
	}
	if f.Column == "-" {
		f.Skip = true
	}

	// Auto-detect well-known timestamp fields.
	switch sf.Name {
	case "ID":
		f.IsPrimary = true
		f.IsAutoIncrement = isIntegerKind(sf.Type.Kind())
	case "CreatedAt":
		if sf.Type == reflect.TypeOf(time.Time{}) {
			f.IsCreatedAt = true
		}
	case "UpdatedAt":
		if sf.Type == reflect.TypeOf(time.Time{}) {
			f.IsUpdatedAt = true
		}
	case "DeletedAt":
		f.IsDeletedAt = true
		f.Nullable = true
	}

	// Detect relation-shaped fields (slice/pointer to struct, non-time).
	switch sf.Type.Kind() {
	case reflect.Slice:
		elem := indirectType(sf.Type.Elem())
		if elem.Kind() == reflect.Struct && elem != reflect.TypeOf(time.Time{}) {
			f.IsRelation = true
		}
	case reflect.Ptr:
		elem := indirectType(sf.Type)
		if elem.Kind() == reflect.Struct && elem != reflect.TypeOf(time.Time{}) {
			// Only treat as relation when explicitly tagged.
			if sf.Tag.Get("relation") != "" {
				f.IsRelation = true
			}
		}
	}

	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, hasV := strings.Cut(part, ":")
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "primary", "primarykey":
			f.IsPrimary = true
		case "autoincrement":
			f.IsAutoIncrement = true
		case "nullable":
			f.Nullable = true
		case "hidden":
			f.Hidden = true
		case "fillable":
			f.Fillable = true
		case "cast":
			if hasV {
				f.Cast = v
			}
		case "default":
			if hasV {
				f.HasDefault = true
				f.Default = v
			}
		case "column":
			if hasV {
				f.Column = v
			}
		}
	}
	return f
}

func indirectType(t reflect.Type) reflect.Type {
	for t != nil && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice) {
		t = t.Elem()
	}
	return t
}

func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// IndirectValue dereferences a Value through pointers.
func IndirectValue(v reflect.Value) reflect.Value {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return v
		}
		v = v.Elem()
	}
	return v
}
