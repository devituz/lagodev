package openapi

import (
	"reflect"
	"sort"
)

// Registry collects named component schemas so struct types are emitted once
// under components/schemas and referenced elsewhere by $ref. It de-duplicates by
// the Go type's name, recursing into nested struct fields.
type Registry struct {
	schemas map[string]*Schema // name -> schema
	types   map[string]reflect.Type
}

// newRegistry returns an empty Registry.
func newRegistry() *Registry {
	return &Registry{
		schemas: map[string]*Schema{},
		types:   map[string]reflect.Type{},
	}
}

// add registers t (a struct type) as a named component if not already present
// and returns the component name. Anonymous structs fall back to inlining via a
// synthetic name; named structs use their declared name.
func (r *Registry) add(t reflect.Type) string {
	name := t.Name()
	if name == "" {
		name = "Anonymous"
	}
	if _, ok := r.schemas[name]; ok {
		return name
	}
	// Reserve the slot before recursing so a self-referential struct does not
	// loop forever.
	r.schemas[name] = nil
	r.types[name] = t
	r.schemas[name] = structSchema(t, r)
	return name
}

// Register adds T to the registry, returning its component name. It is the
// exported way to pre-register a response/request DTO.
func Register[T any](r *Registry) string {
	t := reflect.TypeOf((*T)(nil)).Elem()
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return ""
	}
	return r.add(t)
}

// Schemas returns the registered component schemas keyed by name. The map is a
// fresh copy safe for the caller to mutate.
func (r *Registry) Schemas() map[string]*Schema {
	out := make(map[string]*Schema, len(r.schemas))
	for k, v := range r.schemas {
		out[k] = v
	}
	return out
}

// names returns registered component names in deterministic (sorted) order.
func (r *Registry) names() []string {
	out := make([]string, 0, len(r.schemas))
	for k := range r.schemas {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
