package collection_test

import (
	"fmt"

	"github.com/devituz/lagodev/collection"
)

func ExampleCollection() {
	result := collection.New(1, 2, 3, 4, 5, 6).
		Filter(func(n int) bool { return n%2 == 0 }).
		Map(func(n int) int { return n * n }).
		SortBy(func(n int) int { return -n }).
		Take(2).
		All()

	fmt.Println(result)
	// Output: [36 16]
}

func ExampleGroupBy() {
	type product struct {
		Name     string
		Category string
	}
	products := collection.New(
		product{"Pen", "office"},
		product{"Desk", "furniture"},
		product{"Stapler", "office"},
	)

	byCat := collection.GroupBy(products, func(p product) string { return p.Category })
	names := collection.Map(byCat["office"], func(p product) string { return p.Name })

	fmt.Println(names.All())
	// Output: [Pen Stapler]
}
