// Package graphql is a dependency-free, struct-first GraphQL execution engine
// for the lagodev framework. You describe a schema in plain Go — objects,
// fields, arguments and resolver functions — and the package parses incoming
// GraphQL query documents, executes the requested selection set against your
// resolvers, and produces the standard {"data": ..., "errors": [...]} JSON
// response. It ships with a hand-written lexer + parser and an http.Handler,
// and pulls in nothing outside the standard library.
//
// # What this is (and is not)
//
// This is a pragmatic core, not a full GraphQL-spec implementation. It covers
// the parts day-to-day APIs actually use; the unsupported edges are documented
// honestly below so you are never surprised in production.
//
// Supported:
//
//   - Operations: query and mutation, anonymous or named.
//   - Selection sets: nested fields, aliases (alias: field), arguments.
//   - Arguments: Int, Float, String, Boolean, ID, enum values, lists,
//     input objects, null, and variable references ($var).
//   - Variables: declared in the operation, supplied via the request, coerced
//     to the declared type (Int/Float/String/Boolean/ID/enum/list/input).
//   - Fragments: inline fragments (... on Type { ... }) and named fragment
//     definitions referenced via spreads (...Name).
//   - Types: Object, Scalar (Int, Float, String, Boolean, ID), Enum,
//     InputObject, List (NewList) and NonNull (NewNonNull) wrappers.
//   - Execution: depth-first resolution, per-field error collection with a
//     response path, default field resolution from struct fields / maps.
//   - Introspection-lite: the __typename meta field on every object.
//   - HTTP: Schema.Handler() speaking the standard POST JSON protocol
//     ({query, variables, operationName}).
//
// Not supported (deferred — will error or be ignored, never silently wrong):
//
//   - Directives (@skip, @include, custom). Parsed-away is not attempted;
//     a directive in the document is a parse error.
//   - Subscriptions.
//   - Full __schema / __type introspection (only __typename is provided).
//   - Custom scalar coercion hooks beyond the five built-ins.
//   - Schema-side input validation beyond type coercion (e.g. no @constraint).
//
// # Defining a schema
//
//	var queryType = &graphql.Object{
//	    Name: "Query",
//	    Fields: graphql.Fields{
//	        "user": &graphql.Field{
//	            Type: userType,
//	            Args: graphql.Args{"id": {Type: graphql.NewNonNull(graphql.ID)}},
//	            Resolve: func(ctx context.Context, p any, a graphql.ArgValues) (any, error) {
//	                return findUser(a.String("id")), nil
//	            },
//	        },
//	    },
//	}
//	schema, err := graphql.NewSchema(graphql.Schema{Query: queryType})
//
// # Executing
//
//	res := schema.Execute(ctx, graphql.Request{Query: `{ user(id:"1"){ name } }`})
//	out, _ := json.Marshal(res) // {"data":{"user":{"name":"Ann"}}}
//
// See the package examples for a complete users->posts schema, a mutation, and
// an HTTP round-trip.
package graphql

import (
	"context"
	"fmt"
)

// ArgValues is the resolved, coerced argument map handed to a resolver. Keys
// are argument names; values are Go values already coerced to the argument's
// declared type (int64 for Int, float64 for Float, string for String/ID/enum,
// bool for Boolean, []any for lists, map[string]any for input objects).
type ArgValues map[string]any

// Get returns the raw value for name and whether it was present.
func (a ArgValues) Get(name string) (any, bool) { v, ok := a[name]; return v, ok }

// String returns the argument as a string, or "" if absent / not a string.
func (a ArgValues) String(name string) string {
	if s, ok := a[name].(string); ok {
		return s
	}
	return ""
}

// Int returns the argument as an int64, coercing float64 if needed, or 0.
func (a ArgValues) Int(name string) int64 {
	switch v := a[name].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	}
	return 0
}

// Float returns the argument as a float64, or 0.
func (a ArgValues) Float(name string) float64 {
	switch v := a[name].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	}
	return 0
}

// Bool returns the argument as a bool, or false.
func (a ArgValues) Bool(name string) bool { b, _ := a[name].(bool); return b }

// List returns the argument as a []any, or nil.
func (a ArgValues) List(name string) []any { l, _ := a[name].([]any); return l }

// Input returns the argument as a map[string]any (input object), or nil.
func (a ArgValues) Input(name string) map[string]any {
	m, _ := a[name].(map[string]any)
	return m
}

// ResolveFunc computes the value of a field. parent is the value returned by
// the enclosing field's resolver (or the root value for top-level fields);
// args are the coerced arguments. Returning an error surfaces it in the
// response "errors" array with the field's path, and the field's value becomes
// null without aborting sibling fields.
type ResolveFunc func(ctx context.Context, parent any, args ArgValues) (any, error)

// Type is the interface implemented by every GraphQL type (scalars, objects,
// enums, input objects, and the List / NonNull wrappers).
type Type interface {
	// TypeName returns the printable type name, e.g. "Int", "[User!]!".
	TypeName() string
	isType()
}

// Field describes one field of an Object: its result Type, optional Args, an
// optional human Description and the Resolve function. When Resolve is nil the
// executor falls back to default resolution (struct field or map key matching
// the field name, case-sensitive then case-insensitive).
type Field struct {
	Type        Type
	Args        Args
	Description string
	Resolve     ResolveFunc
}

// Fields maps field names to their definitions within an Object.
type Fields map[string]*Field

// Arg describes a single field/input argument: its Type and an optional
// Default applied when the caller omits it.
type Arg struct {
	Type    Type
	Default any
}

// Args maps argument names to their definitions.
type Args map[string]*Arg

// Object is a GraphQL output object type: a named set of resolvable Fields.
type Object struct {
	Name        string
	Description string
	Fields      Fields
}

// TypeName implements Type.
func (o *Object) TypeName() string { return o.Name }
func (o *Object) isType()          {}

// Scalar is a leaf type that serialises to a single JSON value. The five
// built-in scalars (Int, Float, String, Boolean, ID) are package-level
// variables; coercion of input/variable values into Go values is driven by
// the Name via coerceScalar.
type Scalar struct {
	Name        string
	Description string
}

// TypeName implements Type.
func (s *Scalar) TypeName() string { return s.Name }
func (s *Scalar) isType()          {}

// EnumValue is one member of an Enum. Value is the Go value the resolver sees
// for input and is compared against for output; when nil the Name is used.
type EnumValue struct {
	Value       any
	Description string
}

// Enum is a scalar-like type restricted to a fixed set of named values.
type Enum struct {
	Name        string
	Description string
	Values      map[string]*EnumValue
}

// TypeName implements Type.
func (e *Enum) TypeName() string { return e.Name }
func (e *Enum) isType()          {}

// value returns the Go value for the named enum member and whether it exists.
func (e *Enum) value(name string) (any, bool) {
	ev, ok := e.Values[name]
	if !ok {
		return nil, false
	}
	if ev != nil && ev.Value != nil {
		return ev.Value, true
	}
	return name, true
}

// InputObject is a structured argument type: a named set of input Fields, each
// with a Type and optional Default. Input objects may nest other input objects
// and lists.
type InputObject struct {
	Name        string
	Description string
	Fields      Args
}

// TypeName implements Type.
func (i *InputObject) TypeName() string { return i.Name }
func (i *InputObject) isType()          {}

// List is the [T] wrapper. Use NewList to construct.
type List struct{ OfType Type }

// TypeName implements Type.
func (l *List) TypeName() string { return "[" + l.OfType.TypeName() + "]" }
func (l *List) isType()          {}

// NewList wraps t in a List ([t]).
func NewList(t Type) *List { return &List{OfType: t} }

// NonNull is the T! wrapper. Use NewNonNull to construct.
type NonNull struct{ OfType Type }

// TypeName implements Type.
func (n *NonNull) TypeName() string { return n.OfType.TypeName() + "!" }
func (n *NonNull) isType()          {}

// NewNonNull wraps t in a NonNull (t!).
func NewNonNull(t Type) *NonNull { return &NonNull{OfType: t} }

// Built-in scalar types.
var (
	Int     = &Scalar{Name: "Int", Description: "Signed 32/64-bit integer."}
	Float   = &Scalar{Name: "Float", Description: "Signed double-precision float."}
	String  = &Scalar{Name: "String", Description: "UTF-8 character sequence."}
	Boolean = &Scalar{Name: "Boolean", Description: "true or false."}
	ID      = &Scalar{Name: "ID", Description: "Unique identifier, serialised as a String."}
)

// Schema is the entry point you build: a root Query object and an optional
// Mutation object. Pass it to NewSchema to validate and obtain an executable
// *CompiledSchema.
type Schema struct {
	Query    *Object
	Mutation *Object
}

// CompiledSchema is a validated, executable schema returned by NewSchema. It is
// safe for concurrent use.
type CompiledSchema struct {
	query    *Object
	mutation *Object
	// types indexes every named type reachable from the roots so the executor
	// can resolve inline-fragment and input-object type conditions by name.
	types map[string]Type
}

// NewSchema validates s and returns an executable *CompiledSchema. It errors
// when Query is nil or a reachable type is malformed (nil field type, etc.).
func NewSchema(s Schema) (*CompiledSchema, error) {
	if s.Query == nil {
		return nil, fmt.Errorf("graphql: schema requires a Query object")
	}
	cs := &CompiledSchema{query: s.Query, mutation: s.Mutation, types: map[string]Type{}}
	if err := cs.collect(s.Query); err != nil {
		return nil, err
	}
	if s.Mutation != nil {
		if err := cs.collect(s.Mutation); err != nil {
			return nil, err
		}
	}
	return cs, nil
}

// collect walks t, indexing named types and validating field/arg types.
func (cs *CompiledSchema) collect(t Type) error {
	switch v := t.(type) {
	case *List:
		return cs.collect(v.OfType)
	case *NonNull:
		return cs.collect(v.OfType)
	case *Scalar:
		cs.types[v.Name] = v
	case *Enum:
		cs.types[v.Name] = v
	case *InputObject:
		if _, seen := cs.types[v.Name]; seen {
			return nil
		}
		cs.types[v.Name] = v
		for name, a := range v.Fields {
			if a == nil || a.Type == nil {
				return fmt.Errorf("graphql: input %s.%s has nil type", v.Name, name)
			}
			if err := cs.collect(a.Type); err != nil {
				return err
			}
		}
	case *Object:
		if _, seen := cs.types[v.Name]; seen {
			return nil
		}
		cs.types[v.Name] = v
		for name, f := range v.Fields {
			if f == nil || f.Type == nil {
				return fmt.Errorf("graphql: field %s.%s has nil type", v.Name, name)
			}
			for an, a := range f.Args {
				if a == nil || a.Type == nil {
					return fmt.Errorf("graphql: arg %s.%s(%s) has nil type", v.Name, name, an)
				}
				if err := cs.collect(a.Type); err != nil {
					return err
				}
			}
			if err := cs.collect(f.Type); err != nil {
				return err
			}
		}
	case nil:
		return fmt.Errorf("graphql: nil type in schema")
	}
	return nil
}

// namedType returns the underlying named type, unwrapping List/NonNull.
func namedType(t Type) Type {
	for {
		switch v := t.(type) {
		case *List:
			t = v.OfType
		case *NonNull:
			t = v.OfType
		default:
			return t
		}
	}
}
