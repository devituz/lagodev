package view

import (
	"bytes"
	"errors"
	"html/template"
	"strings"
	"sync"
	"testing"
)

// hostile is a catalogue of payloads that try to break out of their rendering
// context. Each must be neutralised by html/template's contextual escaper when
// emitted through a normal {{.}} pipeline (i.e. without a safe* opt-in).
var hostile = []string{
	`<script>alert(1)</script>`,
	`"><img src=x onerror=alert(1)>`,
	`'><svg/onload=alert(1)>`,
	`</textarea><script>alert(1)</script>`,
	`<a href="javascript:alert(1)">x</a>`,
	`javascript:alert(document.cookie)`,
	`{{.Secret}}`,                // template syntax as data
	`{{template "evil"}}{{end}}`, // injection attempt
	`<!--`,                       // comment breakout
	"\x00\x01\x02",               // control chars
	`&lt;already-escaped&gt;`,    // double-escaping probe
	"line1\nline2 line3",         // JS line separators
	`{{if true}}leak{{end}}`,     // action syntax
}

// --- HTML body context ------------------------------------------------------

// In an HTML text node every hostile payload must come out without a live
// "<script" / "onerror=" tag — the angle brackets must be entity-encoded.
func TestSecurity_BodyContext_Escapes(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<p>{{.Body}}</p>`,
	}, Options{Root: "t"})

	for _, payload := range hostile {
		out := render(t, e, "p", map[string]any{"Body": payload})
		assertNoLiveScript(t, payload, out)
		// A raw, unescaped "<script>" must never survive into body text.
		if strings.Contains(out, "<script>") {
			t.Fatalf("body ctx leaked <script> for %q: %q", payload, out)
		}
	}
}

func TestSecurity_BodyContext_ScriptIsEntityEncoded(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<p>{{.Body}}</p>`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"Body": `<script>alert(1)</script>`})
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected entity-encoded script tag, got %q", out)
	}
}

// --- HTML attribute context -------------------------------------------------

// A double-quoted attribute value must not be terminable by data: the closing
// quote and angle brackets in the payload must be encoded so the attribute (and
// the tag) cannot be broken out of.
func TestSecurity_AttributeContext_NoBreakout(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<div data-x="{{.V}}">y</div>`,
	}, Options{Root: "t"})

	for _, payload := range hostile {
		out := render(t, e, "p", map[string]any{"V": payload})
		// The attribute boundary must remain a single value: no stray '">' that
		// would close the attribute and tag mid-payload.
		inner, ok := between(out, `data-x="`, `">y</div>`)
		if !ok {
			t.Fatalf("attribute boundary moved for %q: %q", payload, out)
		}
		if strings.Contains(inner, `"`) {
			t.Fatalf("unescaped quote inside attribute for %q: %q", payload, out)
		}
		if strings.Contains(inner, "<") || strings.Contains(inner, ">") {
			t.Fatalf("unescaped angle bracket inside attribute for %q: %q", payload, out)
		}
		assertNoLiveScript(t, payload, out)
	}
}

// Unquoted attributes are the classic injection vector; html/template adds
// quoting/encoding so a space or '>' cannot introduce a new attribute or tag.
func TestSecurity_UnquotedAttributeContext(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<div data-x={{.V}}>y</div>`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"V": `x onmouseover=alert(1)`})
	// The injected handler must not appear as a bare, live attribute.
	if strings.Contains(out, " onmouseover=alert(1)>") {
		t.Fatalf("unquoted attr injection succeeded: %q", out)
	}
}

// --- URL context ------------------------------------------------------------

// In an href, a javascript: (or other dangerous) scheme coming from data must
// be neutralised to #ZgotmplZ by the URL sanitiser.
func TestSecurity_URLContext_JavascriptSchemeFiltered(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<a href="{{.U}}">x</a>`,
	}, Options{Root: "t"})

	dangerous := []string{
		`javascript:alert(1)`,
		`javascript:alert(document.cookie)`,
		`JaVaScRiPt:alert(1)`,
		`data:text/html,<script>alert(1)</script>`,
		` javascript:alert(1)`, // leading space trick
	}
	for _, u := range dangerous {
		out := render(t, e, "p", map[string]any{"U": u})
		if !strings.Contains(out, "#ZgotmplZ") {
			t.Fatalf("dangerous URL scheme not filtered for %q: %q", u, out)
		}
	}
}

func TestSecurity_URLContext_SafeSchemesSurvive(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<a href="{{.U}}">x</a>`,
	}, Options{Root: "t"})
	for _, u := range []string{"/path?a=b", "https://example.com/x", "mailto:a@b.com"} {
		out := render(t, e, "p", map[string]any{"U": u})
		if strings.Contains(out, "#ZgotmplZ") {
			t.Fatalf("safe URL %q was wrongly filtered: %q", u, out)
		}
	}
}

// --- JS / script context ----------------------------------------------------

// Inside a <script> block a string interpolated through {{.}} must be JS-string
// escaped so it cannot terminate the literal or the script element.
func TestSecurity_ScriptContext_NoBreakout(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<script>var s = {{.S}};</script>`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"S": `"; alert(1); //`})
	if strings.Contains(out, `alert(1)`) && !strings.Contains(out, `"`) && !strings.Contains(out, `\"`) {
		t.Fatalf("script-string breakout: %q", out)
	}
	if strings.Contains(out, "</script>") && !strings.HasSuffix(out, "</script>") {
		t.Fatalf("premature </script> close: %q", out)
	}
}

func TestSecurity_ScriptContext_ClosingTagNeutralised(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<script>var s = {{.S}};</script>`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"S": `</script><script>alert(1)</script>`})
	// Exactly one real <script> open and one close: the injected pair must be
	// escaped inside the JS string literal, not emitted as live tags.
	if strings.Count(out, "<script>") != 1 {
		t.Fatalf("injected <script> survived in JS ctx: %q", out)
	}
	if strings.Count(out, "</script>") != 1 {
		t.Fatalf("injected </script> survived in JS ctx: %q", out)
	}
}

// --- CSS context ------------------------------------------------------------

func TestSecurity_CSSContext_NoExpressionInjection(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<div style="color: {{.C}}">y</div>`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"C": `red; background:url(javascript:alert(1))`})
	// html/template blanks unsafe CSS to ZgotmplZ rather than emit it raw.
	if strings.Contains(out, "javascript:alert(1)") && !strings.Contains(out, "ZgotmplZ") {
		t.Fatalf("CSS injection survived: %q", out)
	}
}

// --- data-as-data (no parsing of payload template syntax) -------------------

// Data that *looks* like a template action must be rendered literally, never
// evaluated. This is the core "data is data, not code" guarantee.
func TestSecurity_DataContainingTemplateSyntax_NotParsed(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<p>{{.Body}}</p>`,
	}, Options{Root: "t"})

	secret := "TOP-SECRET-SHOULD-NOT-LEAK"
	out := render(t, e, "p", map[string]any{
		"Body":   `{{.Secret}} {{template "evil" .}} {{if .Admin}}pwned{{end}}`,
		"Secret": secret,
		"Admin":  true,
	})
	if strings.Contains(out, secret) {
		t.Fatalf("template syntax in data was evaluated, leaked secret: %q", out)
	}
	// The if-action must survive verbatim (i.e. as literal text), proving it was
	// not evaluated: the surrounding "{{if .Admin}}...{{end}}" markers are still
	// present rather than collapsed to just "pwned".
	if !strings.Contains(out, "{{if .Admin}}pwned{{end}}") {
		t.Fatalf("if-action in data was evaluated, not kept literal: %q", out)
	}
	// The literal action text must appear (with quotes entity-encoded).
	if !strings.Contains(out, "{{.Secret}}") {
		t.Fatalf("expected literal action text in output: %q", out)
	}
}

// The same guarantee must hold through the layout/inline-content path, where the
// content tree is captured in isolation and rebound into the layout. A second
// escaping pass must not occur and data must stay data.
func TestSecurity_LayoutInlineContent_Escapes(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `<div>{{block "content" .}}{{end}}</div>`,
		"t/p.html":           `{{layout "layouts/app"}}<span>{{.Body}}</span>`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"Body": `<script>alert(1)</script>`})
	if strings.Contains(out, "<script>alert(1)") {
		t.Fatalf("inline-content layout leaked live script: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("inline-content layout did not escape: %q", out)
	}
}

// Contextual escaping must follow the *layout's* context, not the view's: a
// content block landing inside an href in the layout must be URL-sanitised even
// though the view body had no surrounding URL context.
func TestSecurity_LayoutBlockContext_PropagatesURLContext(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `<a href="{{block "content" .}}{{end}}">x</a>`,
		"t/p.html":           `{{layout "layouts/app"}}{{define "content"}}{{.U}}{{end}}`,
	}, Options{Root: "t"})
	out := render(t, e, "p", map[string]any{"U": `javascript:alert(1)`})
	if !strings.Contains(out, "#ZgotmplZ") {
		t.Fatalf("URL context not enforced through layout block: %q", out)
	}
}

// --- safe* escape hatches: explicit opt-in only -----------------------------

// safeHTML is the ONLY way to emit raw markup, and it requires the author to
// call it explicitly. Without it the same data is escaped.
func TestSecurity_SafeHTML_RequiresExplicitOptIn(t *testing.T) {
	raw := `<b onclick="x">bold</b>`
	e := newEngine(t, map[string]string{
		"t/escaped.html": `{{.H}}`,
		"t/raw.html":     `{{safeHTML .H}}`,
	}, Options{Root: "t"})

	esc := render(t, e, "escaped", map[string]any{"H": raw})
	if strings.Contains(esc, "<b onclick=") {
		t.Fatalf("default pipeline emitted raw markup without safeHTML: %q", esc)
	}

	bypass := render(t, e, "raw", map[string]any{"H": raw})
	if bypass != raw {
		t.Fatalf("safeHTML opt-in should emit raw markup, got %q", bypass)
	}
}

// Each safe* func is a deliberate, named opt-in returning a typed template.*
// value; none of them fires unless the template author wrote the call. Verify
// the type each returns is the trusted-content type that bypasses its context.
func TestSecurity_SafeFuncs_AreTheOnlyBypass(t *testing.T) {
	fns := builtinFuncs()
	for _, name := range []string{"safeHTML", "safeAttr", "safeURL", "safeJS", "safeCSS"} {
		if fns[name] == nil {
			t.Fatalf("expected safe func %q to exist", name)
		}
	}

	// safeURL bypasses the scheme filter (opt-in trust), proving the filter is
	// the default and the bypass is explicit.
	e := newEngine(t, map[string]string{
		"t/filtered.html": `<a href="{{.U}}">x</a>`,
		"t/trusted.html":  `<a href="{{safeURL .U}}">x</a>`,
	}, Options{Root: "t"})

	u := "javascript:doThing()"
	filtered := render(t, e, "filtered", map[string]any{"U": u})
	if !strings.Contains(filtered, "#ZgotmplZ") {
		t.Fatalf("default href must filter javascript: scheme, got %q", filtered)
	}
	trusted := render(t, e, "trusted", map[string]any{"U": u})
	if strings.Contains(trusted, "#ZgotmplZ") {
		t.Fatalf("safeURL opt-in should bypass scheme filter, got %q", trusted)
	}
	if !strings.Contains(trusted, "javascript:") {
		t.Fatalf("safeURL should preserve scheme, got %q", trusted)
	}
}

// Confirm the safe* funcs return the html/template trusted types (compile-time
// guarantee that they are the bypass primitives, not arbitrary strings).
func TestSecurity_SafeFuncs_ReturnTrustedTypes(t *testing.T) {
	fns := builtinFuncs()
	if _, ok := fns["safeHTML"].(func(string) template.HTML); !ok {
		t.Fatalf("safeHTML must return template.HTML")
	}
	if _, ok := fns["safeAttr"].(func(string) template.HTMLAttr); !ok {
		t.Fatalf("safeAttr must return template.HTMLAttr")
	}
	if _, ok := fns["safeURL"].(func(string) template.URL); !ok {
		t.Fatalf("safeURL must return template.URL")
	}
	if _, ok := fns["safeJS"].(func(string) template.JS); !ok {
		t.Fatalf("safeJS must return template.JS")
	}
	if _, ok := fns["safeCSS"].(func(string) template.CSS); !ok {
		t.Fatalf("safeCSS must return template.CSS")
	}
}

// --- robustness: errors, not panics -----------------------------------------

func TestRobust_MissingTemplate_NoPanic(t *testing.T) {
	e := newEngine(t, map[string]string{"t/a.html": `a`}, Options{Root: "t"})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Render of missing view panicked: %v", r)
		}
	}()
	err := e.Render(&bytes.Buffer{}, "does/not/exist", nil)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("expected ErrTemplateNotFound, got %v", err)
	}
}

func TestRobust_MalformedTemplateDefinitions_ErrorCleanly(t *testing.T) {
	cases := map[string]map[string]string{
		"unterminated if":     {"t/a.html": `{{if .X}}no end`},
		"unterminated define": {"t/a.html": `{{define "x"}}no end`},
		"empty pipeline":      {"t/a.html": `{{ | }}`},
		"unknown func":        {"t/a.html": `{{nosuchfunc .X}}`},
		"unbalanced braces":   {"t/a.html": `{{.X`},
		"bad layout body": {
			"t/layouts/app.html": `[{{block "content" .}}{{end}}]`,
			"t/p.html":           `{{layout "layouts/app"}}{{define "content"}}{{if .X}}{{end}}`,
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("New panicked on malformed template: %v", r)
				}
			}()
			if _, err := New(fsWith(files), Options{Root: "t"}); err == nil {
				t.Fatalf("expected error for malformed template %q", name)
			}
		})
	}
}

// --- robustness: failing io.Writer propagates -------------------------------

// failWriter fails after writing limit bytes, simulating a dropped connection.
type failWriter struct {
	limit   int
	written int
}

func (w *failWriter) Write(p []byte) (int, error) {
	w.written += len(p)
	if w.written > w.limit {
		return 0, errors.New("failWriter: simulated write failure")
	}
	return len(p), nil
}

func TestRobust_FailingWriter_PropagatesError(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `<html><body>{{block "content" .}}{{end}}</body></html>`,
		"t/p.html":           `{{layout "layouts/app"}}{{define "content"}}{{.Name}} and more content to overflow{{end}}`,
	}, Options{Root: "t"})

	err := e.Render(&failWriter{limit: 4}, "p", map[string]any{"Name": "Ada"})
	if err == nil {
		t.Fatal("expected error from failing writer to propagate")
	}
	if !strings.Contains(err.Error(), "render") {
		t.Fatalf("error should be wrapped with render context: %v", err)
	}
}

func TestRobust_FailingWriter_Standalone(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `Hello {{.Name}}, this is a fairly long body to exceed the limit`,
	}, Options{Root: "t"})
	err := e.Render(&failWriter{limit: 2}, "p", map[string]any{"Name": "Bob"})
	if err == nil {
		t.Fatal("expected error from failing writer (standalone)")
	}
}

// --- concurrency under -race ------------------------------------------------

// Hammer the parse-once cache with concurrent renders of the same and different
// views, mixing layout, inline-content, standalone, and partial views, plus
// Names() reads. Must be race-free (run with -race).
func TestRobust_ConcurrentRender_SameAndDifferentViews(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/layouts/app.html":  `<html><body>{{block "content" .}}{{end}}</body></html>`,
		"t/partials/nav.html": `<nav>{{.N}}</nav>`,
		"t/a.html":            `{{layout "layouts/app"}}{{define "content"}}<h1>A {{.N}}</h1>{{end}}`,
		"t/b.html":            `{{layout "layouts/app"}}<h2>B inline {{.N}}</h2>`,
		"t/c.html":            `standalone {{.N}} {{template "partials/nav" .}}`,
	}, Options{Root: "t", Globals: map[string]any{"Site": "lago"}})

	const workers = 64
	var wg sync.WaitGroup
	errCh := make(chan error, workers*4)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := map[string]any{"N": n, "Body": `<script>x</script>`}
			for _, name := range []string{"a", "b", "c"} {
				out, err := e.RenderToString(name, data)
				if err != nil {
					errCh <- err
					return
				}
				if strings.Contains(out, "<script>x</script>") {
					errCh <- errors.New("escaping broke under concurrency: " + out)
					return
				}
			}
			_ = e.Names()
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

// --- table fuzz over data shapes: no panic ----------------------------------

// fuzzData is a spread of awkward data shapes a caller might pass. None may
// panic the engine; either clean render or clean error is acceptable.
var fuzzData = []any{
	nil,
	"",
	"plain",
	`<script>alert(1)</script>`,
	0,
	-1,
	3.14,
	true,
	false,
	[]any{},
	[]any{1, "two", nil},
	map[string]any{},
	map[string]any{"X": nil},
	map[string]any{"X": `"><img onerror=alert(1)>`},
	map[string]any{"X": map[string]any{"nested": `<b>`}},
	map[string]any{"X": []any{`<i>`, "<u>"}},
	struct{ X string }{X: "<svg/onload=alert(1)>"},
	template.HTML(`<b>trusted</b>`),
}

func TestFuzz_DataShapes_NoPanic(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/body.html":  `<p>{{.X}}</p>`,
		"t/attr.html":  `<div data-x="{{.X}}">y</div>`,
		"t/href.html":  `<a href="{{.X}}">y</a>`,
		"t/dot.html":   `<p>{{.}}</p>`,
		"t/range.html": `{{range .X}}[{{.}}]{{end}}`,
	}, Options{Root: "t"})

	views := []string{"body", "attr", "href", "dot", "range"}
	for _, name := range views {
		for i, d := range fuzzData {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic rendering %q with fuzzData[%d]=%#v: %v", name, i, d, r)
					}
				}()
				// Error is fine; panic is not. Discard output.
				_ = e.Render(&bytes.Buffer{}, name, d)
			}()
		}
	}
}

// FuzzRender drives arbitrary byte payloads through the body, attribute, and
// URL contexts and asserts no panic and no raw "<script>" survives a normal
// (non-safe) pipeline. Run: go test -run x -fuzz FuzzRender ./view
func FuzzRender(f *testing.F) {
	e, err := New(fsWith(map[string]string{
		"t/body.html": `<p>{{.X}}</p>`,
		"t/attr.html": `<div data-x="{{.X}}">y</div>`,
		"t/href.html": `<a href="{{.X}}">y</a>`,
	}), Options{Root: "t"})
	if err != nil {
		f.Fatalf("New: %v", err)
	}

	seeds := []string{
		"", "plain", `<script>alert(1)</script>`, `"><img onerror=x>`,
		`javascript:alert(1)`, `{{.Secret}}`, "\x00\xff", "a\nb c",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, payload string) {
		for _, name := range []string{"body", "attr", "href"} {
			func() {
				var buf bytes.Buffer
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic in %q with %q: %v", name, payload, r)
					}
				}()
				if err := e.Render(&buf, name, map[string]any{"X": payload}); err != nil {
					return
				}
				out := buf.String()
				// A normal pipeline must never emit a live, unescaped <script> tag.
				if strings.Contains(out, "<script>") && !strings.Contains(payload, "<script>") {
					t.Fatalf("synthesised <script> in %q out=%q", name, out)
				}
				if strings.Contains(out, "<script>alert") {
					t.Fatalf("live script survived in %q out=%q", name, out)
				}
			}()
		}
	})
}

// --- small helpers ----------------------------------------------------------

// assertNoLiveScript fails if a normal (non-safe) render produced a live script
// tag or inline event handler that was not already present verbatim... actually
// any live "<script" must be absent because the body/attr contexts encode '<'.
func assertNoLiveScript(t *testing.T, payload, out string) {
	t.Helper()
	if strings.Contains(out, "<script") {
		t.Fatalf("live <script in output for payload %q: %q", payload, out)
	}
	if strings.Contains(out, "onerror=") && !strings.Contains(out, "&") {
		t.Fatalf("live onerror handler for payload %q: %q", payload, out)
	}
}

// between returns the substring of s between the first occurrence of pre and
// the following suf, and whether both boundaries were found in order.
func between(s, pre, suf string) (string, bool) {
	i := strings.Index(s, pre)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(pre):]
	j := strings.Index(rest, suf)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
