package search

import (
	"context"
	"sync"
	"testing"
)

func memSeed(t *testing.T) *Memory {
	t.Helper()
	m := NewMemory()
	err := m.Index(context.Background(), "posts",
		Doc("1", map[string]any{"title": "Hello World", "body": "first post about go", "lang": "en"}),
		Doc("2", map[string]any{"title": "Goodbye World World", "body": "second post", "lang": "en"}),
		Doc("3", map[string]any{"title": "Unrelated", "body": "nothing here", "lang": "fr"}),
	)
	if err != nil {
		t.Fatalf("seed index: %v", err)
	}
	return m
}

func TestMemorySearchBasic(t *testing.T) {
	m := memSeed(t)
	res, err := m.Search(context.Background(), "posts", "hello", Options{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 1 || len(res.Hits) != 1 {
		t.Fatalf("hello: total=%d hits=%d, want 1/1", res.Total, len(res.Hits))
	}
	if res.Hits[0].ID != "1" {
		t.Fatalf("hello matched %q, want doc 1", res.Hits[0].ID)
	}
	if res.Hits[0].Fields["title"] != "Hello World" {
		t.Fatalf("hit fields not retained: %v", res.Hits[0].Fields)
	}
}

func TestMemorySearchRankingByTermFrequency(t *testing.T) {
	m := memSeed(t)
	// "world" appears twice in doc 2 ("World World") and once in doc 1, so
	// doc 2 must outrank doc 1.
	res, err := m.Search(context.Background(), "posts", "world", Options{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("world total=%d, want 2", res.Total)
	}
	if res.Hits[0].ID != "2" || res.Hits[1].ID != "1" {
		t.Fatalf("ranking order = %s,%s, want 2,1", res.Hits[0].ID, res.Hits[1].ID)
	}
	if res.Hits[0].Score <= res.Hits[1].Score {
		t.Fatalf("score not strictly higher: %v vs %v", res.Hits[0].Score, res.Hits[1].Score)
	}
}

func TestMemorySearchMultiTerm(t *testing.T) {
	m := memSeed(t)
	// Doc 1 matches both "hello" and "post"; doc 2 matches only "post".
	res, err := m.Search(context.Background(), "posts", "hello post", Options{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Total != 2 {
		t.Fatalf("multi-term total=%d, want 2", res.Total)
	}
	if res.Hits[0].ID != "1" {
		t.Fatalf("multi-term top = %s, want 1", res.Hits[0].ID)
	}
}

func TestMemorySearchPrefix(t *testing.T) {
	m := memSeed(t)
	// "hel" matches nothing exactly, but with Prefix it hits "hello" in doc 1.
	noPrefix, _ := m.Search(context.Background(), "posts", "hel", Options{})
	if noPrefix.Total != 0 {
		t.Fatalf("non-prefix 'hel' total=%d, want 0", noPrefix.Total)
	}
	res, err := m.Search(context.Background(), "posts", "hel", Options{Prefix: true})
	if err != nil {
		t.Fatalf("prefix search: %v", err)
	}
	if res.Total != 1 || res.Hits[0].ID != "1" {
		t.Fatalf("prefix 'hel' = total %d / %v, want doc 1", res.Total, res.Hits)
	}
}

func TestMemorySearchFilters(t *testing.T) {
	m := memSeed(t)
	// "world" matches docs 1 and 2; lang=en keeps both, then a tighter filter.
	res, _ := m.Search(context.Background(), "posts", "world", Options{
		Filters: map[string]any{"lang": "en"},
	})
	if res.Total != 2 {
		t.Fatalf("lang=en total=%d, want 2", res.Total)
	}
	// A filter excluding all matches yields nothing.
	none, _ := m.Search(context.Background(), "posts", "world", Options{
		Filters: map[string]any{"lang": "fr"},
	})
	if none.Total != 0 {
		t.Fatalf("lang=fr total=%d, want 0", none.Total)
	}
	// Missing field never matches.
	missing, _ := m.Search(context.Background(), "posts", "world", Options{
		Filters: map[string]any{"nope": "x"},
	})
	if missing.Total != 0 {
		t.Fatalf("missing-field filter total=%d, want 0", missing.Total)
	}
}

func TestMemorySearchFilterNumericCoercion(t *testing.T) {
	m := NewMemory()
	_ = m.Index(context.Background(), "n",
		Doc("1", map[string]any{"body": "alpha", "year": 2024}),
		Doc("2", map[string]any{"body": "alpha", "year": 2025}),
	)
	// Filter value given as int matches the stored int via stringification.
	res, _ := m.Search(context.Background(), "n", "alpha", Options{Filters: map[string]any{"year": 2024}})
	if res.Total != 1 || res.Hits[0].ID != "1" {
		t.Fatalf("numeric filter = %v, want doc 1 only", res)
	}
}

func TestMemorySearchPagination(t *testing.T) {
	m := NewMemory()
	docs := make([]Document, 0, 5)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		docs = append(docs, Doc(id, map[string]any{"body": "common term"}))
	}
	_ = m.Index(context.Background(), "p", docs...)

	p1, _ := m.Search(context.Background(), "p", "common", Options{Page: 1, PerPage: 2})
	if p1.Total != 5 || len(p1.Hits) != 2 {
		t.Fatalf("page1 total=%d hits=%d, want 5/2", p1.Total, len(p1.Hits))
	}
	if p1.Hits[0].ID != "a" || p1.Hits[1].ID != "b" {
		t.Fatalf("page1 ids = %s,%s, want a,b", p1.Hits[0].ID, p1.Hits[1].ID)
	}
	p3, _ := m.Search(context.Background(), "p", "common", Options{Page: 3, PerPage: 2})
	if p3.Total != 5 || len(p3.Hits) != 1 || p3.Hits[0].ID != "e" {
		t.Fatalf("page3 = total %d / %v, want last doc e", p3.Total, p3.Hits)
	}
	// Page beyond the end yields an empty (non-nil-length) window.
	pBeyond, _ := m.Search(context.Background(), "p", "common", Options{Page: 99, PerPage: 2})
	if pBeyond.Total != 5 || len(pBeyond.Hits) != 0 {
		t.Fatalf("page beyond = total %d hits %d, want 5/0", pBeyond.Total, len(pBeyond.Hits))
	}
}

func TestMemorySearchNoMatch(t *testing.T) {
	m := memSeed(t)
	res, _ := m.Search(context.Background(), "posts", "nonexistentxyz", Options{})
	if res.Total != 0 || len(res.Hits) != 0 {
		t.Fatalf("no-match total=%d hits=%d, want 0/0", res.Total, len(res.Hits))
	}
	// Blank query yields nothing even though docs exist.
	blank, _ := m.Search(context.Background(), "posts", "   ", Options{})
	if blank.Total != 0 {
		t.Fatalf("blank query total=%d, want 0", blank.Total)
	}
	// Unknown index is empty, not an error.
	unknown, err := m.Search(context.Background(), "missing", "hello", Options{})
	if err != nil || unknown.Total != 0 {
		t.Fatalf("unknown index = %v / total %d, want nil/0", err, unknown.Total)
	}
}

func TestMemoryReindexReplaces(t *testing.T) {
	m := memSeed(t)
	// Re-index doc 1 with new content; the old "hello" token must be gone.
	_ = m.Index(context.Background(), "posts",
		Doc("1", map[string]any{"title": "Completely Different", "body": "changed", "lang": "en"}),
	)
	old, _ := m.Search(context.Background(), "posts", "hello", Options{})
	if old.Total != 0 {
		t.Fatalf("after reindex 'hello' total=%d, want 0", old.Total)
	}
	updated, _ := m.Search(context.Background(), "posts", "different", Options{})
	if updated.Total != 1 || updated.Hits[0].ID != "1" {
		t.Fatalf("after reindex 'different' = %v, want doc 1", updated)
	}
}

func TestMemoryDelete(t *testing.T) {
	m := memSeed(t)
	if err := m.Delete(context.Background(), "posts", "1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, _ := m.Search(context.Background(), "posts", "hello", Options{})
	if res.Total != 0 {
		t.Fatalf("after delete total=%d, want 0", res.Total)
	}
	// Deleting unknown IDs / indexes is a no-op, not an error.
	if err := m.Delete(context.Background(), "posts", "999"); err != nil {
		t.Fatalf("delete unknown id: %v", err)
	}
	if err := m.Delete(context.Background(), "missing", "1"); err != nil {
		t.Fatalf("delete unknown index: %v", err)
	}
}

func TestMemoryConcurrentIndexAndSearch(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	var wg sync.WaitGroup

	// Writers continuously index/re-index/delete.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := string(rune('a' + (i % 5)))
				_ = m.Index(ctx, "c", Doc(id, map[string]any{"body": "concurrent term go"}))
				if i%7 == 0 {
					_ = m.Delete(ctx, "c", id)
				}
			}
		}(w)
	}
	// Readers continuously search.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _ = m.Search(ctx, "c", "concurrent", Options{Page: 1, PerPage: 3, Prefix: true})
			}
		}()
	}
	wg.Wait()
}
