// Package search provides a Scout-style full-text search abstraction for the
// lagodev framework. It defines a small Engine interface that higher layers
// program against, plus two implementations:
//
//   - Memory: a dependency-free in-memory inverted index (the default). It
//     tokenizes documents, folds case, drops stop-words, supports term and
//     prefix matching, TF-style ranking, equality filters, and pagination.
//   - Postgres: a full-text engine that compiles tsvector/tsquery SQL through
//     the framework's database.Connection (Grammar + Executor). It is wired so
//     queries are parameterized; the SQL generation is unit-tested independently
//     of a live server.
//
// A Searchable model hook lets ORM rows opt into indexing without coupling the
// search package to the orm package.
//
// Basic usage:
//
//	eng := search.NewMemory()
//	_ = eng.Index(ctx, "posts",
//	    search.Doc("1", map[string]any{"title": "Hello World", "body": "first post"}),
//	    search.Doc("2", map[string]any{"title": "Goodbye World", "body": "last post"}),
//	)
//	res, _ := eng.Search(ctx, "posts", "hello", search.Options{})
//	for _, hit := range res.Hits {
//	    fmt.Println(hit.ID, hit.Score)
//	}
package search

import "context"

// Document is a single indexable record: a stable ID plus a set of searchable
// fields. Field values may be strings, numbers, or bools; non-string values are
// stringified for the full-text index and kept verbatim for equality filters.
type Document struct {
	// ID uniquely identifies the document within its index.
	ID string
	// Fields holds the searchable/filterable attributes. Keys are field names.
	Fields map[string]any
}

// Doc is a convenience constructor for a Document.
func Doc(id string, fields map[string]any) Document {
	return Document{ID: id, Fields: fields}
}

// Hit is a single search result: the matched document ID, its relevance score
// (higher is more relevant), and the original fields (when the engine retains
// them).
type Hit struct {
	ID     string
	Score  float64
	Fields map[string]any
}

// Results is a page of search hits plus the total number of matches across all
// pages (before pagination).
type Results struct {
	Hits  []Hit
	Total int
}

// Options controls a Search call: pagination, equality filters, and prefix
// matching. The zero value is valid (first page, default size, term matching).
type Options struct {
	// Page is 1-indexed; values < 1 are treated as 1.
	Page int
	// PerPage is the page size; values < 1 fall back to DefaultPerPage.
	PerPage int
	// Filters constrains results to documents whose field equals the given
	// value (string-compared). Multiple filters are AND-ed.
	Filters map[string]any
	// Prefix enables prefix matching in addition to exact term matching, so a
	// query token "hel" matches "hello". Affects the Memory engine; the
	// Postgres engine appends ":*" to query lexemes.
	Prefix bool
}

// DefaultPerPage is used when Options.PerPage is unset.
const DefaultPerPage = 15

// Engine is the search backend contract. Implementations must be safe for
// concurrent use.
type Engine interface {
	// Index inserts or replaces documents in the named index. Re-indexing a
	// document with an existing ID replaces it.
	Index(ctx context.Context, index string, docs ...Document) error
	// Delete removes documents by ID from the named index. Unknown IDs are
	// ignored.
	Delete(ctx context.Context, index string, ids ...string) error
	// Search runs query against the named index and returns a ranked,
	// paginated page of hits.
	Search(ctx context.Context, index, query string, opts Options) (Results, error)
}

// normalizePage clamps page/perPage and returns the slice bounds for a result
// set of size total.
func normalizePage(page, perPage, total int) (lo, hi int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	lo = (page - 1) * perPage
	if lo > total {
		lo = total
	}
	hi = lo + perPage
	if hi > total {
		hi = total
	}
	return lo, hi
}
