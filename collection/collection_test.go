package collection_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/devituz/lagodev/collection"
)

type user struct {
	Name string
	Age  int
}

func eq[T any](t *testing.T, got, want []T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestConstructors(t *testing.T) {
	eq(t, collection.New(1, 2, 3).All(), []int{1, 2, 3})
	eq(t, collection.New[int]().All(), []int{})
	eq(t, collection.From([]int{1, 2}).All(), []int{1, 2})
	eq(t, collection.Collect([]string{"a"}).All(), []string{"a"})

	// From copies the input slice: mutating the source must not leak in.
	src := []int{1, 2, 3}
	c := collection.From(src)
	src[0] = 99
	eq(t, c.All(), []int{1, 2, 3})
}

func TestFilterReject(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want []int
	}{
		{"empty", nil, []int{}},
		{"single keep", []int{2}, []int{2}},
		{"single drop", []int{1}, []int{}},
		{"many", []int{1, 2, 3, 4, 5, 6}, []int{2, 4, 6}},
	}
	even := func(n int) bool { return n%2 == 0 }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, collection.From(tt.in).Filter(even).All(), tt.want)
			// Reject is the inverse.
			rej := collection.From(tt.in).Reject(even).All()
			var wantRej []int
			for _, v := range tt.in {
				if !even(v) {
					wantRej = append(wantRej, v)
				}
			}
			if wantRej == nil {
				wantRej = []int{}
			}
			eq(t, rej, wantRej)
		})
	}
}

func TestFilterImmutability(t *testing.T) {
	orig := collection.New(1, 2, 3, 4)
	_ = orig.Filter(func(n int) bool { return n > 2 })
	eq(t, orig.All(), []int{1, 2, 3, 4})
}

func TestMapMethod(t *testing.T) {
	orig := collection.New(1, 2, 3)
	got := orig.Map(func(n int) int { return n * 10 })
	eq(t, got.All(), []int{10, 20, 30})
	// original unchanged
	eq(t, orig.All(), []int{1, 2, 3})
}

func TestMapFunc(t *testing.T) {
	c := collection.New(1, 2, 3)
	got := collection.Map(c, func(n int) string {
		return []string{"", "one", "two", "three"}[n]
	})
	eq(t, got.All(), []string{"one", "two", "three"})
	// empty
	eq(t, collection.Map(collection.New[int](), func(n int) string { return "" }).All(), []string{})
}

func TestFlatMap(t *testing.T) {
	c := collection.New(1, 2, 3)
	got := collection.FlatMap(c, func(n int) []int { return []int{n, n} })
	eq(t, got.All(), []int{1, 1, 2, 2, 3, 3})
	// empty
	if !collection.FlatMap(collection.New[int](), func(n int) []int { return nil }).IsEmpty() {
		t.Fatal("expected empty")
	}
}

func TestReduce(t *testing.T) {
	c := collection.New(1, 2, 3, 4)
	sum := collection.Reduce(c, 0, func(acc, v int) int { return acc + v })
	if sum != 10 {
		t.Fatalf("sum=%d", sum)
	}
	// type-changing reduce: int -> string concat length
	cat := collection.Reduce(collection.New("a", "bb", "ccc"), 0, func(acc int, v string) int { return acc + len(v) })
	if cat != 6 {
		t.Fatalf("cat=%d", cat)
	}
	// empty returns init
	if collection.Reduce(collection.New[int](), 42, func(a, v int) int { return a + v }) != 42 {
		t.Fatal("empty reduce should return init")
	}
}

func TestGroupBy(t *testing.T) {
	users := collection.New(
		user{"Ada", 36}, user{"Linus", 54}, user{"Grace", 36},
	)
	got := collection.GroupBy(users, func(u user) int { return u.Age })
	if got[36].Len() != 2 || got[54].Len() != 2-1 {
		t.Fatalf("group sizes wrong: %v", got)
	}
	eq(t, got[36].All(), []user{{"Ada", 36}, {"Grace", 36}})
	// empty
	if len(collection.GroupBy(collection.New[user](), func(u user) int { return u.Age })) != 0 {
		t.Fatal("expected empty group map")
	}
}

func TestKeyBy(t *testing.T) {
	users := collection.New(user{"Ada", 36}, user{"Linus", 54})
	got := collection.KeyBy(users, func(u user) string { return u.Name })
	if got["Ada"].Age != 36 || got["Linus"].Age != 54 {
		t.Fatalf("keyby wrong: %v", got)
	}
	// collision: later wins
	dup := collection.New(user{"X", 1}, user{"X", 2})
	if collection.KeyBy(dup, func(u user) string { return u.Name })["X"].Age != 2 {
		t.Fatal("later element should win on collision")
	}
}

func TestPluckAssociate(t *testing.T) {
	users := collection.New(user{"Ada", 36}, user{"Linus", 54})
	names := collection.Pluck(users, func(u user) string { return u.Name })
	eq(t, names.All(), []string{"Ada", "Linus"})

	m := collection.Associate(users, func(u user) (string, int) { return u.Name, u.Age })
	if m["Ada"] != 36 || m["Linus"] != 54 {
		t.Fatalf("associate wrong: %v", m)
	}
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want []int
	}{
		{"empty", nil, []int{}},
		{"single", []int{1}, []int{1}},
		{"dups", []int{1, 2, 2, 3, 1, 4}, []int{1, 2, 3, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, collection.Unique(collection.From(tt.in)).All(), tt.want)
		})
	}
}

func TestUniqueBy(t *testing.T) {
	users := collection.New(user{"Ada", 36}, user{"Grace", 36}, user{"Linus", 54})
	got := collection.From(users.All()).UniqueBy(func(u user) any { return u.Age })
	eq(t, got.All(), []user{{"Ada", 36}, {"Linus", 54}})
}

func TestSort(t *testing.T) {
	orig := collection.New(3, 1, 2)
	got := orig.Sort(func(a, b int) bool { return a < b })
	eq(t, got.All(), []int{1, 2, 3})
	// immutability: original untouched
	eq(t, orig.All(), []int{3, 1, 2})

	// SortOrdered / SortByKey free funcs
	eq(t, collection.SortOrdered(collection.New(3, 1, 2)).All(), []int{1, 2, 3})
	users := collection.New(user{"Ada", 36}, user{"Linus", 54}, user{"Grace", 30})
	byAge := collection.SortByKey(users, func(u user) int { return u.Age })
	eq(t, byAge.All(), []user{{"Grace", 30}, {"Ada", 36}, {"Linus", 54}})
}

func TestSortByMethod(t *testing.T) {
	users := collection.New(user{"Ada", 36}, user{"Grace", 30})
	eq(t, users.SortBy(func(u user) int { return u.Age }).All(),
		[]user{{"Grace", 30}, {"Ada", 36}})
}

func TestReverse(t *testing.T) {
	orig := collection.New(1, 2, 3)
	eq(t, orig.Reverse().All(), []int{3, 2, 1})
	eq(t, orig.All(), []int{1, 2, 3})
	eq(t, collection.New[int]().Reverse().All(), []int{})
}

func TestTakeSkip(t *testing.T) {
	c := collection.New(1, 2, 3, 4, 5)
	eq(t, c.Take(2).All(), []int{1, 2})
	eq(t, c.Take(-2).All(), []int{4, 5})
	eq(t, c.Take(99).All(), []int{1, 2, 3, 4, 5})
	eq(t, c.Take(0).All(), []int{})
	eq(t, c.Skip(2).All(), []int{3, 4, 5})
	eq(t, c.Skip(99).All(), []int{})
	eq(t, c.Skip(-1).All(), []int{1, 2, 3, 4, 5})
}

func TestSlicing(t *testing.T) {
	c := collection.New(1, 2, 3, 4, 5)
	eq(t, c.Slicing(1, 2).All(), []int{2, 3})
	eq(t, c.Slicing(1, -1).All(), []int{2, 3, 4, 5})
	eq(t, c.Slicing(-2, -1).All(), []int{4, 5})
	eq(t, c.Slicing(10, 2).All(), []int{})
}

func TestChunk(t *testing.T) {
	c := collection.New(1, 2, 3, 4, 5)
	got := c.Chunk(2)
	want := [][]int{{1, 2}, {3, 4}, {5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunk got %v want %v", got, want)
	}
	if c.Chunk(0) != nil {
		t.Fatal("chunk(0) should be nil")
	}
	// ChunkInto wraps into collections
	wrapped := collection.ChunkInto(c, 2)
	if wrapped.Len() != 3 {
		t.Fatalf("ChunkInto len=%d", wrapped.Len())
	}
	first, _ := wrapped.First()
	eq(t, first.All(), []int{1, 2})
}

func TestPushPrependMerge(t *testing.T) {
	c := collection.New(2, 3)
	eq(t, c.Push(4, 5).All(), []int{2, 3, 4, 5})
	eq(t, c.Prepend(0, 1).All(), []int{0, 1, 2, 3})
	eq(t, c.Merge(collection.New(4, 5)).All(), []int{2, 3, 4, 5})
	// original untouched
	eq(t, c.All(), []int{2, 3})
}

func TestPartition(t *testing.T) {
	c := collection.New(1, 2, 3, 4, 5, 6)
	even, odd := c.Partition(func(n int) bool { return n%2 == 0 })
	eq(t, even.All(), []int{2, 4, 6})
	eq(t, odd.All(), []int{1, 3, 5})
}

func TestTerminals(t *testing.T) {
	c := collection.New(10, 20, 30)
	if c.Len() != 3 || c.Count() != 3 {
		t.Fatal("len/count")
	}
	if c.IsEmpty() || !c.IsNotEmpty() {
		t.Fatal("empty flags")
	}
	if v, ok := c.Get(1); !ok || v != 20 {
		t.Fatal("get")
	}
	if _, ok := c.Get(9); ok {
		t.Fatal("get out of range")
	}
	if v, ok := c.First(); !ok || v != 10 {
		t.Fatal("first")
	}
	if v, ok := c.Last(); !ok || v != 30 {
		t.Fatal("last")
	}

	empty := collection.New[int]()
	if _, ok := empty.First(); ok {
		t.Fatal("first empty")
	}
	if _, ok := empty.Last(); ok {
		t.Fatal("last empty")
	}
	if !empty.IsEmpty() {
		t.Fatal("empty")
	}
}

func TestContains(t *testing.T) {
	c := collection.New(1, 2, 3)
	if !collection.Contains(c, 2) || collection.Contains(c, 9) {
		t.Fatal("contains")
	}
	if !c.ContainsBy(func(n int) bool { return n > 2 }) {
		t.Fatal("containsBy")
	}
	if v, ok := c.FirstWhere(func(n int) bool { return n > 1 }); !ok || v != 2 {
		t.Fatal("firstWhere")
	}
	if _, ok := c.FirstWhere(func(n int) bool { return n > 99 }); ok {
		t.Fatal("firstWhere none")
	}
}

func TestSumAvg(t *testing.T) {
	c := collection.New(1, 2, 3, 4)
	if collection.Sum(c) != 10 {
		t.Fatal("sum")
	}
	if avg, ok := collection.Avg(c); !ok || avg != 2.5 {
		t.Fatalf("avg=%v ok=%v", avg, ok)
	}
	if _, ok := collection.Avg(collection.New[int]()); ok {
		t.Fatal("avg empty should be false")
	}
	if collection.Sum(collection.New[int]()) != 0 {
		t.Fatal("sum empty")
	}
	// SumBy
	users := collection.New(user{"A", 1}, user{"B", 2})
	if collection.SumBy(users, func(u user) int { return u.Age }) != 3 {
		t.Fatal("sumBy")
	}
	// float
	if collection.Sum(collection.New(1.5, 2.5)) != 4.0 {
		t.Fatal("sum float")
	}
}

func TestMinMax(t *testing.T) {
	c := collection.New(3, 1, 4, 1, 5, 9, 2)
	if v, ok := collection.Min(c); !ok || v != 1 {
		t.Fatal("min")
	}
	if v, ok := collection.Max(c); !ok || v != 9 {
		t.Fatal("max")
	}
	if _, ok := collection.Min(collection.New[int]()); ok {
		t.Fatal("min empty")
	}
	if _, ok := collection.Max(collection.New[int]()); ok {
		t.Fatal("max empty")
	}
	// strings are ordered too
	if v, _ := collection.Min(collection.New("b", "a", "c")); v != "a" {
		t.Fatal("min string")
	}
}

func TestEachTap(t *testing.T) {
	c := collection.New(1, 2, 3)
	var sum, idxSum int
	ret := c.Each(func(i, v int) { sum += v; idxSum += i })
	if sum != 6 || idxSum != 3 {
		t.Fatalf("each sum=%d idx=%d", sum, idxSum)
	}
	// Each returns receiver for chaining
	eq(t, ret.All(), []int{1, 2, 3})

	var tapped int
	c.Tap(func(cc collection.Collection[int]) { tapped = cc.Len() })
	if tapped != 3 {
		t.Fatal("tap")
	}
}

func TestChaining(t *testing.T) {
	got := collection.New(1, 2, 3, 4, 5, 6).
		Filter(func(n int) bool { return n%2 == 0 }).
		Map(func(n int) int { return n * n }).
		Reverse().
		Take(2).
		All()
	eq(t, got, []int{36, 16})
}

func TestSeqBridge(t *testing.T) {
	c := collection.New(1, 2, 3)
	var collected []int
	for v := range c.Seq() {
		collected = append(collected, v)
	}
	eq(t, collected, []int{1, 2, 3})

	// early break
	var partial []int
	for v := range c.Seq() {
		partial = append(partial, v)
		if v == 2 {
			break
		}
	}
	eq(t, partial, []int{1, 2})

	// Seq2 indices
	var idxs []int
	for i := range c.Seq2() {
		idxs = append(idxs, i)
	}
	eq(t, idxs, []int{0, 1, 2})

	// FromSeq round-trips
	back := collection.FromSeq(c.Seq())
	eq(t, back.All(), []int{1, 2, 3})

	// FromSeq from slices.Values
	fromStd := collection.FromSeq(slices.Values([]int{7, 8, 9}))
	eq(t, fromStd.All(), []int{7, 8, 9})
}
