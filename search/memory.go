package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Memory is a dependency-free, in-memory search Engine. It keeps every indexed
// document in a per-index map and answers queries by tokenizing both the stored
// documents and the query, then scoring by term-frequency overlap. It is the
// default engine returned by NewMemory and is safe for concurrent use.
//
// Ranking is intentionally simple: each query token contributes the number of
// times it appears across a document's fields (its term frequency). When
// Options.Prefix is set, a query token also matches any document token that has
// it as a prefix. Documents with a higher accumulated score rank first; ties
// break on document ID for a stable ordering.
type Memory struct {
	mu sync.RWMutex
	// indexes maps an index name to its documents, keyed by document ID.
	indexes map[string]map[string]memDoc
}

// memDoc is the internal representation of an indexed document: the original
// fields (returned on hits and used for filters) plus a precomputed term-
// frequency table built from the searchable field values.
type memDoc struct {
	id     string
	fields map[string]any
	// freq counts how often each folded token occurs across all field values.
	freq map[string]int
}

// NewMemory constructs an empty in-memory search Engine.
func NewMemory() *Memory {
	return &Memory{indexes: make(map[string]map[string]memDoc)}
}

// Index inserts or replaces docs in the named index. A document whose ID already
// exists is replaced wholesale (its previous tokens and fields are discarded).
func (m *Memory) Index(_ context.Context, index string, docs ...Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx, ok := m.indexes[index]
	if !ok {
		idx = make(map[string]memDoc, len(docs))
		m.indexes[index] = idx
	}
	for _, d := range docs {
		idx[d.ID] = memDoc{
			id:     d.ID,
			fields: d.Fields,
			freq:   buildFreq(d.Fields),
		}
	}
	return nil
}

// Delete removes documents by ID from the named index. Unknown IDs and unknown
// indexes are silently ignored.
func (m *Memory) Delete(_ context.Context, index string, ids ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx, ok := m.indexes[index]
	if !ok {
		return nil
	}
	for _, id := range ids {
		delete(idx, id)
	}
	return nil
}

// Search tokenizes query, scores every document in the named index, applies
// equality Filters, sorts by descending score, and returns the requested page.
// Total reflects the number of matches before pagination. A blank query (no
// tokens) yields no hits.
func (m *Memory) Search(_ context.Context, index, query string, opts Options) (Results, error) {
	tokens := Tokenize(query)

	m.mu.RLock()
	idx := m.indexes[index]
	// Collect matching, filtered hits under the read lock; scoring touches only
	// the snapshot maps, which are never mutated in place.
	matched := make([]Hit, 0, len(idx))
	for _, d := range idx {
		if !matchesFilters(d.fields, opts.Filters) {
			continue
		}
		score := scoreDoc(d.freq, tokens, opts.Prefix)
		if score <= 0 {
			continue
		}
		matched = append(matched, Hit{ID: d.id, Score: score, Fields: d.fields})
	}
	m.mu.RUnlock()

	// Stable ordering: score descending, then ID ascending for determinism.
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].Score != matched[j].Score {
			return matched[i].Score > matched[j].Score
		}
		return matched[i].ID < matched[j].ID
	})

	total := len(matched)
	lo, hi := normalizePage(opts.Page, opts.PerPage, total)
	return Results{Hits: matched[lo:hi], Total: total}, nil
}

// buildFreq tokenizes every field value and returns the per-token occurrence
// counts used for ranking. Non-string values are stringified via fmt so that
// numbers and bools become searchable lexemes.
func buildFreq(fields map[string]any) map[string]int {
	freq := make(map[string]int)
	for _, v := range fields {
		for _, tok := range Tokenize(stringify(v)) {
			freq[tok]++
		}
	}
	return freq
}

// scoreDoc accumulates the term-frequency contribution of each query token
// against a document's freq table. With prefix matching enabled, a query token
// also collects the frequency of every document token it prefixes.
func scoreDoc(freq map[string]int, tokens []string, prefix bool) float64 {
	if len(tokens) == 0 {
		return 0
	}
	var score float64
	for _, q := range tokens {
		if c, ok := freq[q]; ok {
			score += float64(c)
			continue
		}
		if prefix {
			for tok, c := range freq {
				if strings.HasPrefix(tok, q) {
					score += float64(c)
				}
			}
		}
	}
	return score
}

// matchesFilters reports whether a document satisfies every equality filter.
// Comparison is by stringified value so an int filter matches a numeric field
// regardless of its concrete Go type. A missing field never matches.
func matchesFilters(fields map[string]any, filters map[string]any) bool {
	for k, want := range filters {
		got, ok := fields[k]
		if !ok {
			return false
		}
		if stringify(got) != stringify(want) {
			return false
		}
	}
	return true
}

// stringify renders a field value as a string for tokenizing and filtering.
// Strings pass through unchanged; everything else goes through fmt's default
// formatting.
func stringify(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}
