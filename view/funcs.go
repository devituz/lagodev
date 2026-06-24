package view

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// builtinFuncs returns the FuncMap every Engine ships with. Caller-supplied
// Options.Funcs are merged over these, so an app can override "route"/"asset"/
// "url"/"csrfField" with real implementations without view importing web.
//
// Categories:
//
//   - safe HTML/attr/url/js escaping bypasses (use deliberately)
//   - data helpers: dict, default
//   - string helpers: upper, lower, title, trim, replace, truncate
//   - number helpers: add, sub, mul
//   - date helpers: now, date (Go layout), formatDate
//   - include/component aliases for {{template}}
//   - injectable stubs: route, asset, url, csrfField (overridable via Funcs)
func builtinFuncs() template.FuncMap {
	return template.FuncMap{
		// --- safe-output bypasses (escape-hatches; use deliberately) -------
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeAttr": func(s string) template.HTMLAttr { return template.HTMLAttr(s) },
		"safeURL":  func(s string) template.URL { return template.URL(s) },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeCSS":  func(s string) template.CSS { return template.CSS(s) },

		// --- data helpers --------------------------------------------------
		"dict":    dict,
		"default": defaultVal,

		// --- string helpers ------------------------------------------------
		"upper":    strings.ToUpper,
		"lower":    strings.ToLower,
		"title":    strings.Title, //nolint:staticcheck // adequate for ASCII view text
		"trim":     strings.TrimSpace,
		"replace":  func(old, new, s string) string { return strings.ReplaceAll(s, old, new) },
		"contains": strings.Contains,
		"split":    strings.Split,
		"join":     strings.Join,
		"truncate": truncate,

		// --- number helpers ------------------------------------------------
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"mul": func(a, b int) int { return a * b },

		// --- date helpers --------------------------------------------------
		"now":  time.Now,
		"date": func(layout string, t time.Time) string { return t.Format(layout) },
		"formatDate": func(t time.Time) string { // human-friendly default
			return t.Format("Jan 2, 2006")
		},

		// --- injectable stubs (override via Options.Funcs) ----------------
		// Default no-op-ish implementations so templates referencing them
		// parse and render even before an app injects real route/asset
		// resolvers. Returning the input keeps URLs visible in dev.
		"route":     func(name string, _ ...any) string { return "/" + name },
		"asset":     func(p string) string { return "/" + strings.TrimLeft(p, "/") },
		"url":       func(p string) string { return "/" + strings.TrimLeft(p, "/") },
		"csrfField": func() template.HTML { return template.HTML("") },
	}
}

// dict builds a map[string]any from alternating key/value pairs. It is the
// idiomatic way to pass several named values into a partial or component:
//
//	{{template "components/button" (dict "label" "Save" "kind" "primary")}}
//
// An odd number of arguments, or a non-string key, is an error surfaced by
// the template executor.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("view: dict expects an even number of args, got %d", len(pairs))
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("view: dict key %d is not a string (%T)", i, pairs[i])
		}
		m[k] = pairs[i+1]
	}
	return m, nil
}

// defaultVal returns value unless it is a zero/empty value, in which case it
// returns fallback. Mirrors Laravel/Sprig's "default": {{ .Title | default "Untitled" }}.
func defaultVal(fallback, value any) any {
	if isEmpty(value) {
		return fallback
	}
	return value
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case int:
		return x == 0
	case int64:
		return x == 0
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

// truncate shortens s to at most n runes, appending "…" when it had to cut.
func truncate(n int, s string) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
