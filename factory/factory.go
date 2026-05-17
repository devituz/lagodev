// Package factory provides a generic, type-safe model factory inspired by
// Laravel's factories. Factories generate fake model instances and can
// persist them via orm.Save. States layer transformations on top of the
// default definition.
package factory

import (
	"context"
	"errors"
	"fmt"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/orm"
)

// DefinitionFn returns a freshly-built model instance.
type DefinitionFn[T any] func(f *Faker) T

// StateFn mutates a model in place.
type StateFn[T any] func(f *Faker, m *T)

// Factory builds and persists T instances.
type Factory[T any] struct {
	conn       *database.Connection
	def        DefinitionFn[T]
	states     []StateFn[T]
	overrides  []func(m *T)
	count      int
	beforeSave []func(m *T)
	afterMake  []func(m *T)
	afterCreate []func(ctx context.Context, m *T) error
	faker      *Faker
}

// New constructs a Factory using a definition function.
func New[T any](conn *database.Connection, def DefinitionFn[T]) *Factory[T] {
	return &Factory[T]{conn: conn, def: def, count: 1, faker: NewFaker()}
}

// Count sets how many instances to build.
func (f *Factory[T]) Count(n int) *Factory[T] { f.count = n; return f }

// State applies an inline state transformer to every produced instance.
func (f *Factory[T]) State(fn StateFn[T]) *Factory[T] {
	f.states = append(f.states, fn)
	return f
}

// Set is a convenience: overrides a specific field via callback. Useful for
// pinning a relation: factory.User().Set(func(u *User) { u.TenantID = 1 }).
func (f *Factory[T]) Set(fn func(*T)) *Factory[T] {
	f.overrides = append(f.overrides, fn)
	return f
}

// AfterMake runs after construction but before persistence.
func (f *Factory[T]) AfterMake(fn func(*T)) *Factory[T] {
	f.afterMake = append(f.afterMake, fn)
	return f
}

// AfterCreate runs after persistence.
func (f *Factory[T]) AfterCreate(fn func(context.Context, *T) error) *Factory[T] {
	f.afterCreate = append(f.afterCreate, fn)
	return f
}

// Make builds instances without persisting.
func (f *Factory[T]) Make() []T {
	if f.count <= 0 {
		f.count = 1
	}
	out := make([]T, 0, f.count)
	for i := 0; i < f.count; i++ {
		m := f.def(f.faker)
		for _, s := range f.states {
			s(f.faker, &m)
		}
		for _, o := range f.overrides {
			o(&m)
		}
		for _, am := range f.afterMake {
			am(&m)
		}
		out = append(out, m)
	}
	return out
}

// MakeOne builds a single instance.
func (f *Factory[T]) MakeOne() T {
	prev := f.count
	f.count = 1
	defer func() { f.count = prev }()
	return f.Make()[0]
}

// Create persists instances and returns them.
func (f *Factory[T]) Create(ctx context.Context) ([]T, error) {
	if f.conn == nil {
		return nil, errors.New("factory: no connection bound")
	}
	models := f.Make()
	for i := range models {
		if err := orm.Save(ctx, f.conn, &models[i]); err != nil {
			return models, fmt.Errorf("factory: create: %w", err)
		}
		for _, ac := range f.afterCreate {
			if err := ac(ctx, &models[i]); err != nil {
				return models, err
			}
		}
	}
	return models, nil
}

// CreateOne creates and returns one instance.
func (f *Factory[T]) CreateOne(ctx context.Context) (T, error) {
	prev := f.count
	f.count = 1
	defer func() { f.count = prev }()
	models, err := f.Create(ctx)
	var zero T
	if err != nil || len(models) == 0 {
		return zero, err
	}
	return models[0], nil
}
