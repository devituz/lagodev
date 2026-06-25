package search

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// These tests stress the Memory engine and the Indexer under many concurrent
// goroutines. They are written to be run with -race: the assertions are
// secondary to the race detector, but they also pin down behavioural
// invariants (bounded growth, internally consistent results).

// memDocCount reaches into the engine to count live documents in an index. It
// is test-only and takes the same lock the engine uses, so it is race-safe.
func memDocCount(m *Memory, index string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.indexes[index])
}

func TestMemoryConcurrentMixedOps(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	const (
		writers  = 8
		readers  = 8
		deleters = 4
		iters    = 500
		keySpace = 32 // bounded ID space => bounded index growth
	)

	var wg sync.WaitGroup

	// Writers index/re-index across a bounded key space.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("k%d", (w*iters+i)%keySpace)
				_ = m.Index(ctx, "c", Doc(id, map[string]any{
					"title": "concurrent indexing under race",
					"body":  fmt.Sprintf("payload %d worker %d go term", i, w),
					"lang":  "en",
				}))
			}
		}(w)
	}

	// Deleters evict from the same bounded key space.
	for d := 0; d < deleters; d++ {
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("k%d", (d*7+i)%keySpace)
				_ = m.Delete(ctx, "c", id)
			}
		}(d)
	}

	// Readers search continuously; they must never panic or read torn state.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				res, err := m.Search(ctx, "c", "concurrent go term", Options{
					Page: 1, PerPage: 5, Prefix: i%2 == 0,
					Filters: map[string]any{"lang": "en"},
				})
				if err != nil {
					t.Errorf("search error: %v", err)
					return
				}
				// Hits returned must not exceed the page size, and Total must
				// never be negative — basic internal-consistency checks.
				if len(res.Hits) > 5 {
					t.Errorf("page overflow: %d hits", len(res.Hits))
					return
				}
				if res.Total < 0 {
					t.Errorf("negative total: %d", res.Total)
					return
				}
			}
		}()
	}

	wg.Wait()

	// Growth must be bounded by the key space, proving Index/Delete don't leak.
	if n := memDocCount(m, "c"); n > keySpace {
		t.Fatalf("index grew beyond key space: %d > %d", n, keySpace)
	}
}

func TestMemoryConcurrentResultsAreSnapshots(t *testing.T) {
	// A Search returns Hits whose Fields point at the stored map. Re-indexing
	// replaces the map wholesale (never mutates in place), so a hit captured by
	// one goroutine must stay stable while others re-index. We assert no race
	// and that the captured fields remain readable/consistent.
	m := NewMemory()
	ctx := context.Background()
	_ = m.Index(ctx, "s", Doc("1", map[string]any{"body": "stable term", "v": 0}))

	var stop atomic.Bool

	// Re-indexer churns the same ID with new maps until readers are done. It is
	// joined separately from the readers so wg.Wait() never blocks on it.
	var churner sync.WaitGroup
	churner.Add(1)
	go func() {
		defer churner.Done()
		for i := 1; !stop.Load(); i++ {
			_ = m.Index(ctx, "s", Doc("1", map[string]any{"body": "stable term", "v": i}))
		}
	}()

	// Readers grab hits and read their fields concurrently.
	var readers sync.WaitGroup
	for r := 0; r < 8; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 2000; i++ {
				res, _ := m.Search(ctx, "s", "stable", Options{})
				for _, h := range res.Hits {
					// Reading the captured field must be race-free because the
					// map is never mutated after publication.
					_ = fmt.Sprintf("%v", h.Fields["v"])
				}
			}
		}()
	}

	readers.Wait()   // wait for readers first
	stop.Store(true) // then signal the churner to exit
	churner.Wait()   // and join it
}

func TestIndexerConcurrentBackfill(t *testing.T) {
	m := NewMemory()
	ix := NewIndexer(m)
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			items := make([]Searchable, 0, 64)
			for i := 0; i < 64; i++ {
				items = append(items, sm{
					idx: "bf",
					doc: Doc(fmt.Sprintf("g%d-%d", g, i), map[string]any{
						"body": "backfill concurrent term",
					}),
				})
			}
			if err := ix.Backfill(ctx, items...); err != nil {
				t.Errorf("backfill: %v", err)
			}
		}(g)
	}
	wg.Wait()

	// 16 goroutines * 64 unique ids = 1024 distinct documents.
	if n := memDocCount(m, "bf"); n != 16*64 {
		t.Fatalf("backfill doc count = %d, want %d", n, 16*64)
	}
}

// sm is a minimal Searchable for tests.
type sm struct {
	idx string
	doc Document
}

func (s sm) SearchIndex() string      { return s.idx }
func (s sm) SearchDocument() Document { return s.doc }
