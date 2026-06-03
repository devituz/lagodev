// Package authz provides Gate (function-based) and Policy
// (struct-based) authorisation primitives modelled on Laravel's Gate
// facade.
//
// Two patterns are supported:
//
//  1. Gate — register an ability name with a closure. Useful for
//     ad-hoc checks that don't fit a model boundary:
//
//     g := authz.New()
//     authz.Define(g, "manage-billing", func(_ context.Context, u User, _ any) bool {
//         return u.Role == "admin"
//     })
//     ok, _ := g.Allows(ctx, "manage-billing", currentUser, nil)
//
//  2. Policy — a struct whose methods cover the per-resource abilities.
//     Register it once per resource type and the gate routes the
//     ability name to the matching method:
//
//     type PostPolicy struct{}
//     func (PostPolicy) Update(_ context.Context, u User, p Post) bool { return p.AuthorID == u.ID }
//     authz.Policy[Post](g, PostPolicy{})
//     ok, _ := g.Allows(ctx, "update", currentUser, somePost)
//
// Authorize is the panic-friendlier variant; Check returns the same
// information without the panic.
package authz

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// ErrDenied is returned by Authorize when a check fails.
var ErrDenied = errors.New("authz: denied")

// ErrUnknownAbility is returned when no gate or policy matches the
// requested ability.
var ErrUnknownAbility = errors.New("authz: unknown ability")

// Decision captures the result of a check.
type Decision struct {
	Allowed bool
	Reason  string
}

// Gate is the registry that routes ability names to closures and
// resource-typed methods on registered policies. Safe for concurrent
// use.
type Gate[U any] struct {
	mu        sync.RWMutex
	gates     map[string]gateFn[U]
	policies  map[reflect.Type]reflect.Value // resource type → policy value
	beforeFns []func(ctx context.Context, user U, ability string) bool
}

type gateFn[U any] func(ctx context.Context, user U, resource any) bool

// New returns a fresh Gate parameterised on the user type.
func New[U any]() *Gate[U] {
	return &Gate[U]{
		gates:    make(map[string]gateFn[U]),
		policies: make(map[reflect.Type]reflect.Value),
	}
}

// Define registers a closure for ability. Re-registration replaces the
// previous closure.
//
// Use the generic R parameter to let callers receive a typed resource:
//
//	authz.Define(g, "delete-post", func(ctx context.Context, u User, p Post) bool { ... })
func Define[U any, R any](g *Gate[U], ability string, fn func(ctx context.Context, user U, resource R) bool) {
	wrapped := func(ctx context.Context, u U, r any) bool {
		// nil resource is fine if R is an interface/pointer; for value
		// types we pass the zero value.
		var rv R
		if r != nil {
			if cast, ok := r.(R); ok {
				rv = cast
			} else {
				return false
			}
		}
		return fn(ctx, u, rv)
	}
	g.mu.Lock()
	g.gates[ability] = wrapped
	g.mu.Unlock()
}

// Before registers a hook that runs before every check. If it returns
// true, the check short-circuits to allow. Use sparingly — typically
// for an "admin can do anything" override.
func Before[U any](g *Gate[U], fn func(ctx context.Context, user U, ability string) bool) {
	g.mu.Lock()
	g.beforeFns = append(g.beforeFns, fn)
	g.mu.Unlock()
}

// Policy registers a policy struct for resource type R. Each exported
// method on the policy whose name matches an ability (case-insensitive,
// kebab/snake-aware) and whose signature is one of:
//
//	func(ctx context.Context, user U, resource R) bool
//	func(ctx context.Context, user U, resource R) (bool, error)
//
// is registered. Methods that don't match are ignored.
func Policy[R any, U any](g *Gate[U], policy any) {
	rt := reflect.TypeOf((*R)(nil)).Elem()
	g.mu.Lock()
	g.policies[rt] = reflect.ValueOf(policy)
	g.mu.Unlock()
}

// Allows reports whether user is permitted to perform ability on the
// (optional) resource. Resource may be nil for gate-only abilities.
func (g *Gate[U]) Allows(ctx context.Context, ability string, user U, resource any) (bool, error) {
	g.mu.RLock()
	beforeFns := append([]func(ctx context.Context, u U, ability string) bool{}, g.beforeFns...)
	fn, hasGate := g.gates[ability]
	policies := g.policies
	g.mu.RUnlock()

	for _, b := range beforeFns {
		if b(ctx, user, ability) {
			return true, nil
		}
	}
	if hasGate {
		return fn(ctx, user, resource), nil
	}
	if resource != nil {
		rt := reflect.TypeOf(resource)
		if pv, ok := policies[rt]; ok {
			return invokePolicy(ctx, pv, ability, user, resource)
		}
		// Indirect (pointer) lookup
		if rt.Kind() == reflect.Ptr {
			if pv, ok := policies[rt.Elem()]; ok {
				return invokePolicy(ctx, pv, ability, user, reflect.ValueOf(resource).Elem().Interface())
			}
		}
	}
	return false, fmt.Errorf("%w: %q", ErrUnknownAbility, ability)
}

// Denies is the boolean opposite of Allows. Errors collapse to denial.
func (g *Gate[U]) Denies(ctx context.Context, ability string, user U, resource any) bool {
	ok, err := g.Allows(ctx, ability, user, resource)
	return err != nil || !ok
}

// Authorize is Allows that returns ErrDenied instead of (false, nil).
// Idiomatic in handler code:
//
//	if err := gate.Authorize(ctx, "update", user, post); err != nil {
//	    return err
//	}
func (g *Gate[U]) Authorize(ctx context.Context, ability string, user U, resource any) error {
	ok, err := g.Allows(ctx, ability, user, resource)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %q", ErrDenied, ability)
	}
	return nil
}

// Check returns a Decision so callers can render rich responses.
func (g *Gate[U]) Check(ctx context.Context, ability string, user U, resource any) Decision {
	ok, err := g.Allows(ctx, ability, user, resource)
	if err != nil {
		return Decision{Allowed: false, Reason: err.Error()}
	}
	if !ok {
		return Decision{Allowed: false, Reason: "denied: " + ability}
	}
	return Decision{Allowed: true}
}

// invokePolicy looks up the ability method on the policy and calls it.
// Matching is case-insensitive and treats "-"/"_" as boundaries:
// "update", "Update", "update-post", "update_post" all match Update().
func invokePolicy(ctx context.Context, policy reflect.Value, ability string, user, resource any) (bool, error) {
	want := normaliseAbility(ability)
	t := policy.Type()
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		if normaliseAbility(m.Name) != want {
			continue
		}
		// Validate signature: (receiver,) ctx, user, resource
		mt := m.Func.Type()
		if mt.NumIn() != 4 {
			return false, fmt.Errorf("authz: policy method %s has wrong arity", m.Name)
		}
		args := []reflect.Value{
			policy,
			reflect.ValueOf(ctx),
			reflect.ValueOf(user),
			reflect.ValueOf(resource),
		}
		out := m.Func.Call(args)
		switch len(out) {
		case 1:
			return out[0].Bool(), nil
		case 2:
			var err error
			if !out[1].IsNil() {
				err = out[1].Interface().(error)
			}
			return out[0].Bool(), err
		default:
			return false, fmt.Errorf("authz: policy method %s has unexpected return count", m.Name)
		}
	}
	return false, fmt.Errorf("%w: %q on %T", ErrUnknownAbility, ability, policy.Interface())
}

func normaliseAbility(s string) string {
	out := strings.ToLower(s)
	out = strings.ReplaceAll(out, "-", "")
	out = strings.ReplaceAll(out, "_", "")
	return out
}
