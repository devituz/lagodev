// Package collection provides a type-safe, ergonomic wrapper over Go slices,
// modelled on Laravel's Collections but idiomatic Go. It leans on generics and
// favours immutability: chainable methods return a new Collection instead of
// mutating the receiver's backing array.
//
// Because Go methods cannot introduce new type parameters, the API is split in
// two:
//
//   - Same-type operations are methods on Collection[T] and return
//     Collection[T] so they chain: Filter, Reject, Map, Sort, Take, ...
//   - Type-changing operations are free functions (Map, FlatMap, Reduce,
//     GroupBy, KeyBy, Pluck, ...) because the result element type differs from T.
//
// Usage:
//
//	nums := collection.New(1, 2, 3, 4, 5, 6)
//	even := nums.
//		Filter(func(n int) bool { return n%2 == 0 }).
//		SortDesc().
//		Take(2).
//		All() // []int{6, 4}
//
//	type User struct {
//		Name string
//		Age  int
//	}
//	users := collection.New(
//		User{"Ada", 36}, User{"Linus", 54}, User{"Grace", 36},
//	)
//	byAge := collection.GroupBy(users, func(u User) int { return u.Age })
//	names := collection.Map(users, func(u User) string { return u.Name }).All()
//
// Immutability: methods that reshape the collection (Filter, Map, Sort,
// Reverse, ...) copy the backing slice so the original Collection and any slice
// passed to From is never modified. The only mutating helpers are Push and
// Prepend, and even those return a fresh Collection. Each and Tap iterate
// without copying and are the only way to observe elements in place.
package collection

// Collection wraps a slice of T and provides chainable, mostly-immutable
// operations over it. The zero value is an empty, ready-to-use collection.
type Collection[T any] struct {
	items []T
}

// New builds a Collection from the given variadic items.
func New[T any](items ...T) Collection[T] {
	return Collection[T]{items: items}
}

// From wraps an existing slice. The slice is copied so later mutations of the
// argument do not leak into the Collection (and vice versa).
func From[T any](items []T) Collection[T] {
	return Collection[T]{items: clone(items)}
}

// Collect is an alias for From, mirroring Laravel's collect() helper.
func Collect[T any](items []T) Collection[T] {
	return From(items)
}

// clone returns a shallow copy of s. A nil slice clones to nil.
func clone[T any](s []T) []T {
	if s == nil {
		return nil
	}
	out := make([]T, len(s))
	copy(out, s)
	return out
}
