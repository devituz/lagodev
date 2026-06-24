package search

import (
	"reflect"
	"testing"
)

// --- Tokenize -------------------------------------------------------------

func TestTokenizeBasic(t *testing.T) {
	got := Tokenize("Hello World")
	want := []string{"hello", "world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize(%q) = %v, want %v", "Hello World", got, want)
	}
}

func TestTokenizeCaseFolding(t *testing.T) {
	got := Tokenize("GoLang LAGODEV gO")
	want := []string{"golang", "lagodev", "go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case folding: got %v, want %v", got, want)
	}
}

func TestTokenizePunctuationSplitting(t *testing.T) {
	// Splitting happens on any non-letter, non-digit rune; runs of
	// separators collapse (FieldsFunc drops empty fields).
	got := Tokenize("foo,bar.baz!!qux---end__final")
	want := []string{"foo", "bar", "baz", "qux", "end", "final"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("punctuation split: got %v, want %v", got, want)
	}
}

func TestTokenizeKeepsDigits(t *testing.T) {
	got := Tokenize("go1.26 v2 path/sub/42")
	// Digits and alphanumeric runs survive splitting on punctuation.
	want := []string{"go1", "26", "v2", "path", "sub", "42"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("digits: got %v, want %v", got, want)
	}
}

func TestTokenizeAlphanumericKept(t *testing.T) {
	// Letters and digits inside a single run are not split from each other.
	got := Tokenize("abc123def")
	want := []string{"abc123def"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alphanumeric run: got %v, want %v", got, want)
	}
}

func TestTokenizeStopWordsRemoved(t *testing.T) {
	got := Tokenize("the quick brown fox is in the box")
	// "the", "is", "in" are stop-words.
	want := []string{"quick", "brown", "fox", "box"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stop-words: got %v, want %v", got, want)
	}
}

func TestTokenizeAllStopWords(t *testing.T) {
	got := Tokenize("the and or with to")
	if len(got) != 0 {
		t.Fatalf("all stop-words should yield empty, got %v", got)
	}
}

func TestTokenizeEmptyAndSeparatorOnly(t *testing.T) {
	for _, in := range []string{"", "   ", "---", ".,!?", "\t\n"} {
		if got := Tokenize(in); len(got) != 0 {
			t.Fatalf("Tokenize(%q) = %v, want empty", in, got)
		}
	}
}

func TestTokenizeDiacriticsFold(t *testing.T) {
	// "Café" and "cafe" must collide; accents are stripped to ASCII base.
	cases := map[string][]string{
		"Café":              {"cafe"},
		"naïve résumé":      {"naive", "resume"},
		"jalapeño piñata":   {"jalapeno", "pinata"},
		"Über Mañana Søren": {"uber", "manana", "soren"},
		"façade":            {"facade"},
		"Straße":            {"strasse"}, // ß folds to "ss"
	}
	for in, want := range cases {
		if got := Tokenize(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Tokenize(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTokenizeFoldCollision(t *testing.T) {
	// The folded forms must be byte-identical so index and query collide.
	a := Tokenize("Café")
	b := Tokenize("cafe")
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("fold collision failed: %v vs %v", a, b)
	}
}

func TestTokenizeUnicodeNonLatinKept(t *testing.T) {
	// Non-Latin letters are still letters: not split, not folded away.
	got := Tokenize("привет мир 日本語")
	want := []string{"привет", "мир", "日本語"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unicode letters: got %v, want %v", got, want)
	}
}

func TestTokenizeUnicodeDigits(t *testing.T) {
	// Arabic-Indic digits are unicode digits and must be retained.
	got := Tokenize("test ٤٢")
	want := []string{"test", "٤٢"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unicode digits: got %v, want %v", got, want)
	}
}

func TestTokenizeNoStemming(t *testing.T) {
	// There is no stemmer: plurals/inflections survive verbatim.
	got := Tokenize("running runs runner")
	want := []string{"running", "runs", "runner"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no stemming expected: got %v, want %v", got, want)
	}
}

// --- IsStopWord -----------------------------------------------------------

func TestIsStopWord(t *testing.T) {
	stops := []string{"the", "and", "or", "with", "to", "is", "in"}
	for _, w := range stops {
		if !IsStopWord(w) {
			t.Errorf("IsStopWord(%q) = false, want true", w)
		}
	}
	notStops := []string{"hello", "world", "golang", "thers", "ther", ""}
	for _, w := range notStops {
		if IsStopWord(w) {
			t.Errorf("IsStopWord(%q) = true, want false", w)
		}
	}
}

func TestIsStopWordCaseInsensitive(t *testing.T) {
	for _, w := range []string{"THE", "The", "tHe", "AND", "With"} {
		if !IsStopWord(w) {
			t.Errorf("IsStopWord(%q) = false, want true (case-insensitive)", w)
		}
	}
}

// IsStopWord folds case only; it does not strip diacritics, so an accented
// look-alike of a stop-word is not treated as one. This documents the
// boundary between IsStopWord (ToLower only) and Tokenize (full fold).
func TestIsStopWordNoDiacriticFold(t *testing.T) {
	if IsStopWord("thé") {
		t.Errorf("IsStopWord(%q) = true, want false (no diacritic fold)", "thé")
	}
}

// --- Doc / Document -------------------------------------------------------

func TestDocConstructor(t *testing.T) {
	fields := map[string]any{"title": "Hello", "n": 42, "ok": true}
	d := Doc("7", fields)
	if d.ID != "7" {
		t.Fatalf("Doc ID = %q, want %q", d.ID, "7")
	}
	if !reflect.DeepEqual(d.Fields, fields) {
		t.Fatalf("Doc Fields = %v, want %v", d.Fields, fields)
	}
}

func TestDocNilFields(t *testing.T) {
	d := Doc("1", nil)
	if d.ID != "1" || d.Fields != nil {
		t.Fatalf("Doc with nil fields = %+v", d)
	}
}

// --- normalizePage --------------------------------------------------------

func TestNormalizePage(t *testing.T) {
	tests := []struct {
		name                 string
		page, perPage, total int
		wantLo, wantHi       int
	}{
		{"first page default", 1, 10, 100, 0, 10},
		{"second page", 2, 10, 100, 10, 20},
		{"last partial page", 3, 10, 25, 20, 25},
		{"page zero clamps to first", 0, 10, 100, 0, 10},
		{"negative page clamps to first", -5, 10, 100, 0, 10},
		{"perPage zero falls back to default", 1, 0, 100, 0, DefaultPerPage},
		{"negative perPage falls back to default", 1, -3, 100, 0, DefaultPerPage},
		{"page beyond total clamps lo to total", 100, 10, 25, 25, 25},
		{"empty result set", 1, 10, 0, 0, 0},
		{"hi clamped to total", 1, 50, 30, 0, 30},
		{"exact full page", 2, 10, 20, 10, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lo, hi := normalizePage(tt.page, tt.perPage, tt.total)
			if lo != tt.wantLo || hi != tt.wantHi {
				t.Fatalf("normalizePage(%d,%d,%d) = (%d,%d), want (%d,%d)",
					tt.page, tt.perPage, tt.total, lo, hi, tt.wantLo, tt.wantHi)
			}
			// Invariants: bounds must always be a valid, in-range slice window.
			if lo < 0 || hi < 0 {
				t.Fatalf("negative bounds: lo=%d hi=%d", lo, hi)
			}
			if lo > hi {
				t.Fatalf("lo > hi: lo=%d hi=%d", lo, hi)
			}
			if lo > tt.total || hi > tt.total {
				t.Fatalf("bounds exceed total=%d: lo=%d hi=%d", tt.total, lo, hi)
			}
		})
	}
}

func TestNormalizePageDefaultConstant(t *testing.T) {
	if DefaultPerPage != 15 {
		t.Fatalf("DefaultPerPage = %d, want 15", DefaultPerPage)
	}
}

// --- Tokenize index/query parity -----------------------------------------

// Tokenize is the shared vocabulary primitive: indexing a document field and
// parsing a query for the same logical term must produce matching tokens, even
// across case and diacritics. This guards the contract the Engine relies on.
func TestTokenizeIndexQueryParity(t *testing.T) {
	docTokens := Tokenize("The Café in São Paulo")
	queryTokens := Tokenize("cafe")
	found := false
	for _, dt := range docTokens {
		if dt == queryTokens[0] {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("query token %v not found in doc tokens %v", queryTokens, docTokens)
	}
}
