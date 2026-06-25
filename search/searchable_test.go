package search_test

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/devituz/lagodev/search"
)

// post is a minimal Searchable model used to exercise the Indexer against the
// Memory engine. It mirrors how an ORM model would project itself into a
// Document without importing the orm package.
type post struct {
	ID    uint64
	Title string
	Body  string
}

func (p post) SearchIndex() string { return "posts" }

func (p post) SearchDocument() search.Document {
	return search.Doc(strconv.FormatUint(p.ID, 10), map[string]any{
		"title": p.Title,
		"body":  p.Body,
	})
}

func TestIndexerIndexAndSearch(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	if err := ix.Index(ctx, post{ID: 1, Title: "Hello World", Body: "first post"}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	res, err := ix.Engine().Search(ctx, "posts", "hello", search.Options{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 || len(res.Hits) != 1 {
		t.Fatalf("want 1 hit, got total=%d hits=%d", res.Total, len(res.Hits))
	}
	if res.Hits[0].ID != "1" {
		t.Fatalf("want hit ID 1, got %q", res.Hits[0].ID)
	}
}

func TestIndexerReindexReplaces(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	if err := ix.Index(ctx, post{ID: 1, Title: "Hello", Body: "old"}); err != nil {
		t.Fatalf("Index: %v", err)
	}
	// Re-index the same ID with different content: the old token must be gone.
	if err := ix.Index(ctx, post{ID: 1, Title: "Goodbye", Body: "new"}); err != nil {
		t.Fatalf("re-Index: %v", err)
	}

	res, _ := ix.Engine().Search(ctx, "posts", "hello", search.Options{})
	if res.Total != 0 {
		t.Fatalf("stale token still indexed: total=%d", res.Total)
	}
	res, _ = ix.Engine().Search(ctx, "posts", "goodbye", search.Options{})
	if res.Total != 1 {
		t.Fatalf("re-index not applied: total=%d", res.Total)
	}
}

func TestIndexerDelete(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	p := post{ID: 7, Title: "Ephemeral", Body: "gone soon"}
	if err := ix.Index(ctx, p); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := ix.Delete(ctx, "posts", "7"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	res, _ := ix.Engine().Search(ctx, "posts", "ephemeral", search.Options{})
	if res.Total != 0 {
		t.Fatalf("document not deleted: total=%d", res.Total)
	}
}

func TestIndexerDeleteModel(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	p := post{ID: 9, Title: "Vanish", Body: "poof"}
	if err := ix.Index(ctx, p); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := ix.DeleteModel(ctx, p); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}

	res, _ := ix.Engine().Search(ctx, "posts", "vanish", search.Options{})
	if res.Total != 0 {
		t.Fatalf("document not deleted by model: total=%d", res.Total)
	}
}

func TestIndexerBackfill(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	items := []search.Searchable{
		post{ID: 1, Title: "Alpha World", Body: "one"},
		post{ID: 2, Title: "Beta World", Body: "two"},
		post{ID: 3, Title: "Gamma World", Body: "three"},
	}
	if err := ix.Backfill(ctx, items...); err != nil {
		t.Fatalf("Backfill: %v", err)
	}

	res, err := ix.Engine().Search(ctx, "posts", "world", search.Options{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 3 {
		t.Fatalf("want 3 backfilled, got %d", res.Total)
	}
}

// other is a second Searchable model that lives in a different index, used to
// verify Backfill groups documents per index.
type other struct{ id string }

func (o other) SearchIndex() string { return "others" }
func (o other) SearchDocument() search.Document {
	return search.Doc(o.id, map[string]any{"name": "shared term"})
}

func TestIndexerBackfillMultiIndex(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	if err := ix.Backfill(ctx,
		post{ID: 1, Title: "shared term", Body: "p"},
		other{id: "x"},
	); err != nil {
		t.Fatalf("Backfill: %v", err)
	}

	if r, _ := ix.Engine().Search(ctx, "posts", "shared", search.Options{}); r.Total != 1 {
		t.Fatalf("posts index wrong: %d", r.Total)
	}
	if r, _ := ix.Engine().Search(ctx, "others", "shared", search.Options{}); r.Total != 1 {
		t.Fatalf("others index wrong: %d", r.Total)
	}
}

func TestIndexerBackfillEmpty(t *testing.T) {
	ix := search.NewIndexer(search.NewMemory())
	if err := ix.Backfill(context.Background()); err != nil {
		t.Fatalf("empty Backfill should be a no-op, got %v", err)
	}
}

func TestNewIndexerNilEnginePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewIndexer(nil) should panic")
		}
	}()
	_ = search.NewIndexer(nil)
}

func TestIndexerNilSearchable(t *testing.T) {
	ix := search.NewIndexer(search.NewMemory())
	if err := ix.Index(context.Background(), nil); err == nil {
		t.Fatal("Index(nil) should error")
	}
	if err := ix.DeleteModel(context.Background(), nil); err == nil {
		t.Fatal("DeleteModel(nil) should error")
	}
}

// TestIndexerConcurrent exercises the Indexer under the race detector to confirm
// it adds no shared state on top of the concurrency-safe Engine.
func TestIndexerConcurrent(t *testing.T) {
	ctx := context.Background()
	ix := search.NewIndexer(search.NewMemory())

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = ix.Index(ctx, post{ID: uint64(n), Title: "World", Body: "x"})
		}(i)
	}
	wg.Wait()

	res, _ := ix.Engine().Search(ctx, "posts", "world", search.Options{PerPage: 100})
	if res.Total != 50 {
		t.Fatalf("want 50 indexed, got %d", res.Total)
	}
}
