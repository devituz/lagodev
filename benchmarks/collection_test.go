package benchmarks

import (
	"testing"

	"github.com/devituz/lagodev/collection"
)

type person struct {
	Name string
	Age  int
}

func samplePeople(n int) []person {
	out := make([]person, n)
	for i := range out {
		out[i] = person{Name: "Name", Age: 18 + i%60}
	}
	return out
}

// BenchmarkCollection_FilterMapSort measures a realistic immutable chain over
// a 1k-element collection: filter, sort, take. Each step copies the backing
// slice (the documented immutability contract), so this captures the cost of
// that ergonomic guarantee.
func BenchmarkCollection_FilterMapSort(b *testing.B) {
	people := samplePeople(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collection.From(people).
			Filter(func(p person) bool { return p.Age >= 30 }).
			SortBy(func(p person) int { return p.Age }).
			Take(10).
			All()
	}
}

// BenchmarkCollection_MapReduce measures the type-changing free functions:
// Map to ages, then Reduce to a sum.
func BenchmarkCollection_MapReduce(b *testing.B) {
	people := samplePeople(1000)
	c := collection.From(people)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ages := collection.Map(c, func(p person) int { return p.Age })
		_ = collection.Reduce(ages, 0, func(acc, v int) int { return acc + v })
	}
}

// BenchmarkCollection_GroupBy measures grouping 1k elements by a key.
func BenchmarkCollection_GroupBy(b *testing.B) {
	people := samplePeople(1000)
	c := collection.From(people)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = collection.GroupBy(c, func(p person) int { return p.Age })
	}
}
