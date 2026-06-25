package search

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzTokenize asserts Tokenize never panics and always returns clean tokens:
// non-empty, no stop-words, valid UTF-8, and free of separator runes. It is the
// shared primitive for both indexing and querying, so a panic here would crash
// every engine.
func FuzzTokenize(f *testing.F) {
	seeds := []string{
		"",
		"   ",
		"Hello World",
		"the and or but with",
		"café naïve über",
		"日本語 한국어 العربية",
		":* & | ! ( ) <-> ",
		"'; DROP TABLE posts; --",
		"%s %q %v %d",
		strings.Repeat("a", 10000),
		"\x00\x01\x02 mixed ￿ control",
		"123 456 mixed789words",
		"\t\n\r\v\f",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		toks := Tokenize(in)
		for _, tok := range toks {
			if tok == "" {
				t.Fatalf("empty token from %q", in)
			}
			if !utf8.ValidString(tok) {
				t.Fatalf("invalid UTF-8 token %q from %q", tok, in)
			}
			if IsStopWord(tok) {
				t.Fatalf("stop-word %q leaked from %q", tok, in)
			}
			// No token may contain a separator (whitespace/punct); Tokenize
			// splits on any non-letter, non-digit rune.
			for _, r := range tok {
				// Folded output is lower-case; digits and letters only.
				if strings.ContainsRune(" \t\n\r.,;:!?()[]{}\"'", r) {
					t.Fatalf("token %q contains separator %q (from %q)", tok, r, in)
				}
			}
		}
	})
}

func TestTokenizeEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int // expected token count
	}{
		{"empty", "", 0},
		{"whitespace", "   \t\n  ", 0},
		{"all stopwords", "the and or but with as at by", 0},
		{"single", "hello", 1},
		{"punctuation only", ".,;:!?()-", 0},
		{"tsquery metachars", ":* & | ! ( )", 0},
		{"mixed", "go: the lang!", 2}, // "go", "lang"
	}
	for _, c := range cases {
		if got := len(Tokenize(c.in)); got != c.want {
			t.Errorf("%s: Tokenize(%q) = %d tokens, want %d (%v)",
				c.name, c.in, got, c.want, Tokenize(c.in))
		}
	}
}

func TestMemorySearchEdgeQueries(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	// A huge document must index without error.
	huge := strings.Repeat("lorem ipsum dolor ", 20000)
	if err := m.Index(ctx, "e",
		Doc("1", map[string]any{"body": huge, "title": "huge doc"}),
		Doc("2", map[string]any{"body": "small", "title": "tiny"}),
	); err != nil {
		t.Fatalf("index huge: %v", err)
	}

	// Empty query => no hits.
	if res, _ := m.Search(ctx, "e", "", Options{}); res.Total != 0 {
		t.Fatalf("empty query total=%d, want 0", res.Total)
	}
	// Only stop-words => no hits.
	if res, _ := m.Search(ctx, "e", "the and or with", Options{}); res.Total != 0 {
		t.Fatalf("stopword query total=%d, want 0", res.Total)
	}
	// Very long query must not panic and should still match.
	long := strings.Repeat("lorem ", 10000)
	res, err := m.Search(ctx, "e", long, Options{})
	if err != nil {
		t.Fatalf("long query: %v", err)
	}
	if res.Total < 1 {
		t.Fatalf("long query total=%d, want >=1", res.Total)
	}
	// tsquery metachars as a query are harmless to the Memory engine.
	if res, _ := m.Search(ctx, "e", ":* & | ! ( )", Options{}); res.Total != 0 {
		t.Fatalf("metachar query total=%d, want 0", res.Total)
	}
}

func TestBackfillEdgeCases(t *testing.T) {
	m := NewMemory()
	ix := NewIndexer(m)
	ctx := context.Background()

	// Empty backfill is a no-op, not an error.
	if err := ix.Backfill(ctx); err != nil {
		t.Fatalf("empty backfill: %v", err)
	}

	// Duplicate IDs: last write wins; index holds a single doc.
	dup := []Searchable{
		sm{idx: "d", doc: Doc("1", map[string]any{"body": "first"})},
		sm{idx: "d", doc: Doc("1", map[string]any{"body": "second"})},
		sm{idx: "d", doc: Doc("1", map[string]any{"body": "third"})},
	}
	if err := ix.Backfill(ctx, dup...); err != nil {
		t.Fatalf("dup backfill: %v", err)
	}
	if n := memDocCount(m, "d"); n != 1 {
		t.Fatalf("dup backfill count = %d, want 1", n)
	}
	// The last write must win.
	res, _ := m.Search(ctx, "d", "third", Options{})
	if res.Total != 1 {
		t.Fatalf("expected last-write-wins doc 'third', total=%d", res.Total)
	}
	if old, _ := m.Search(ctx, "d", "first", Options{}); old.Total != 0 {
		t.Fatalf("stale token 'first' survived, total=%d", old.Total)
	}

	// A nil entry mid-batch is an error and must be reported with its index.
	withNil := []Searchable{
		sm{idx: "d", doc: Doc("9", map[string]any{"body": "ok"})},
		nil,
	}
	if err := ix.Backfill(ctx, withNil...); err == nil {
		t.Fatalf("expected error for nil backfill item")
	}
}

func TestPostgresEdgeQueries(t *testing.T) {
	pg := NewPostgres(&fakeDB{queryRows: &fakeRows{}}, PgConfig{})
	ctx := context.Background()

	// Empty and stop-word-only queries must short-circuit without a DB round trip.
	for _, q := range []string{"", "   ", "the and or with"} {
		db := &fakeDB{queryRows: &fakeRows{}}
		pg2 := NewPostgres(db, PgConfig{})
		res, err := pg2.Search(ctx, "posts", q, Options{})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if res.Total != 0 || db.lastQuery != "" {
			t.Fatalf("query %q should short-circuit, ran %q", q, db.lastQuery)
		}
	}

	// A very long query still produces a single bound parameter, not a huge SQL.
	long := strings.Repeat("term ", 10000)
	db := &fakeDB{queryRows: &fakeRows{}}
	pgLong := NewPostgres(db, PgConfig{})
	if _, err := pgLong.Search(ctx, "posts", long, Options{}); err != nil {
		t.Fatalf("long query: %v", err)
	}
	if len(db.lastArgs) == 0 {
		t.Fatalf("long query produced no bound args")
	}
	_ = pg
}
