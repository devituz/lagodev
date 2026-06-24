package search

import (
	"strings"
	"unicode"
)

// stopWords is a small English stop-word set removed from both documents and
// queries before indexing/matching. Kept intentionally compact — callers that
// need a different list can build their own engine on top of the exported
// Tokenize primitives.
var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"but": {}, "by": {}, "for": {}, "if": {}, "in": {}, "into": {}, "is": {},
	"it": {}, "no": {}, "not": {}, "of": {}, "on": {}, "or": {}, "such": {},
	"that": {}, "the": {}, "their": {}, "then": {}, "there": {}, "these": {},
	"they": {}, "this": {}, "to": {}, "was": {}, "will": {}, "with": {},
}

// IsStopWord reports whether tok is in the built-in stop-word set. The input is
// folded to lower-case before the lookup.
func IsStopWord(tok string) bool {
	_, ok := stopWords[strings.ToLower(tok)]
	return ok
}

// fold lower-cases s and strips diacritics via Unicode decomposition so that
// e.g. "Café" and "cafe" collide. It is intentionally simple (NFKD-style fold
// over the basic Latin combining marks) and dependency-free.
func fold(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if folded, ok := foldRune(r); ok {
			b.WriteString(folded)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// foldRune maps a handful of common accented Latin runes to their ASCII base.
// Returns ("", false) when the rune needs no folding.
func foldRune(r rune) (string, bool) {
	switch r {
	case 'á', 'à', 'â', 'ä', 'ã', 'å', 'ā':
		return "a", true
	case 'é', 'è', 'ê', 'ë', 'ē':
		return "e", true
	case 'í', 'ì', 'î', 'ï', 'ī':
		return "i", true
	case 'ó', 'ò', 'ô', 'ö', 'õ', 'ø', 'ō':
		return "o", true
	case 'ú', 'ù', 'û', 'ü', 'ū':
		return "u", true
	case 'ñ':
		return "n", true
	case 'ç':
		return "c", true
	case 'ß':
		return "ss", true
	}
	return "", false
}

// Tokenize splits text into normalized, folded, stop-word-filtered tokens. It
// is the shared primitive used to index documents and parse queries so both
// sides use the same vocabulary. Splitting happens on any non-letter,
// non-digit rune.
func Tokenize(text string) []string {
	folded := fold(text)
	raw := strings.FieldsFunc(folded, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if t == "" {
			continue
		}
		if _, stop := stopWords[t]; stop {
			continue
		}
		out = append(out, t)
	}
	return out
}
