package collection

import (
	"cmp"
	"sort"
)

// Number is the set of types over which arithmetic aggregation (Sum, Avg) is
// defined.
type Number interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
		~float32 | ~float64
}

// Map applies fn to every element of c and returns a new Collection whose
// element type is U. This is the type-changing counterpart to the same-type
// Collection.Map method.
func Map[T, U any](c Collection[T], fn func(T) U) Collection[U] {
	out := make([]U, len(c.items))
	for i, v := range c.items {
		out[i] = fn(v)
	}
	return Collection[U]{items: out}
}

// FlatMap applies fn to every element, where fn returns a slice, and
// concatenates all results into a single Collection[U].
func FlatMap[T, U any](c Collection[T], fn func(T) []U) Collection[U] {
	var out []U
	for _, v := range c.items {
		out = append(out, fn(v)...)
	}
	return Collection[U]{items: out}
}

// Reduce folds the collection into a single value of type U, starting from init
// and applying fn for each element in order.
func Reduce[T, U any](c Collection[T], init U, fn func(acc U, v T) U) U {
	acc := init
	for _, v := range c.items {
		acc = fn(acc, v)
	}
	return acc
}

// GroupBy partitions the collection into a map keyed by keyFn. Each bucket is a
// Collection preserving the original relative order of its elements.
func GroupBy[T any, K comparable](c Collection[T], keyFn func(T) K) map[K]Collection[T] {
	out := make(map[K]Collection[T])
	for _, v := range c.items {
		k := keyFn(v)
		grp := out[k]
		grp.items = append(grp.items, v)
		out[k] = grp
	}
	return out
}

// KeyBy indexes the collection by keyFn. When two elements share a key, the
// later element wins (matching Laravel's keyBy semantics).
func KeyBy[T any, K comparable](c Collection[T], keyFn func(T) K) map[K]T {
	out := make(map[K]T, len(c.items))
	for _, v := range c.items {
		out[keyFn(v)] = v
	}
	return out
}

// Pluck extracts a single derived value from each element and returns them as a
// Collection[U], preserving order.
func Pluck[T, U any](c Collection[T], fn func(T) U) Collection[U] {
	return Map(c, fn)
}

// Associate builds a map from the collection by deriving a key/value pair from
// each element. Later elements overwrite earlier ones on key collision.
func Associate[T any, K comparable, V any](c Collection[T], fn func(T) (K, V)) map[K]V {
	out := make(map[K]V, len(c.items))
	for _, v := range c.items {
		k, val := fn(v)
		out[k] = val
	}
	return out
}

// ChunkInto splits c into groups of at most size and returns each group wrapped
// in its own Collection, all held by an outer Collection. This is the
// free-function form of Chunk; it lives outside the Collection method set to
// avoid the generic instantiation cycle that Collection[Collection[T]] would
// cause if exposed as a method. A size <= 0 yields an empty outer Collection.
func ChunkInto[T any](c Collection[T], size int) Collection[Collection[T]] {
	raw := c.Chunk(size)
	groups := make([]Collection[T], len(raw))
	for i, g := range raw {
		groups[i] = Collection[T]{items: g}
	}
	return Collection[Collection[T]]{items: groups}
}

// Unique returns a new Collection with duplicate elements removed. The first
// occurrence of each value is kept and order is preserved. T must be comparable.
func Unique[T comparable](c Collection[T]) Collection[T] {
	seen := make(map[T]struct{}, len(c.items))
	out := make([]T, 0, len(c.items))
	for _, v := range c.items {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return Collection[T]{items: out}
}

// Contains reports whether the collection holds target. T must be comparable;
// for non-comparable types or custom predicates use Collection.ContainsBy.
func Contains[T comparable](c Collection[T], target T) bool {
	for _, v := range c.items {
		if v == target {
			return true
		}
	}
	return false
}

// SortOrdered returns a new Collection sorted ascending using the natural order
// of an ordered element type. The original collection is left untouched.
func SortOrdered[T cmp.Ordered](c Collection[T]) Collection[T] {
	out := clone(c.items)
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return Collection[T]{items: out}
}

// SortByKey returns a new Collection sorted ascending by the ordered key
// produced by keyFn.
func SortByKey[T any, K cmp.Ordered](c Collection[T], keyFn func(T) K) Collection[T] {
	out := clone(c.items)
	sort.SliceStable(out, func(i, j int) bool { return keyFn(out[i]) < keyFn(out[j]) })
	return Collection[T]{items: out}
}

// Sum returns the arithmetic sum of all elements. The sum of an empty
// collection is the zero value of T.
func Sum[T Number](c Collection[T]) T {
	var total T
	for _, v := range c.items {
		total += v
	}
	return total
}

// SumBy sums the numeric value derived from each element by fn.
func SumBy[T any, N Number](c Collection[T], fn func(T) N) N {
	var total N
	for _, v := range c.items {
		total += fn(v)
	}
	return total
}

// Avg returns the mean of the elements as a float64 and true. For an empty
// collection it returns 0 and false.
func Avg[T Number](c Collection[T]) (float64, bool) {
	if len(c.items) == 0 {
		return 0, false
	}
	return float64(Sum(c)) / float64(len(c.items)), true
}

// Min returns the smallest element and true, or the zero value and false when
// the collection is empty.
func Min[T cmp.Ordered](c Collection[T]) (T, bool) {
	if len(c.items) == 0 {
		var zero T
		return zero, false
	}
	m := c.items[0]
	for _, v := range c.items[1:] {
		if v < m {
			m = v
		}
	}
	return m, true
}

// Max returns the largest element and true, or the zero value and false when
// the collection is empty.
func Max[T cmp.Ordered](c Collection[T]) (T, bool) {
	if len(c.items) == 0 {
		var zero T
		return zero, false
	}
	m := c.items[0]
	for _, v := range c.items[1:] {
		if v > m {
			m = v
		}
	}
	return m, true
}
