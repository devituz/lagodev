package view

import (
	"bytes"
	"errors"
	"html/template"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// fsWith builds a fstest.MapFS from name->content pairs.
func fsWith(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

// newEngine is a test helper that fails on construction error.
func newEngine(t *testing.T, files map[string]string, opts Options) *Engine {
	t.Helper()
	e, err := New(fsWith(files), opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func render(t *testing.T, e *Engine, name string, data any) string {
	t.Helper()
	s, err := e.RenderToString(name, data)
	if err != nil {
		t.Fatalf("RenderToString(%q): %v", name, err)
	}
	return s
}

// --- construction & loading -------------------------------------------------

func TestNew_NilFS(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("expected error for nil fs.FS")
	}
}

func TestNew_DefaultExt(t *testing.T) {
	e := newEngine(t, map[string]string{
		"templates/pages/home.html": `hi`,
		"templates/pages/home.txt":  `ignored`, // wrong ext
	}, Options{Root: "templates"})

	got := e.Names()
	if !contains(got, "pages/home") {
		t.Fatalf("expected pages/home in names, got %v", got)
	}
	for _, n := range got {
		if strings.Contains(n, ".txt") {
			t.Fatalf("non-.html file was loaded: %v", got)
		}
	}
}

func TestNew_CustomExt(t *testing.T) {
	e := newEngine(t, map[string]string{
		"v/a.tmpl": `A`,
		"v/b.html": `B`, // wrong ext now
	}, Options{Root: "v", Ext: ".tmpl"})

	if !contains(e.Names(), "a") {
		t.Fatalf("expected a, got %v", e.Names())
	}
	if contains(e.Names(), "b") {
		t.Fatalf("b should be ignored with Ext=.tmpl, got %v", e.Names())
	}
}

func TestNames_TrimsRootAndExt(t *testing.T) {
	e := newEngine(t, map[string]string{
		"templates/pages/home.html":   `home`,
		"templates/partials/nav.html": `nav`,
		"templates/layouts/app.html":  `{{block "content" .}}{{end}}`,
	}, Options{Root: "templates"})

	for _, want := range []string{"pages/home", "partials/nav", "layouts/app"} {
		if !contains(e.Names(), want) {
			t.Fatalf("missing %q in %v", want, e.Names())
		}
	}
}

func TestNew_NoRoot(t *testing.T) {
	e := newEngine(t, map[string]string{
		"home.html":  `home`,
		"about.html": `about`,
	}, Options{})
	if !contains(e.Names(), "home") || !contains(e.Names(), "about") {
		t.Fatalf("expected home+about, got %v", e.Names())
	}
}

// --- rendering: standalone, layout, partials --------------------------------

func TestRender_Standalone(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/hello.html": `Hello {{.Name}}`,
	}, Options{Root: "t"})

	if got := render(t, e, "hello", map[string]any{"Name": "Ada"}); got != "Hello Ada" {
		t.Fatalf("got %q", got)
	}
}

func TestRender_ToWriter(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/hello.html": `Hi {{.Name}}`,
	}, Options{Root: "t"})

	var buf bytes.Buffer
	if err := e.Render(&buf, "hello", map[string]any{"Name": "Bob"}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "Hi Bob" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestRender_LayoutComposition(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `<html><body><main>{{block "content" .}}{{end}}</main></body></html>`,
		"t/pages/home.html": `{{layout "layouts/app"}}` +
			`{{define "content"}}<h1>Hello {{.Name}}</h1>{{end}}`,
	}, Options{Root: "t"})

	got := render(t, e, "pages/home", map[string]any{"Name": "Ada"})
	want := `<html><body><main><h1>Hello Ada</h1></main></body></html>`
	if got != want {
		t.Fatalf("layout compose:\n got=%q\nwant=%q", got, want)
	}
}

func TestRender_LayoutWithLeadingWhitespace(t *testing.T) {
	// {{layout}} only honoured at the very start after optional whitespace.
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `[{{block "content" .}}{{end}}]`,
		"t/p.html":           "  \n\t" + `{{layout "layouts/app"}}{{define "content"}}X{{end}}`,
	}, Options{Root: "t"})

	if got := render(t, e, "p", nil); got != `[X]` {
		t.Fatalf("got %q", got)
	}
}

func TestRender_TwoViewsShareLayout_Isolation(t *testing.T) {
	// Distinct "content" definitions per view must not bleed across renders
	// because Render clones the set.
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `[{{block "content" .}}{{end}}]`,
		"t/a.html":           `{{layout "layouts/app"}}{{define "content"}}AAA{{end}}`,
		"t/b.html":           `{{layout "layouts/app"}}{{define "content"}}BBB{{end}}`,
	}, Options{Root: "t"})

	if got := render(t, e, "a", nil); got != `[AAA]` {
		t.Fatalf("a got %q", got)
	}
	if got := render(t, e, "b", nil); got != `[BBB]` {
		t.Fatalf("b got %q", got)
	}
	// Render a again to ensure no residual state from b.
	if got := render(t, e, "a", nil); got != `[AAA]` {
		t.Fatalf("a (2nd) got %q", got)
	}
}

func TestRender_LayoutInlineContent(t *testing.T) {
	// A layout-using view that writes its body inline without an explicit
	// {{define "content"}} must still compose: reparse falls back to the body
	// registered under the view name as the content tree.
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `[{{block "content" .}}{{end}}]`,
		"t/p.html":           `{{layout "layouts/app"}}<h1>{{.Name}}</h1>`,
	}, Options{Root: "t"})

	if got := render(t, e, "p", map[string]any{"Name": "Ada"}); got != `[<h1>Ada</h1>]` {
		t.Fatalf("inline-content layout got %q", got)
	}
}

func TestSplitLayout_Unterminated(t *testing.T) {
	// A {{layout directive missing its closing }} is not a layout directive:
	// the source is returned untouched and the view renders standalone.
	body, layout := splitLayout(`{{layout "layouts/app"`)
	if layout != "" {
		t.Fatalf("unterminated layout should yield empty layout, got %q", layout)
	}
	if body != `{{layout "layouts/app"` {
		t.Fatalf("body should be returned verbatim, got %q", body)
	}
}

func TestRender_Partial(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/partials/nav.html": `[nav]`,
		"t/page.html":         `top {{template "partials/nav" .}} bottom`,
	}, Options{Root: "t"})

	if got := render(t, e, "page", nil); got != `top [nav] bottom` {
		t.Fatalf("got %q", got)
	}
}

func TestRender_ComponentWithDict(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/components/button.html": `<button class="{{.kind}}">{{.label}}</button>`,
		"t/page.html":              `{{template "components/button" (dict "label" "Save" "kind" "primary")}}`,
	}, Options{Root: "t"})

	got := render(t, e, "page", nil)
	if got != `<button class="primary">Save</button>` {
		t.Fatalf("got %q", got)
	}
}

// --- globals ---------------------------------------------------------------

func TestRender_GlobalsMergedUnderMapData(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `site={{.Site}} page={{.Page}} g={{.globals.Site}}`,
	}, Options{
		Root:    "t",
		Globals: map[string]any{"Site": "Lago", "Page": "default"},
	})

	// Per-render key wins over global of the same name.
	got := render(t, e, "p", map[string]any{"Page": "home"})
	want := `site=Lago page=home g=Lago`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRender_NonMapDataPassesThrough(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `val={{.}}`,
	}, Options{
		Root:    "t",
		Globals: map[string]any{"Site": "Lago"},
	})
	// Non-map data is passed through untouched (no globals injection).
	if got := render(t, e, "p", "raw"); got != `val=raw` {
		t.Fatalf("got %q", got)
	}
}

func TestRender_NoGlobalsKeepsMapData(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `{{.X}}`,
	}, Options{Root: "t"})
	if got := render(t, e, "p", map[string]any{"X": "y"}); got != "y" {
		t.Fatalf("got %q", got)
	}
}

// --- auto-escaping ----------------------------------------------------------

func TestRender_AutoEscapesHTML(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `<p>{{.Body}}</p>`,
	}, Options{Root: "t"})

	got := render(t, e, "p", map[string]any{"Body": `<script>alert(1)</script>`})
	if strings.Contains(got, "<script>") {
		t.Fatalf("html not escaped: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("expected escaped entities, got %q", got)
	}
}

func TestRender_SafeHTMLBypass(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `{{safeHTML .Raw}}`,
	}, Options{Root: "t"})

	got := render(t, e, "p", map[string]any{"Raw": `<b>x</b>`})
	if got != `<b>x</b>` {
		t.Fatalf("safeHTML should not escape, got %q", got)
	}
}

// --- error paths ------------------------------------------------------------

func TestRender_MissingView(t *testing.T) {
	e := newEngine(t, map[string]string{"t/a.html": `a`}, Options{Root: "t"})
	err := e.Render(&bytes.Buffer{}, "nope", nil)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("expected ErrTemplateNotFound, got %v", err)
	}
}

func TestRender_MissingLayout(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `{{layout "layouts/ghost"}}{{define "content"}}X{{end}}`,
	}, Options{Root: "t"})
	err := e.Render(&bytes.Buffer{}, "p", nil)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("expected ErrTemplateNotFound for missing layout, got %v", err)
	}
	if !strings.Contains(err.Error(), "layout") {
		t.Fatalf("error should mention layout: %v", err)
	}
}

func TestNew_ParseError(t *testing.T) {
	_, err := New(fsWith(map[string]string{
		"t/bad.html": `{{ if .X }}unterminated`,
	}), Options{Root: "t"})
	if err == nil {
		t.Fatal("expected parse error at New")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error should mention parse: %v", err)
	}
}

func TestRender_RuntimeError(t *testing.T) {
	// Calling a method that doesn't exist surfaces as a render error.
	e := newEngine(t, map[string]string{
		"t/p.html": `{{.MissingMethod 1 2 3}}`,
	}, Options{Root: "t"})
	err := e.Render(&bytes.Buffer{}, "p", map[string]any{})
	if err == nil {
		t.Fatal("expected render error")
	}
	if !strings.Contains(err.Error(), "render") {
		t.Fatalf("error should mention render: %v", err)
	}
}

func TestRenderToString_PropagatesError(t *testing.T) {
	e := newEngine(t, map[string]string{"t/a.html": `a`}, Options{Root: "t"})
	s, err := e.RenderToString("nope", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if s != "" {
		t.Fatalf("expected empty string on error, got %q", s)
	}
}

// --- Dev hot-reload ---------------------------------------------------------

func TestRender_DevHotReload(t *testing.T) {
	// Mutable FS to simulate on-disk edits in Dev mode.
	mfs := fsWith(map[string]string{"t/p.html": `v1`})
	e, err := New(mfs, Options{Root: "t", Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, e, "p", nil); got != "v1" {
		t.Fatalf("got %q", got)
	}
	// Edit the file; Dev mode must re-read on next Render.
	mfs["t/p.html"] = &fstest.MapFile{Data: []byte("v2")}
	if got := render(t, e, "p", nil); got != "v2" {
		t.Fatalf("Dev hot-reload failed, got %q", got)
	}
}

func TestRender_ProdNoReload(t *testing.T) {
	mfs := fsWith(map[string]string{"t/p.html": `v1`})
	e, err := New(mfs, Options{Root: "t"}) // Dev=false
	if err != nil {
		t.Fatal(err)
	}
	mfs["t/p.html"] = &fstest.MapFile{Data: []byte("v2")}
	if got := render(t, e, "p", nil); got != "v1" {
		t.Fatalf("prod must not reload, got %q", got)
	}
}

func TestRender_DevReparseError(t *testing.T) {
	mfs := fsWith(map[string]string{"t/p.html": `ok`})
	e, err := New(mfs, Options{Root: "t", Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	// Introduce a parse error; Dev Render must surface it.
	mfs["t/p.html"] = &fstest.MapFile{Data: []byte(`{{ if`)}
	if err := e.Render(&bytes.Buffer{}, "p", nil); err == nil {
		t.Fatal("expected reparse error in Dev mode")
	}
}

// --- injectable funcs override ----------------------------------------------

func TestRender_FuncsOverrideBuiltins(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `{{route "home"}}`,
	}, Options{
		Root: "t",
		Funcs: template.FuncMap{
			"route": func(name string, _ ...any) string { return "https://x/" + name },
		},
	})
	if got := render(t, e, "p", nil); got != `https://x/home` {
		t.Fatalf("override failed, got %q", got)
	}
}

func TestRender_BuiltinStubs(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `{{route "home"}}|{{asset "css/app.css"}}|{{url "x"}}`,
	}, Options{Root: "t"})
	got := render(t, e, "p", nil)
	if got != `/home|/css/app.css|/x` {
		t.Fatalf("builtin stubs: got %q", got)
	}
}

// --- concurrency (-race) ----------------------------------------------------

func TestRender_ConcurrentProd(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/layouts/app.html": `<{{block "content" .}}{{end}}>`,
		"t/a.html":           `{{layout "layouts/app"}}{{define "content"}}A{{.N}}{{end}}`,
		"t/b.html":           `{{layout "layouts/app"}}{{define "content"}}B{{.N}}{{end}}`,
		"t/c.html":           `standalone {{.N}}`,
	}, Options{Root: "t"})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := map[string]any{"N": n}
			if _, err := e.RenderToString("a", data); err != nil {
				t.Errorf("a: %v", err)
			}
			if _, err := e.RenderToString("b", data); err != nil {
				t.Errorf("b: %v", err)
			}
			if _, err := e.RenderToString("c", data); err != nil {
				t.Errorf("c: %v", err)
			}
			_ = e.Names()
		}(i)
	}
	wg.Wait()
}

func TestRender_ConcurrentDev(t *testing.T) {
	mfs := fsWith(map[string]string{
		"t/p.html": `{{.N}}`,
	})
	e, err := New(mfs, Options{Root: "t", Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, err := e.RenderToString("p", map[string]any{"N": n}); err != nil {
				t.Errorf("render: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

// ===========================================================================
// funcs.go coverage
// ===========================================================================

func TestDict(t *testing.T) {
	m, err := dict("a", 1, "b", "two")
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != 1 || m["b"] != "two" {
		t.Fatalf("got %v", m)
	}

	if _, err := dict("a"); err == nil {
		t.Fatal("expected error for odd args")
	}
	if _, err := dict(1, "x"); err == nil {
		t.Fatal("expected error for non-string key")
	}
	// Zero args is a valid empty dict.
	if m, err := dict(); err != nil || len(m) != 0 {
		t.Fatalf("empty dict: m=%v err=%v", m, err)
	}
}

func TestDefaultVal(t *testing.T) {
	cases := []struct {
		name     string
		fallback any
		value    any
		want     any
	}{
		{"empty string", "fb", "", "fb"},
		{"nonempty string", "fb", "x", "x"},
		{"nil", "fb", nil, "fb"},
		{"false", "fb", false, "fb"},
		{"true", "fb", true, true},
		{"zero int", 9, 0, 9},
		{"nonzero int", 9, 3, 3},
		{"zero int64", 9, int64(0), 9},
		{"zero float", 9, float64(0), 9},
		{"empty slice", "fb", []any{}, "fb"},
		{"nonempty slice", "fb", []any{1}, []any{1}},
		{"empty map", "fb", map[string]any{}, "fb"},
		{"unknown type non-empty", "fb", struct{}{}, struct{}{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := defaultVal(c.fallback, c.value)
			// Compare slices/maps by length to avoid uncomparable panics.
			switch want := c.want.(type) {
			case []any:
				g, ok := got.([]any)
				if !ok || len(g) != len(want) {
					t.Fatalf("got %v want %v", got, want)
				}
			default:
				if got != c.want {
					t.Fatalf("got %v want %v", got, c.want)
				}
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		n    int
		s    string
		want string
	}{
		{0, "abc", ""},
		{-1, "abc", ""},
		{3, "abc", "abc"},     // exact length, no cut
		{5, "abc", "abc"},     // shorter than n
		{2, "abcd", "ab…"},    // cut + ellipsis
		{1, "héllo", "h…"},    // rune-aware
		{2, "héllo", "hé…"},   // multi-byte rune counted as one
		{4, "héllo", "héll…"}, // cut before last rune
	}
	for _, c := range cases {
		if got := truncate(c.n, c.s); got != c.want {
			t.Fatalf("truncate(%d,%q)=%q want %q", c.n, c.s, got, c.want)
		}
	}
}

func TestIsEmpty(t *testing.T) {
	empties := []any{nil, "", false, 0, int64(0), float64(0), []any{}, map[string]any{}}
	for _, v := range empties {
		if !isEmpty(v) {
			t.Fatalf("expected empty: %#v", v)
		}
	}
	nonEmpties := []any{"x", true, 1, int64(1), float64(1), []any{0}, map[string]any{"a": 1}, struct{}{}}
	for _, v := range nonEmpties {
		if isEmpty(v) {
			t.Fatalf("expected non-empty: %#v", v)
		}
	}
}

// Exercise the string/number/date/safe funcs through actual template execution.
func TestBuiltinFuncs_ViaTemplate(t *testing.T) {
	fixed := time.Date(2025, 1, 2, 15, 4, 5, 0, time.UTC)
	e := newEngine(t, map[string]string{
		"t/p.html": strings.Join([]string{
			`{{upper "ab"}}`,
			`{{lower "AB"}}`,
			`{{title "hello world"}}`,
			`{{trim "  x  "}}`,
			`{{replace "a" "b" "aaa"}}`,
			`{{contains "abc" "b"}}`,
			`{{join (split "a,b,c" ",") "-"}}`,
			`{{truncate 2 "abcd"}}`,
			`{{add 2 3}}`,
			`{{sub 5 2}}`,
			`{{mul 4 3}}`,
			`{{date "2006-01-02" .T}}`,
			`{{formatDate .T}}`,
		}, "|"),
	}, Options{Root: "t"})

	got := render(t, e, "p", map[string]any{"T": fixed})
	want := strings.Join([]string{
		"AB",          // upper
		"ab",          // lower
		"Hello World", // title
		"x",           // trim
		"bbb",         // replace
		"true",        // contains
		"a-b-c",       // join(split)
		"ab…",         // truncate
		"5",           // add
		"3",           // sub
		"12",          // mul
		"2025-01-02",  // date
		"Jan 2, 2025", // formatDate
	}, "|")
	if got != want {
		t.Fatalf("builtin funcs:\n got=%q\nwant=%q", got, want)
	}
}

func TestSafeFuncs_Types(t *testing.T) {
	e := newEngine(t, map[string]string{
		// safeURL on an href that would otherwise be filtered to #ZgotmplZ.
		"t/p.html": `<a href="{{safeURL .U}}">x</a> <style>{{safeCSS .C}}</style> <script>{{safeJS .J}}</script> <span {{safeAttr .A}}>y</span>`,
	}, Options{Root: "t"})

	got := render(t, e, "p", map[string]any{
		"U": "javascript:ok()",
		"C": "color:red",
		"J": "var x=1",
		"A": `data-x="1"`,
	})
	if strings.Contains(got, "ZgotmplZ") {
		t.Fatalf("safe* bypass failed (filtered): %q", got)
	}
	// template.URL bypasses the scheme filter (no #ZgotmplZ); html/template
	// still percent-normalizes the URL in href context, so the scheme survives
	// but "()" become "%28%29".
	if !strings.Contains(got, "javascript:") {
		t.Fatalf("safeURL scheme filtered out: %q", got)
	}
	if !strings.Contains(got, "color:red") {
		t.Fatalf("safeCSS not passed through: %q", got)
	}
	if !strings.Contains(got, "var x=1") {
		t.Fatalf("safeJS not passed through: %q", got)
	}
	if !strings.Contains(got, `data-x="1"`) {
		t.Fatalf("safeAttr not passed through: %q", got)
	}
}

func TestCsrfField_Builtin(t *testing.T) {
	e := newEngine(t, map[string]string{
		"t/p.html": `[{{csrfField}}]`,
	}, Options{Root: "t"})
	if got := render(t, e, "p", nil); got != `[]` {
		t.Fatalf("default csrfField should be empty, got %q", got)
	}
}

// --- os.DirFS / real fs.FS path with sub-FS (testdata-style) ----------------

func TestNew_WithSubFS(t *testing.T) {
	// Verify the engine works against a sub-FS (fs.Sub), the common pattern
	// with embed.FS / os.DirFS roots.
	base := fsWith(map[string]string{
		"assets/templates/home.html": `home`,
	})
	sub, err := fs.Sub(base, "assets")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(sub, Options{Root: "templates"})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, e, "home", nil); got != "home" {
		t.Fatalf("got %q", got)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
