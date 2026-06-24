package search_test

import (
	"context"
	"fmt"

	"github.com/devituz/lagodev/search"
)

// ExampleMemory indexes a handful of documents into the in-memory engine and
// runs a query, printing the ranked result IDs. Ranking is by term-frequency
// overlap; ties break on ID, so the order is deterministic.
func ExampleMemory() {
	ctx := context.Background()
	eng := search.NewMemory()

	_ = eng.Index(ctx, "posts",
		search.Doc("1", map[string]any{"title": "Hello World", "body": "first post"}),
		search.Doc("2", map[string]any{"title": "Goodbye World", "body": "last post"}),
		search.Doc("3", map[string]any{"title": "World World World", "body": "all about the world"}),
	)

	res, err := eng.Search(ctx, "posts", "world", search.Options{})
	if err != nil {
		fmt.Println("search:", err)
		return
	}

	fmt.Println("total:", res.Total)
	for _, hit := range res.Hits {
		fmt.Printf("%s score=%.0f\n", hit.ID, hit.Score)
	}

	// Output:
	// total: 3
	// 3 score=4
	// 1 score=1
	// 2 score=1
}
