package search

import (
	"context"
	"fmt"
)

// Searchable is implemented by a model that knows how to project itself into a
// search Document. It is deliberately ORM-agnostic: the search package never
// imports orm, so a model can opt into indexing by satisfying this interface
// without coupling the two packages. The wiring into ORM lifecycle hooks lives
// in application code (see the package docs and docs/SEARCH.md).
//
// SearchIndex returns the name of the index the model belongs to (e.g.
// "posts"). All rows of the same model normally share one index.
//
// SearchDocument returns the indexable projection of the model: a stable ID
// plus the searchable/filterable fields. The returned Document.ID is the value
// used to replace or evict the row, so it must be stable across saves (the
// primary key is the natural choice).
type Searchable interface {
	// SearchIndex is the index name this model indexes into.
	SearchIndex() string
	// SearchDocument is the model's projection into an indexable Document.
	SearchDocument() Document
}

// Indexer mirrors model writes into a search Engine. It is a thin, stateless
// adapter around an Engine that speaks in terms of Searchable models and index
// IDs, so application code never has to thread index names and Document
// construction through every call site. The zero value is not usable; build one
// with NewIndexer.
//
// Indexer holds no state of its own and is safe for concurrent use whenever the
// underlying Engine is (both bundled engines are).
type Indexer struct {
	eng Engine
}

// NewIndexer returns an Indexer that writes to eng. It panics if eng is nil,
// since an Indexer with no engine can never do useful work.
func NewIndexer(eng Engine) *Indexer {
	if eng == nil {
		panic("search: NewIndexer requires a non-nil Engine")
	}
	return &Indexer{eng: eng}
}

// Engine returns the underlying search Engine the Indexer writes to.
func (ix *Indexer) Engine() Engine { return ix.eng }

// Index inserts or replaces m in its declared index. Because the Engine replaces
// a document by ID, a single Index call covers both inserts and updates, which
// makes it the natural body of an AfterSave hook. The supplied context is passed
// straight through to the Engine.
func (ix *Indexer) Index(ctx context.Context, m Searchable) error {
	if m == nil {
		return fmt.Errorf("search: Index requires a non-nil Searchable")
	}
	if err := ix.eng.Index(ctx, m.SearchIndex(), m.SearchDocument()); err != nil {
		return fmt.Errorf("search: index %q: %w", m.SearchIndex(), err)
	}
	return nil
}

// Delete evicts a document by ID from the named index. It is the natural body of
// an AfterDelete hook; pass the same index name and ID that Index used. Unknown
// IDs are ignored by the Engine.
func (ix *Indexer) Delete(ctx context.Context, index, id string) error {
	if err := ix.eng.Delete(ctx, index, id); err != nil {
		return fmt.Errorf("search: delete %q/%q: %w", index, id, err)
	}
	return nil
}

// DeleteModel evicts m using its own declared index and document ID. It is a
// convenience over Delete for the common case where a Searchable value is in
// hand (e.g. inside an AfterDelete hook on the model itself).
func (ix *Indexer) DeleteModel(ctx context.Context, m Searchable) error {
	if m == nil {
		return fmt.Errorf("search: DeleteModel requires a non-nil Searchable")
	}
	return ix.Delete(ctx, m.SearchIndex(), m.SearchDocument().ID)
}

// Backfill (re)indexes a batch of Searchable models. It groups documents by
// index so that each index receives a single bulk Index call, which is far
// cheaper than indexing rows one at a time when warming an index from the
// database on boot. Models are allowed to span multiple indexes. A nil entry in
// items is an error; the call stops at the first failing Engine write.
func (ix *Indexer) Backfill(ctx context.Context, items ...Searchable) error {
	if len(items) == 0 {
		return nil
	}
	// Preserve first-seen index order for deterministic batching.
	order := make([]string, 0)
	batches := make(map[string][]Document)
	for i, m := range items {
		if m == nil {
			return fmt.Errorf("search: Backfill: item %d is nil", i)
		}
		idx := m.SearchIndex()
		if _, ok := batches[idx]; !ok {
			order = append(order, idx)
		}
		batches[idx] = append(batches[idx], m.SearchDocument())
	}
	for _, idx := range order {
		if err := ix.eng.Index(ctx, idx, batches[idx]...); err != nil {
			return fmt.Errorf("search: backfill %q: %w", idx, err)
		}
	}
	return nil
}
