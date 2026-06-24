package collection

import "sort"

// All returns the underlying slice. The returned slice is a copy, so callers
// may mutate it freely without affecting the Collection. An empty collection
// yields a non-nil, zero-length slice.
func (c Collection[T]) All() []T {
	out := make([]T, len(c.items))
	copy(out, c.items)
	return out
}

// Slice is an alias for All.
func (c Collection[T]) Slice() []T {
	return c.All()
}

// Len returns the number of elements.
func (c Collection[T]) Len() int { return len(c.items) }

// Count is an alias for Len.
func (c Collection[T]) Count() int { return len(c.items) }

// IsEmpty reports whether the collection has no elements.
func (c Collection[T]) IsEmpty() bool { return len(c.items) == 0 }

// IsNotEmpty reports whether the collection has at least one element.
func (c Collection[T]) IsNotEmpty() bool { return len(c.items) > 0 }

// Get returns the element at index i and whether the index was in range.
func (c Collection[T]) Get(i int) (T, bool) {
	if i < 0 || i >= len(c.items) {
		var zero T
		return zero, false
	}
	return c.items[i], true
}

// First returns the first element and true, or the zero value and false when
// the collection is empty.
func (c Collection[T]) First() (T, bool) {
	if len(c.items) == 0 {
		var zero T
		return zero, false
	}
	return c.items[0], true
}

// Last returns the last element and true, or the zero value and false when the
// collection is empty.
func (c Collection[T]) Last() (T, bool) {
	if len(c.items) == 0 {
		var zero T
		return zero, false
	}
	return c.items[len(c.items)-1], true
}

// Filter returns a new Collection containing only the elements for which keep
// returns true.
func (c Collection[T]) Filter(keep func(T) bool) Collection[T] {
	out := make([]T, 0, len(c.items))
	for _, v := range c.items {
		if keep(v) {
			out = append(out, v)
		}
	}
	return Collection[T]{items: out}
}

// Reject is the inverse of Filter: it keeps elements for which drop returns
// false.
func (c Collection[T]) Reject(drop func(T) bool) Collection[T] {
	return c.Filter(func(v T) bool { return !drop(v) })
}

// Each invokes fn for every element, in order, passing the index and value. It
// returns the receiver unchanged so it can sit in a chain. fn observes the
// elements in place; do not retain pointers into the backing array.
func (c Collection[T]) Each(fn func(i int, v T)) Collection[T] {
	for i, v := range c.items {
		fn(i, v)
	}
	return c
}

// Tap invokes fn with the whole collection (for side effects such as logging or
// debugging) and returns the receiver unchanged.
func (c Collection[T]) Tap(fn func(Collection[T])) Collection[T] {
	fn(c)
	return c
}

// Map applies fn to every element and returns a new Collection of the same
// type. For transformations that change the element type, use the free function
// Map instead.
func (c Collection[T]) Map(fn func(T) T) Collection[T] {
	out := make([]T, len(c.items))
	for i, v := range c.items {
		out[i] = fn(v)
	}
	return Collection[T]{items: out}
}

// UniqueBy returns a new Collection with duplicates removed, where uniqueness is
// determined by the comparable key returned by keyFn. The first occurrence of
// each key is kept and original order is preserved. For comparable element
// types, use the package-level Unique function.
func (c Collection[T]) UniqueBy(keyFn func(T) any) Collection[T] {
	seen := make(map[any]struct{}, len(c.items))
	out := make([]T, 0, len(c.items))
	for _, v := range c.items {
		k := keyFn(v)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, v)
	}
	return Collection[T]{items: out}
}

// Sort returns a new Collection sorted in ascending order using less. The
// original collection is left untouched.
func (c Collection[T]) Sort(less func(a, b T) bool) Collection[T] {
	out := clone(c.items)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return Collection[T]{items: out}
}

// SortBy returns a new Collection sorted ascending by the integer key produced
// by keyFn. For keys of other ordered types, use the package-level SortByKey.
func (c Collection[T]) SortBy(keyFn func(T) int) Collection[T] {
	return c.Sort(func(a, b T) bool { return keyFn(a) < keyFn(b) })
}

// Reverse returns a new Collection with the elements in reverse order.
func (c Collection[T]) Reverse() Collection[T] {
	n := len(c.items)
	out := make([]T, n)
	for i, v := range c.items {
		out[n-1-i] = v
	}
	return Collection[T]{items: out}
}

// Take returns a new Collection with the first n elements. A negative n takes
// the last |n| elements. n larger than the length takes everything.
func (c Collection[T]) Take(n int) Collection[T] {
	if n < 0 {
		n = -n
		if n > len(c.items) {
			n = len(c.items)
		}
		return Collection[T]{items: clone(c.items[len(c.items)-n:])}
	}
	if n > len(c.items) {
		n = len(c.items)
	}
	return Collection[T]{items: clone(c.items[:n])}
}

// Skip returns a new Collection with the first n elements dropped.
func (c Collection[T]) Skip(n int) Collection[T] {
	if n < 0 {
		n = 0
	}
	if n > len(c.items) {
		n = len(c.items)
	}
	return Collection[T]{items: clone(c.items[n:])}
}

// Slicing returns a new Collection covering the half-open range [start, start+length).
// A negative start counts from the end. A negative length means "to the end".
// Out-of-range bounds are clamped.
func (c Collection[T]) Slicing(start, length int) Collection[T] {
	n := len(c.items)
	if start < 0 {
		start += n
	}
	if start < 0 {
		start = 0
	}
	if start > n {
		start = n
	}
	end := n
	if length >= 0 {
		end = start + length
		if end > n {
			end = n
		}
	}
	return Collection[T]{items: clone(c.items[start:end])}
}

// Chunk splits the collection into consecutive groups of at most size elements,
// returned as [][]T. A size <= 0 yields a nil result. Each chunk is an
// independent copy of the underlying data.
//
// [][]T is used rather than Collection[Collection[T]] because instantiating a
// Collection with itself as its type argument triggers a generic instantiation
// cycle. Wrap the result with ChunkInto if a Collection-of-Collections is
// needed at a call site.
func (c Collection[T]) Chunk(size int) [][]T {
	if size <= 0 {
		return nil
	}
	var chunks [][]T
	for i := 0; i < len(c.items); i += size {
		end := i + size
		if end > len(c.items) {
			end = len(c.items)
		}
		chunks = append(chunks, clone(c.items[i:end]))
	}
	return chunks
}

// Push returns a new Collection with the given items appended.
func (c Collection[T]) Push(items ...T) Collection[T] {
	out := make([]T, 0, len(c.items)+len(items))
	out = append(out, c.items...)
	out = append(out, items...)
	return Collection[T]{items: out}
}

// Prepend returns a new Collection with the given items inserted at the front,
// in the order supplied.
func (c Collection[T]) Prepend(items ...T) Collection[T] {
	out := make([]T, 0, len(c.items)+len(items))
	out = append(out, items...)
	out = append(out, c.items...)
	return Collection[T]{items: out}
}

// Merge returns a new Collection with the elements of other appended after the
// receiver's elements.
func (c Collection[T]) Merge(other Collection[T]) Collection[T] {
	return c.Push(other.items...)
}

// Partition splits the collection into two: the first holds elements for which
// pred returns true, the second holds the rest. Order is preserved within each.
func (c Collection[T]) Partition(pred func(T) bool) (Collection[T], Collection[T]) {
	var yes, no []T
	for _, v := range c.items {
		if pred(v) {
			yes = append(yes, v)
		} else {
			no = append(no, v)
		}
	}
	return Collection[T]{items: yes}, Collection[T]{items: no}
}

// ContainsBy reports whether any element satisfies pred.
func (c Collection[T]) ContainsBy(pred func(T) bool) bool {
	for _, v := range c.items {
		if pred(v) {
			return true
		}
	}
	return false
}

// FirstWhere returns the first element satisfying pred and true, or the zero
// value and false when none match.
func (c Collection[T]) FirstWhere(pred func(T) bool) (T, bool) {
	for _, v := range c.items {
		if pred(v) {
			return v, true
		}
	}
	var zero T
	return zero, false
}
