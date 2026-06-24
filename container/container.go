// Package container provides a type-safe, generics-based dependency
// injection (service) container modelled on Laravel's container and
// NestJS's DI, made idiomatic for Go.
//
// Services are keyed on their static type via reflect. Registration and
// resolution go through package-level generic functions so callers never
// type-assert:
//
//	type Config struct{ DSN string }
//	type DB struct{ cfg Config }
//	type UserRepo struct{ db *DB }
//
//	c := container.New()
//	container.Instance(c, Config{DSN: "postgres://…"})
//	container.Singleton(c, func(c *container.Container) (*DB, error) {
//	    cfg, err := container.Make[Config](c)
//	    if err != nil {
//	        return nil, err
//	    }
//	    return &DB{cfg: cfg}, nil
//	})
//	container.Bind(c, func(c *container.Container) (*UserRepo, error) {
//	    db, err := container.Make[*DB](c)
//	    if err != nil {
//	        return nil, err
//	    }
//	    return &UserRepo{db: db}, nil
//	})
//
//	repo := container.MustMake[*UserRepo](c)
//
// Lifetimes:
//
//   - Bind      — transient: the factory runs on every Make.
//   - Singleton — the factory runs at most once; the result is cached and
//     shared. Caching is concurrency-safe: under concurrent Make the
//     factory runs exactly once and every caller observes the same value.
//   - Instance  — an already-built value, returned as-is on every Make.
//
// Named bindings (BindNamed / MakeNamed) let several implementations of
// one type coexist under distinct names.
//
// Scopes (c.Scope()) create child containers that inherit the parent's
// bindings and may override them. A singleton defined in the parent
// resolves to the parent's shared instance even when made from a child;
// a singleton defined (overridden) in a scope caches inside that scope,
// giving per-request lifetimes.
//
// Build provides modest, opt-in reflection-based autowiring for struct
// types: it allocates the struct and fills each exported field whose type
// is already bound. See Build for the exact rules.
//
// All exported operations are safe for concurrent use.
package container

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ErrNotBound is returned by Make/MakeNamed when no binding (in the
// container or any ancestor scope) matches the requested type and name.
var ErrNotBound = errors.New("container: type not bound")

// cyclicError reports a dependency cycle detected while resolving. It
// carries the chain of types that form the loop so the message is
// actionable.
type cyclicError struct {
	chain []string
}

func (e *cyclicError) Error() string {
	return "container: cyclic dependency detected: " + strings.Join(e.chain, " -> ")
}

// provider is the type-erased recipe for one binding. The generic
// registration helpers populate it; resolution is driven entirely from
// these fields so the container core is non-generic.
type provider struct {
	// factory builds a fresh value. nil for pure Instance bindings.
	factory func(*Container) (any, error)
	// singleton marks the binding as resolve-once-and-cache.
	singleton bool

	// once guards single execution of a singleton factory; instance and
	// value cache the resolved result.
	once     sync.Once
	instance any
	instErr  error
}

// key identifies a binding by type and optional name. The zero name is
// the default (unnamed) binding.
type key struct {
	typ  reflect.Type
	name string
}

// Container is a dependency-injection container. The zero value is not
// usable; obtain one from New or, for a child scope, from Scope.
//
// Safe for concurrent use.
type Container struct {
	parent *Container

	mu        sync.RWMutex
	providers map[key]*provider

	// resolving tracks the per-goroutine resolve stack used for cycle
	// detection. It lives on the root container so a single chain spanning
	// parent and child scopes is still detected.
	resolveMu sync.Mutex
	resolving map[int64][]key
}

// New returns a fresh root container.
func New() *Container {
	return &Container{
		providers: make(map[key]*provider),
		resolving: make(map[int64][]key),
	}
}

// Scope returns a child container that inherits c's bindings. Bindings
// added to the child shadow the parent for resolutions made through the
// child, leaving the parent untouched. Singletons defined on an ancestor
// resolve to that ancestor's shared instance; singletons (re)defined on
// the scope cache within the scope, which is how per-request lifetimes are
// expressed.
func (c *Container) Scope() *Container {
	return &Container{
		parent:    c,
		providers: make(map[key]*provider),
		// Scopes share the root's resolve-tracking so a cycle crossing the
		// parent/child boundary is still caught.
		resolving: c.root().resolving,
	}
}

func (c *Container) root() *Container {
	r := c
	for r.parent != nil {
		r = r.parent
	}
	return r
}

// register installs p under k on c, replacing any existing binding with
// the same key on this container (ancestors are unaffected).
func (c *Container) register(k key, p *provider) {
	c.mu.Lock()
	c.providers[k] = p
	c.mu.Unlock()
}

// lookup finds the provider for k and the container that owns it,
// searching c then its ancestors. The owning container matters for
// singletons: the factory runs and caches on the defining container so the
// instance is shared by everything below it.
func (c *Container) lookup(k key) (*provider, *Container, bool) {
	for cur := c; cur != nil; cur = cur.parent {
		cur.mu.RLock()
		p, ok := cur.providers[k]
		cur.mu.RUnlock()
		if ok {
			return p, cur, true
		}
	}
	return nil, nil, false
}

// typeOf returns the reflect.Type used as a binding key for T. For
// interface and pointer types this is the declared type, not the dynamic
// one, so Make[T] always keys on what the caller wrote.
func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// Bind registers a transient factory for type T: the factory runs on every
// Make[T]. Re-binding the same type (and name) replaces the prior binding.
func Bind[T any](c *Container, factory func(*Container) (T, error)) {
	bind(c, typeOf[T](), "", false, wrap(factory))
}

// Singleton registers factory for type T as a singleton: the factory runs
// at most once and the result is cached and shared. Resolution is
// concurrency-safe — under concurrent Make the factory runs exactly once.
func Singleton[T any](c *Container, factory func(*Container) (T, error)) {
	bind(c, typeOf[T](), "", true, wrap(factory))
}

// Instance registers an already-built value for type T. Every Make[T]
// returns value unchanged. Equivalent to a singleton that is already
// resolved.
func Instance[T any](c *Container, value T) {
	// factory==nil marks a pre-resolved instance; resolve returns it as-is.
	c.register(key{typ: typeOf[T]()}, &provider{singleton: true, instance: value})
}

// BindNamed registers factory for type T under name, allowing several
// implementations of one type to coexist. Resolve with MakeNamed.
func BindNamed[T any](c *Container, name string, factory func(*Container) (T, error)) {
	bind(c, typeOf[T](), name, false, wrap(factory))
}

// SingletonNamed registers a named singleton for type T. Like Singleton
// but addressed by name.
func SingletonNamed[T any](c *Container, name string, factory func(*Container) (T, error)) {
	bind(c, typeOf[T](), name, true, wrap(factory))
}

// InstanceNamed registers an already-built value for type T under name.
func InstanceNamed[T any](c *Container, name string, value T) {
	c.register(key{typ: typeOf[T](), name: name}, &provider{singleton: true, instance: value})
}

// wrap adapts a typed factory to the type-erased provider signature.
func wrap[T any](factory func(*Container) (T, error)) func(*Container) (any, error) {
	return func(c *Container) (any, error) {
		v, err := factory(c)
		if err != nil {
			return nil, err
		}
		return v, nil
	}
}

func bind(c *Container, t reflect.Type, name string, singleton bool, f func(*Container) (any, error)) {
	c.register(key{typ: t, name: name}, &provider{factory: f, singleton: singleton})
}

// Make resolves the value bound to type T. It returns ErrNotBound if no
// binding matches, propagates the factory's error, and returns a cyclic
// dependency error (carrying the type chain) instead of deadlocking when a
// factory depends, directly or transitively, on itself.
func Make[T any](c *Container) (T, error) {
	return MakeNamed[T](c, "")
}

// MakeNamed resolves the value bound to type T under name. See Make.
func MakeNamed[T any](c *Container, name string) (T, error) {
	var zero T
	k := key{typ: typeOf[T](), name: name}
	v, err := c.resolve(k)
	if err != nil {
		return zero, err
	}
	// Factories return the concrete T; assert back to it. For interface T
	// the stored value satisfies the assertion.
	tv, ok := v.(T)
	if !ok {
		// nil values for pointer/interface T arrive as a typed nil and may
		// fail a direct assertion; reflect handles the assignable case.
		if v == nil {
			return zero, nil
		}
		return zero, fmt.Errorf("container: resolved value for %s has unexpected type %T", k.typ, v)
	}
	return tv, nil
}

// MustMake is Make for wiring/bootstrap code: it panics on any error.
func MustMake[T any](c *Container) T {
	v, err := Make[T](c)
	if err != nil {
		panic(err)
	}
	return v
}

// MustMakeNamed is MakeNamed that panics on any error.
func MustMakeNamed[T any](c *Container, name string) T {
	v, err := MakeNamed[T](c, name)
	if err != nil {
		panic(err)
	}
	return v
}

// resolve performs a type-erased resolution of k against c and its
// ancestors, applying lifetime semantics and cycle detection.
func (c *Container) resolve(k key) (any, error) {
	p, owner, ok := c.lookup(k)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotBound, describeKey(k))
	}

	// Cycle detection: track the resolve stack per goroutine. The stack
	// lives on the root so chains crossing scope boundaries are visible.
	gid := goID()
	root := c.root()

	root.resolveMu.Lock()
	stack := root.resolving[gid]
	for _, seen := range stack {
		if seen == k {
			chain := make([]string, 0, len(stack)+1)
			for _, s := range stack {
				chain = append(chain, describeKey(s))
			}
			chain = append(chain, describeKey(k))
			root.resolveMu.Unlock()
			return nil, &cyclicError{chain: chain}
		}
	}
	root.resolving[gid] = append(stack, k)
	root.resolveMu.Unlock()

	defer func() {
		root.resolveMu.Lock()
		s := root.resolving[gid]
		if len(s) > 0 {
			s = s[:len(s)-1]
		}
		if len(s) == 0 {
			delete(root.resolving, gid)
		} else {
			root.resolving[gid] = s
		}
		root.resolveMu.Unlock()
	}()

	// Instance binding (factory nil): the cached value is authoritative.
	if p.factory == nil {
		return p.instance, p.instErr
	}

	if !p.singleton {
		// Transient: run the factory every time, resolving relative to the
		// container the call started from so scoped overrides are visible.
		return p.factory(c)
	}

	// Singleton: resolve exactly once on the owning container and cache.
	// sync.Once guarantees a single factory execution even under concurrent
	// Make, and every caller observes the same instance/error.
	p.once.Do(func() {
		p.instance, p.instErr = p.factory(owner)
	})
	return p.instance, p.instErr
}

// describeKey renders a binding key for error messages.
func describeKey(k key) string {
	t := "<nil>"
	if k.typ != nil {
		t = k.typ.String()
	}
	if k.name != "" {
		return t + " (name=" + k.name + ")"
	}
	return t
}

// Build allocates a value of struct type T and autowires its exported
// fields from the container. It is opt-in and intentionally modest.
//
// Rules:
//
//   - T (after dereferencing one pointer level) must be a struct.
//   - For each exported field, the container is asked for a binding keyed
//     on the field's exact type (unnamed). A field tagged
//     `inject:"name"` resolves the named binding instead; `inject:"-"` is
//     skipped.
//   - A field whose type is bound is set to the resolved value. A field
//     whose type is not bound is left at its zero value, unless it is
//     tagged `inject:""` or `inject:"name"` (explicitly requested), in
//     which case the missing binding is a hard error.
//   - Unexported fields are never touched.
//
// Build resolves through c (and its ancestors/overrides) and participates
// in cycle detection.
func Build[T any](c *Container) (T, error) {
	var zero T
	rt := typeOf[T]()

	// Allow T to be a struct or a pointer to a struct.
	isPtr := rt.Kind() == reflect.Ptr
	st := rt
	if isPtr {
		st = rt.Elem()
	}
	if st.Kind() != reflect.Struct {
		return zero, fmt.Errorf("container: Build requires a struct type, got %s", rt)
	}

	out := reflect.New(st) // *st
	elem := out.Elem()

	for i := 0; i < st.NumField(); i++ {
		field := st.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}
		tag, hasTag := field.Tag.Lookup("inject")
		if hasTag && tag == "-" {
			continue
		}
		required := hasTag // an inject tag (even empty) makes the field required
		name := ""
		if hasTag {
			name = tag
		}

		v, err := c.resolve(key{typ: field.Type, name: name})
		if err != nil {
			if errors.Is(err, ErrNotBound) && !required {
				continue // optional, unbound: leave zero
			}
			return zero, fmt.Errorf("container: Build %s.%s: %w", st, field.Name, err)
		}
		if v == nil {
			continue
		}
		rv := reflect.ValueOf(v)
		if !rv.Type().AssignableTo(field.Type) {
			return zero, fmt.Errorf("container: Build %s.%s: resolved %s not assignable to %s",
				st, field.Name, rv.Type(), field.Type)
		}
		elem.Field(i).Set(rv)
	}

	if isPtr {
		return out.Interface().(T), nil
	}
	return elem.Interface().(T), nil
}
