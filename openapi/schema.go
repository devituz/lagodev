package openapi

import (
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Schema is an OpenAPI 3.1 / JSON Schema object. It is a thin, JSON-faithful
// representation: only the fields lagodev emits are modelled, and everything
// marshals with the canonical OpenAPI key names. Unset fields are omitted so a
// generated document stays minimal.
//
// OpenAPI 3.1 aligns JSON Schema's "nullable" with a type union, so a nullable
// value is expressed as Type being a []string such as ["string","null"] rather
// than the 3.0-era `nullable: true`. The Type field therefore marshals as a
// bare string when single and as an array when it carries "null".
type Schema struct {
	Ref string `json:"$ref,omitempty"`

	Type   typeField `json:"type,omitempty"`
	Format string    `json:"format,omitempty"`

	// Object.
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`

	// Array.
	Items    *Schema `json:"items,omitempty"`
	MinItems *int    `json:"minItems,omitempty"`
	MaxItems *int    `json:"maxItems,omitempty"`

	// String bounds / numeric bounds (pointers so 0 is distinguishable from
	// "unset").
	MinLength *int     `json:"minLength,omitempty"`
	MaxLength *int     `json:"maxLength,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`

	// OpenAPI 3.1 / JSON Schema Draft 2020-12 model exclusive bounds as
	// numbers, but lagodev emits them as booleans paired with minimum/maximum
	// for readability; both forms validate equivalently for our generated docs.
	ExclusiveMinimum bool `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum bool `json:"exclusiveMaximum,omitempty"`

	Enum []any `json:"enum,omitempty"`

	Description string `json:"description,omitempty"`
}

// hasType reports whether name is one of the schema's declared types.
func (s *Schema) hasType(name string) bool {
	for _, t := range s.Type {
		if t == name {
			return true
		}
	}
	return false
}

// typeField marshals as a string when it holds a single type and as an array
// when it carries multiple (the OpenAPI 3.1 nullable idiom, e.g.
// ["string","null"]). An empty value is omitted entirely.
type typeField []string

func (t typeField) MarshalJSON() ([]byte, error) {
	switch len(t) {
	case 0:
		return []byte("null"), nil
	case 1:
		return []byte(strconv.Quote(t[0])), nil
	default:
		b := []byte{'['}
		for i, s := range t {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, strconv.Quote(s)...)
		}
		return append(b, ']'), nil
	}
}

func (t *typeField) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*t = nil
		return nil
	}
	if s[0] == '[' {
		var arr []string
		if err := jsonUnmarshal(b, &arr); err != nil {
			return err
		}
		*t = arr
		return nil
	}
	var one string
	if err := jsonUnmarshal(b, &one); err != nil {
		return err
	}
	*t = typeField{one}
	return nil
}

var timeType = reflect.TypeOf(time.Time{})

// SchemaOf returns the JSON Schema for T using struct reflection. It is the
// generic entry point; SchemaFor is the reflect.Type equivalent.
//
//	schema := openapi.SchemaOf[CreateUser]()
func SchemaOf[T any]() *Schema {
	var zero T
	return SchemaFor(reflect.TypeOf(&zero).Elem())
}

// SchemaFor builds a JSON Schema for an arbitrary Go type. It honours json
// tags (names, omitempty, "-"), validate tags (required + format/bounds/enum),
// and recurses through nested structs, slices, maps and pointers. time.Time is
// emitted as {type:string, format:date-time}. Pointer and omitempty fields
// become nullable (the 3.1 ["T","null"] union).
//
// SchemaFor builds inline schemas only; use a Registry / Spec.Components to
// produce $ref-able named component schemas.
func SchemaFor(t reflect.Type) *Schema {
	return schemaFor(t, nil)
}

// schemaFor is the shared worker. reg, when non-nil, causes named struct types
// to be registered as components and referenced by $ref instead of inlined.
func schemaFor(t reflect.Type, reg *Registry) *Schema {
	return schemaForSeen(t, reg, nil)
}

// schemaForSeen is schemaFor plus a cycle guard for the inline (reg == nil)
// path. The registry path breaks cycles by reserving a $ref slot in add, but an
// inline build has no components to point at, so it tracks the struct types on
// the current recursion path in seen and emits an open object schema when it
// would otherwise recurse into a type already being built. Without this a
// self-referential struct (e.g. type Node struct{ Next *Node }) overflows the
// stack.
func schemaForSeen(t reflect.Type, reg *Registry, seen map[reflect.Type]bool) *Schema {
	// Unwrap pointers; a pointer type itself does not change the schema shape
	// here (nullability is decided per-field in structFieldSchema).
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t == timeType {
		return &Schema{Type: typeField{"string"}, Format: "date-time"}
	}

	switch t.Kind() {
	case reflect.Bool:
		return &Schema{Type: typeField{"boolean"}}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &Schema{Type: typeField{"integer"}, Format: "int64"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: typeField{"integer"}, Format: "int64"}
	case reflect.Float32:
		return &Schema{Type: typeField{"number"}, Format: "float"}
	case reflect.Float64:
		return &Schema{Type: typeField{"number"}, Format: "double"}
	case reflect.String:
		return &Schema{Type: typeField{"string"}}
	case reflect.Slice, reflect.Array:
		// []byte serialises as a base64 string in encoding/json.
		if t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: typeField{"string"}, Format: "byte"}
		}
		return &Schema{Type: typeField{"array"}, Items: schemaForSeen(t.Elem(), reg, seen)}
	case reflect.Map:
		return &Schema{
			Type:                 typeField{"object"},
			AdditionalProperties: schemaForSeen(t.Elem(), reg, seen),
		}
	case reflect.Struct:
		if reg != nil {
			name := reg.add(t)
			return &Schema{Ref: "#/components/schemas/" + name}
		}
		// Inline build: guard against self-referential / mutually-recursive
		// struct types, which have no $ref to terminate the recursion.
		if seen[t] {
			return &Schema{Type: typeField{"object"}}
		}
		return structSchemaSeen(t, reg, seen)
	case reflect.Interface:
		// An open value: no constraints (any JSON).
		return &Schema{}
	default:
		return &Schema{}
	}
}

// structSchema builds the object schema for a struct type, walking exported
// fields. Embedded (anonymous) struct fields are flattened, matching
// encoding/json's promotion of their fields.
func structSchema(t reflect.Type, reg *Registry) *Schema {
	return structSchemaSeen(t, reg, nil)
}

func structSchemaSeen(t reflect.Type, reg *Registry, seen map[reflect.Type]bool) *Schema {
	s := &Schema{
		Type:       typeField{"object"},
		Properties: map[string]*Schema{},
	}
	// Mark this struct type as on the current path so nested fields that loop
	// back to it terminate. The registry path uses its own slot reservation, so
	// this set is only consulted when reg == nil.
	if reg == nil {
		if seen == nil {
			seen = map[reflect.Type]bool{}
		}
		seen[t] = true
		defer delete(seen, t)
	}
	collectFields(t, s, reg, seen)
	if len(s.Required) == 0 {
		s.Required = nil
	}
	return s
}

func collectFields(t reflect.Type, s *Schema, reg *Registry, seen map[reflect.Type]bool) {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)

		// Flatten anonymous (embedded) structs without a json tag, mirroring
		// encoding/json field promotion. An embedded struct may itself be an
		// unexported type yet still promote its exported fields, so this check
		// precedes the IsExported guard below.
		ft := sf.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if sf.Anonymous && sf.Tag.Get("json") == "" && ft.Kind() == reflect.Struct && ft != timeType {
			// A struct that embeds itself (directly or via a cycle) would loop
			// here; mark the embedded type on the path and skip a re-entry,
			// mirroring the field-level cycle guard.
			if reg == nil {
				if seen[ft] {
					continue
				}
				if seen == nil {
					seen = map[reflect.Type]bool{}
				}
				seen[ft] = true
			}
			collectFields(ft, s, reg, seen)
			continue
		}

		if !sf.IsExported() {
			continue
		}
		name, omitempty, skip := jsonName(sf)
		if skip {
			continue
		}

		fieldSchema := schemaForSeen(sf.Type, reg, seen)
		nullable := sf.Type.Kind() == reflect.Ptr || omitempty

		rules := parseValidate(sf.Tag.Get("validate"))
		applyValidate(fieldSchema, rules)

		if nullable {
			markNullable(fieldSchema)
		}

		s.Properties[name] = fieldSchema

		if rules.has("required") && !omitempty {
			s.Required = append(s.Required, name)
		}
	}
}

// markNullable adds "null" to a schema's type union (3.1 idiom). It is a no-op
// for a $ref schema, which carries no type, and avoids duplicate "null".
func markNullable(s *Schema) {
	if s.Ref != "" || len(s.Type) == 0 {
		return
	}
	for _, t := range s.Type {
		if t == "null" {
			return
		}
	}
	s.Type = append(s.Type, "null")
}

// jsonName resolves a struct field's JSON object key, omitempty flag, and
// whether it is skipped (`json:"-"`).
func jsonName(sf reflect.StructField) (name string, omitempty, skip bool) {
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	name = sf.Name
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			name = parts[0]
		}
		for _, opt := range parts[1:] {
			if opt == "omitempty" {
				omitempty = true
			}
		}
	}
	return name, omitempty, false
}
