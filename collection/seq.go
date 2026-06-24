package collection

import "iter"

// Seq returns an iter.Seq[T] that yields each element in order, enabling
// range-over-func interop:
//
//	for v := range c.Seq() {
//		use(v)
//	}
//
// The sequence iterates the live backing array; do not mutate the collection
// (e.g. via concurrent Push results) while ranging.
func (c Collection[T]) Seq() iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, v := range c.items {
			if !yield(v) {
				return
			}
		}
	}
}

// Seq2 returns an iter.Seq2[int, T] yielding (index, value) pairs in order.
func (c Collection[T]) Seq2() iter.Seq2[int, T] {
	return func(yield func(int, T) bool) {
		for i, v := range c.items {
			if !yield(i, v) {
				return
			}
		}
	}
}

// FromSeq drains an iter.Seq[T] into a new Collection.
func FromSeq[T any](seq iter.Seq[T]) Collection[T] {
	var items []T
	for v := range seq {
		items = append(items, v)
	}
	return Collection[T]{items: items}
}
