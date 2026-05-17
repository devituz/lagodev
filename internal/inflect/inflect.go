// Package inflect provides minimal, allocation-conscious string transformations
// used across the framework (snake_case, plural table names, etc.). It is
// intentionally small: callers may register irregulars at init time.
package inflect

import (
	"strings"
	"sync"
	"unicode"
)

var (
	mu         sync.RWMutex
	irregulars = map[string]string{
		"person":     "people",
		"man":        "men",
		"woman":      "women",
		"child":      "children",
		"tooth":      "teeth",
		"foot":       "feet",
		"goose":      "geese",
		"mouse":      "mice",
		"ox":         "oxen",
		"sheep":      "sheep",
		"fish":       "fish",
		"deer":       "deer",
		"series":     "series",
		"species":    "species",
		"datum":      "data",
		"criterion":  "criteria",
		"phenomenon": "phenomena",
	}
	uncountables = map[string]struct{}{
		"equipment":   {},
		"information": {},
		"rice":        {},
		"money":       {},
		"news":        {},
		"jeans":       {},
	}
)

// RegisterIrregular adds or overrides an irregular plural mapping.
func RegisterIrregular(singular, plural string) {
	mu.Lock()
	defer mu.Unlock()
	irregulars[strings.ToLower(singular)] = strings.ToLower(plural)
}

// Snake converts CamelCase or PascalCase to snake_case.
//
//	"UserID"        -> "user_id"
//	"HTTPRequest"   -> "http_request"
//	"PostCommentID" -> "post_comment_id"
func Snake(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if unicode.IsLower(prev) || (unicode.IsUpper(prev) && next != 0 && unicode.IsLower(next)) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Camel converts snake_case to camelCase.
func Camel(s string) string {
	if s == "" {
		return s
	}
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if i == 0 {
			parts[i] = strings.ToLower(p)
			continue
		}
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	return strings.Join(parts, "")
}

// Pascal converts snake_case to PascalCase.
func Pascal(s string) string {
	c := Camel(s)
	if c == "" {
		return c
	}
	return strings.ToUpper(c[:1]) + c[1:]
}

// Pluralize returns the (English) plural of word.
func Pluralize(word string) string {
	if word == "" {
		return word
	}
	lower := strings.ToLower(word)

	mu.RLock()
	if _, ok := uncountables[lower]; ok {
		mu.RUnlock()
		return word
	}
	if p, ok := irregulars[lower]; ok {
		mu.RUnlock()
		return matchCase(word, p)
	}
	mu.RUnlock()

	switch {
	case strings.HasSuffix(lower, "y") && len(lower) > 1 && !isVowel(rune(lower[len(lower)-2])):
		return word[:len(word)-1] + "ies"
	case strings.HasSuffix(lower, "s"),
		strings.HasSuffix(lower, "x"),
		strings.HasSuffix(lower, "z"),
		strings.HasSuffix(lower, "ch"),
		strings.HasSuffix(lower, "sh"):
		return word + "es"
	case strings.HasSuffix(lower, "f"):
		return word[:len(word)-1] + "ves"
	case strings.HasSuffix(lower, "fe"):
		return word[:len(word)-2] + "ves"
	default:
		return word + "s"
	}
}

// Singularize returns the singular form of word (best-effort).
func Singularize(word string) string {
	if word == "" {
		return word
	}
	lower := strings.ToLower(word)

	mu.RLock()
	if _, ok := uncountables[lower]; ok {
		mu.RUnlock()
		return word
	}
	for s, p := range irregulars {
		if p == lower {
			mu.RUnlock()
			return matchCase(word, s)
		}
	}
	mu.RUnlock()

	switch {
	case strings.HasSuffix(lower, "ies") && len(lower) > 3:
		return word[:len(word)-3] + "y"
	case strings.HasSuffix(lower, "ves") && len(lower) > 3:
		return word[:len(word)-3] + "f"
	case strings.HasSuffix(lower, "es") && (strings.HasSuffix(lower, "ches") ||
		strings.HasSuffix(lower, "shes") || strings.HasSuffix(lower, "xes") ||
		strings.HasSuffix(lower, "zes") || strings.HasSuffix(lower, "sses")):
		return word[:len(word)-2]
	case strings.HasSuffix(lower, "s") && !strings.HasSuffix(lower, "ss"):
		return word[:len(word)-1]
	default:
		return word
	}
}

// Table returns the conventional table name for a struct name:
// snake_case + pluralized. e.g. "UserProfile" -> "user_profiles".
func Table(structName string) string {
	return Pluralize(Snake(structName))
}

// ForeignKey returns the conventional foreign-key column for a struct name.
// e.g. "User" -> "user_id".
func ForeignKey(structName string) string {
	return Snake(structName) + "_id"
}

func isVowel(r rune) bool {
	switch r {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	}
	return false
}

func matchCase(orig, target string) string {
	if orig == "" {
		return target
	}
	if unicode.IsUpper(rune(orig[0])) {
		if len(target) == 0 {
			return target
		}
		return strings.ToUpper(target[:1]) + target[1:]
	}
	return target
}
