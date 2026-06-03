// Package i18n provides translation primitives modelled on Laravel's
// Lang facade.
//
// Messages live in JSON files keyed by locale:
//
//	resources/lang/en.json
//	resources/lang/uz.json
//	resources/lang/ru.json
//
// Each file is a flat JSON object: `{"welcome": "Hello :name"}`.
// Nested keys are joined with "." in lookups: `{"auth.failed": "..."}`
// is referenced as `t.Get("auth.failed", ...)`.
//
// Placeholders use Laravel's `:name` syntax. Pass a map of
// replacements (or a list of key/value pairs via Replace).
//
// Pluralisation: a key whose value is `{"one": "...", "other": "..."}`
// is selected based on the count passed via Choice.
//
// Usage:
//
//	tr, _ := i18n.Load("resources/lang", "en")
//	tr.Get("welcome", i18n.M{"name": "Ada"})       // "Hello Ada"
//	tr.WithLocale("uz").Get("welcome", i18n.M{...}) // "Salom ..."
//	tr.Choice("apples", 3, i18n.M{"count": "3"})
package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrLocaleMissing is returned when no messages are loaded for the
// requested locale.
var ErrLocaleMissing = errors.New("i18n: locale not loaded")

// M is a shorthand for a map of placeholder replacements.
type M map[string]string

// Translator holds the loaded messages for one or more locales.
type Translator struct {
	mu       sync.RWMutex
	messages map[string]map[string]any // locale → flat key → string|map[string]any
	def      string                    // default locale
	current  string                    // currently active locale
	fallback string                    // fallback locale (e.g. "en")
}

// Load reads every *.json file in dir, treating the basename as the
// locale code. The default locale is set to `defaultLocale`.
func Load(dir, defaultLocale string) (*Translator, error) {
	t := &Translator{
		messages: make(map[string]map[string]any),
		def:      defaultLocale,
		current:  defaultLocale,
		fallback: defaultLocale,
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("i18n: read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		raw := map[string]any{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("i18n: parse %s: %w", e.Name(), err)
		}
		t.messages[locale] = flatten(raw, "")
	}
	if _, ok := t.messages[defaultLocale]; !ok {
		// Loading without the default locale is allowed (caller may
		// supply it later via Add), but warn through ErrLocaleMissing.
		return t, fmt.Errorf("%w: default %q", ErrLocaleMissing, defaultLocale)
	}
	return t, nil
}

// LoadFS is the io/fs.FS-backed variant for embedded files.
func LoadFS(fsys fs.FS, dir, defaultLocale string) (*Translator, error) {
	t := &Translator{
		messages: make(map[string]map[string]any),
		def:      defaultLocale,
		current:  defaultLocale,
		fallback: defaultLocale,
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("i18n: read fs dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		data, err := fs.ReadFile(fsys, filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		raw := map[string]any{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("i18n: parse %s: %w", e.Name(), err)
		}
		t.messages[locale] = flatten(raw, "")
	}
	return t, nil
}

// Add registers a locale from a flat key/value map. Useful for tests.
func (t *Translator) Add(locale string, msgs map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.messages[locale] = flatten(msgs, "")
}

// SetFallback designates the locale used when a key is missing from
// the active locale.
func (t *Translator) SetFallback(locale string) *Translator {
	t.mu.Lock()
	t.fallback = locale
	t.mu.Unlock()
	return t
}

// Default returns the original default locale (set at Load time).
func (t *Translator) Default() string { return t.def }

// Current returns the active locale.
func (t *Translator) Current() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.current
}

// WithLocale returns a shallow clone with the requested active locale.
// The underlying messages map is shared (read-only).
func (t *Translator) WithLocale(locale string) *Translator {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return &Translator{
		messages: t.messages,
		def:      t.def,
		current:  locale,
		fallback: t.fallback,
	}
}

// Has reports whether the active locale defines key.
func (t *Translator) Has(key string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.messages[t.current][key]
	return ok
}

// Get returns the translated message for key, substituting :placeholder
// occurrences from replace. If the key is missing from the active
// locale, the fallback locale is consulted. If still missing, the key
// itself is returned (helps spot missing translations in development).
func (t *Translator) Get(key string, replace ...M) string {
	t.mu.RLock()
	msgs := t.messages[t.current]
	fb := t.messages[t.fallback]
	t.mu.RUnlock()
	v, ok := msgs[key]
	if !ok {
		v, ok = fb[key]
	}
	if !ok {
		return key
	}
	s, ok := v.(string)
	if !ok {
		// Plural map without explicit Choice — just stringify default
		// "other" if present.
		if m, mok := v.(map[string]any); mok {
			if other, ook := m["other"].(string); ook {
				return substitute(other, replace...)
			}
		}
		return key
	}
	return substitute(s, replace...)
}

// Choice returns the pluralised form for key based on count. The
// underlying value must be a map with at least "other"; "one" is used
// when count==1, "zero" when count==0 (if defined).
//
// The literal value of count is also exposed as :count in the
// substitution map so messages like `":count items"` work without
// extra boilerplate.
func (t *Translator) Choice(key string, count int, replace ...M) string {
	t.mu.RLock()
	msgs := t.messages[t.current]
	fb := t.messages[t.fallback]
	t.mu.RUnlock()
	v, ok := msgs[key]
	if !ok {
		v, ok = fb[key]
	}
	if !ok {
		return key
	}
	m, ok := v.(map[string]any)
	if !ok {
		// Not a plural form — fall back to Get semantics.
		s, _ := v.(string)
		return substituteWithCount(s, count, replace...)
	}
	pick := func(form string) (string, bool) {
		v, ok := m[form].(string)
		return v, ok
	}
	switch count {
	case 0:
		if s, ok := pick("zero"); ok {
			return substituteWithCount(s, count, replace...)
		}
	case 1:
		if s, ok := pick("one"); ok {
			return substituteWithCount(s, count, replace...)
		}
	}
	if s, ok := pick("other"); ok {
		return substituteWithCount(s, count, replace...)
	}
	return key
}

// substitute performs :placeholder replacement.
func substitute(s string, replace ...M) string {
	if len(replace) == 0 || len(replace[0]) == 0 {
		return s
	}
	for k, v := range replace[0] {
		s = strings.ReplaceAll(s, ":"+k, v)
	}
	return s
}

func substituteWithCount(s string, count int, replace ...M) string {
	merged := M{"count": fmt.Sprintf("%d", count)}
	if len(replace) > 0 {
		for k, v := range replace[0] {
			merged[k] = v
		}
	}
	return substitute(s, merged)
}

// flatten turns a nested JSON tree into a flat "a.b.c" → leaf map.
// Plural entries (objects with only "zero"/"one"/"other" keys) are
// preserved as map[string]any so Choice can consume them.
func flatten(src map[string]any, prefix string) map[string]any {
	out := map[string]any{}
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch vv := v.(type) {
		case map[string]any:
			if isPluralForm(vv) {
				out[key] = vv
				continue
			}
			for nk, nv := range flatten(vv, key) {
				out[nk] = nv
			}
		default:
			out[key] = v
		}
	}
	return out
}

func isPluralForm(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		switch k {
		case "zero", "one", "two", "few", "many", "other":
			continue
		default:
			return false
		}
	}
	return true
}
