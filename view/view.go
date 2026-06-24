// Package view is a thin, ergonomic layer over the standard library's
// html/template, modelled on Laravel's Blade view layer.
//
// It loads templates from any [fs.FS] — an [embed.FS] for a single-binary
// deploy, or an on-disk directory with hot-reload during development — and
// adds the conveniences a real app needs on top of html/template:
//
//   - Named views resolved by path (e.g. "pages/home").
//   - Layouts: a view declares its layout and its body is injected into the
//     layout's {{block "content"}} placeholder.
//   - Partials / includes and reusable components via {{template ...}}.
//   - A batteries-included FuncMap (dict, default, date/string/number
//     helpers) plus injectable route/asset/url helpers so this package never
//     imports the web layer — it stays a leaf.
//   - Auto-escaping is inherited from html/template (contextual, on by
//     default).
//   - Per-render data merged over shared globals.
//
// The package is deliberately a leaf: [Engine.Render] writes rendered bytes
// to any [io.Writer] the caller passes (an http.ResponseWriter, a
// bytes.Buffer for mail, etc.). It does not import the web package, so the
// web layer depends on view and not the other way around.
//
// # Layouts
//
// A layout is an ordinary template that yields to its child via a block:
//
//	{{/* layouts/app.html */}}
//	<html><body><main>{{block "content" .}}{{end}}</main></body></html>
//
// A view redefines that block and declares which layout wraps it through the
// {{layout "name"}} directive on its first line:
//
//	{{layout "layouts/app"}}
//	{{define "content"}}<h1>Hello {{.Name}}</h1>{{end}}
//
// Render then executes the layout with the view's "content" definition in
// scope. A view without a {{layout}} directive is rendered standalone.
//
// # Partials and components
//
// Any loaded template is addressable by name, so partials and components are
// just {{template "name" .}}; pass several values into a component with dict:
//
//	{{template "partials/nav" .}}
//	{{template "components/button" (dict "label" "Save" "kind" "primary")}}
//
// # Usage
//
//	//go:embed templates
//	var tplFS embed.FS
//
//	eng, err := view.New(tplFS, view.Options{Root: "templates"})
//	// ...
//	eng.Render(w, "pages/home", map[string]any{"Name": "Ada"})
//
// # Rendering mail
//
// Because Render targets any io.Writer, the mail package can render an HTML
// body without view importing mail:
//
//	var buf bytes.Buffer
//	eng.Render(&buf, "emails/welcome", data)
//	msg := mail.NewMessage().To(addr).HTML(buf.String()).Build()
package view

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"text/template/parse"
)

// ErrTemplateNotFound is returned by Render/RenderToString when the requested
// view name was never loaded.
var ErrTemplateNotFound = errors.New("view: template not found")

// layoutDirective is the magic prefix a view uses to declare its wrapping
// layout: {{layout "layouts/app"}}. It is stripped before parsing and the
// referenced layout recorded for that view.
const (
	layoutOpen  = "{{layout "
	layoutClose = "}}"
)

// Options configures an Engine.
type Options struct {
	// Root is the sub-directory of the FS that holds templates. The Root
	// prefix is trimmed from every view name, so a file at
	// "templates/pages/home.html" with Root "templates" is addressed as
	// "pages/home". Empty means the FS root.
	Root string

	// Ext is the template file extension to load, including the dot. When
	// empty it defaults to ".html". Files with other extensions are ignored.
	Ext string

	// Dev enables hot-reload: every Render re-reads and re-parses the
	// templates from the FS so edits are picked up without a restart. Leave
	// false in production for a parse-once, lock-free fast path. Dev relies on
	// the FS reflecting on-disk changes (os.DirFS does; embed.FS does not).
	Dev bool

	// Funcs are extra template functions merged over the built-ins. Injecting
	// route/asset/url helpers here keeps view a leaf package.
	Funcs template.FuncMap

	// Globals are values exposed to every render under the "globals" key and
	// merged into map data (see Render). Per-render data wins on conflict.
	Globals map[string]any
}

// Engine compiles and renders a set of named templates from an fs.FS.
//
// An Engine is safe for concurrent use. In production (Dev=false) templates
// are parsed once at New and reads are lock-free against an immutable set; in
// Dev mode the set is rebuilt under a mutex on every Render.
type Engine struct {
	fsys    fs.FS
	root    string
	ext     string
	dev     bool
	funcs   template.FuncMap
	globals map[string]any

	mu sync.RWMutex
	// set is the parsed template set. layouts maps a view name to the layout
	// name it declared. content holds, per layout-using view, the parse tree to
	// bind as the layout's "content" block — kept per view so two views sharing
	// a layout never collide in the shared set's namespace. All three are
	// replaced atomically on (re)parse.
	set     *template.Template
	layouts map[string]string
	content map[string]*parse.Tree
}

// New constructs an Engine over fsys and parses the templates immediately so
// syntax errors surface at startup (in Dev mode they also re-surface on each
// Render). A nil fsys is an error.
func New(fsys fs.FS, opts Options) (*Engine, error) {
	if fsys == nil {
		return nil, errors.New("view: nil fs.FS")
	}
	ext := opts.Ext
	if ext == "" {
		ext = ".html"
	}
	e := &Engine{
		fsys:    fsys,
		root:    strings.Trim(opts.Root, "/"),
		ext:     ext,
		dev:     opts.Dev,
		funcs:   opts.Funcs,
		globals: opts.Globals,
	}
	if err := e.reparse(); err != nil {
		return nil, err
	}
	return e, nil
}

// Names returns the sorted list of view names currently loaded. Useful for
// diagnostics and tests.
func (e *Engine) Names() []string {
	e.mu.RLock()
	set := e.set
	e.mu.RUnlock()
	var out []string
	for _, t := range set.Templates() {
		if n := t.Name(); n != "" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// reparse walks the FS, strips layout directives, and builds a fresh template
// set under the engine's FuncMap. It swaps the result in atomically so an
// in-flight Render in Dev mode never observes a half-built set.
func (e *Engine) reparse() error {
	funcs := template.FuncMap{}
	maps.Copy(funcs, builtinFuncs())
	maps.Copy(funcs, e.funcs)
	root := template.New("").Funcs(funcs)

	layouts := map[string]string{}
	content := map[string]*parse.Tree{}
	walkRoot := "."
	if e.root != "" {
		walkRoot = e.root
	}

	err := fs.WalkDir(e.fsys, walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, e.ext) {
			return nil
		}
		raw, err := fs.ReadFile(e.fsys, p)
		if err != nil {
			return fmt.Errorf("view: read %s: %w", p, err)
		}
		name := e.viewName(p)
		body, layout := splitLayout(string(raw))

		// Non-layout view: parse straight into the shared root so it is
		// addressable by name as a partial/component too.
		if layout == "" {
			if _, err := root.New(name).Parse(body); err != nil {
				return fmt.Errorf("view: parse %s: %w", name, err)
			}
			return nil
		}

		// Layout view: parse the body in a fresh, isolated set — NOT a clone of
		// root — so its {{define "content"}} (and any other defines) neither
		// collide with another view's same-named defines nor pick up the empty
		// "content" template that a layout's {{block "content"}} registers in
		// root. Cloning root here would make Lookup("content") return that empty
		// block tree, masking the inline-body fallback below. The view's content
		// tree is captured per view and bound into the layout at render time.
		// The body is still registered under the view name in root so
		// Lookup(name) succeeds and the view stays addressable.
		layouts[name] = layout
		iso := template.New("").Funcs(funcs)
		if _, err := iso.New(name).Parse(body); err != nil {
			return fmt.Errorf("view: parse %s: %w", name, err)
		}
		// Prefer an explicit {{define "content"}} in the body (the documented
		// style); fall back to the body itself when the author wrote the
		// content inline without a define. Because iso is a fresh set, a
		// non-nil Lookup("content") means the body itself defined it.
		tree := iso.Lookup("content")
		if tree == nil {
			tree = iso.Lookup(name)
		}
		content[name] = tree.Tree.Copy()
		if _, err := root.New(name).Parse(""); err != nil {
			return fmt.Errorf("view: register %s: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.set = root
	e.layouts = layouts
	e.content = content
	e.mu.Unlock()
	return nil
}

// viewName converts a file path into its addressable view name: the Root
// prefix and the extension are trimmed and separators normalised to "/".
func (e *Engine) viewName(p string) string {
	p = path.Clean(filepath_ToSlash(p))
	if e.root != "" {
		p = strings.TrimPrefix(p, e.root+"/")
	}
	return strings.TrimSuffix(p, e.ext)
}

// splitLayout pulls a leading {{layout "name"}} directive off the source,
// returning the remaining body and the declared layout name (empty if none).
// Only a directive at the very start (after optional whitespace) is honoured.
func splitLayout(src string) (body, layout string) {
	trimmed := strings.TrimLeft(src, " \t\r\n")
	if !strings.HasPrefix(trimmed, layoutOpen) {
		return src, ""
	}
	rest := trimmed[len(layoutOpen):]
	arg, body, ok := strings.Cut(rest, layoutClose)
	if !ok {
		return src, ""
	}
	arg = strings.Trim(strings.TrimSpace(arg), `"`)
	return body, arg
}

// Render executes the named view and writes the result to w. When the view
// declared a {{layout}}, the layout is executed with the view's "content"
// block in scope; otherwise the view is rendered standalone.
//
// data is the per-render payload. When it is a map[string]any, the engine
// merges the configured Globals beneath it (per-render keys win) and also
// exposes the whole globals map under the "globals" key. For non-map data the
// value is passed through untouched and globals are reachable only via the
// "globals" func/key is not injected — pass a map if you need globals.
//
// A missing view yields ErrTemplateNotFound. Auto-escaping is html/template's
// contextual default.
func (e *Engine) Render(w io.Writer, name string, data any) error {
	if e.dev {
		if err := e.reparse(); err != nil {
			return err
		}
	}

	e.mu.RLock()
	set := e.set
	layout := e.layouts[name]
	contentTree := e.content[name]
	e.mu.RUnlock()

	if set.Lookup(name) == nil {
		return fmt.Errorf("%w: %q", ErrTemplateNotFound, name)
	}

	data = e.mergeGlobals(data)

	// Execute against a clone, never the shared set: html/template refuses to
	// Clone a set after any of its templates has executed, so executing e.set
	// directly would poison every later layout render with "cannot Clone after
	// it has executed". Cloning per render keeps e.set a pristine master and
	// isolates a view's "content" redefinition from concurrent renders.
	clone, err := set.Clone()
	if err != nil {
		return fmt.Errorf("view: clone set for %q: %w", name, err)
	}

	// No layout: render the view directly off the clone.
	if layout == "" {
		if err := clone.ExecuteTemplate(w, name, data); err != nil {
			return fmt.Errorf("view: render %q: %w", name, err)
		}
		return nil
	}

	if clone.Lookup(layout) == nil {
		return fmt.Errorf("%w: layout %q for view %q", ErrTemplateNotFound, layout, name)
	}
	// Bind the view's captured content tree as the "content" block the layout
	// yields to. The tree was captured per view at parse time (from an explicit
	// {{define "content"}} or the inline body) and kept off the shared set so
	// two views sharing a layout never collide; the body registered under the
	// view name in the shared set is intentionally empty.
	if contentTree == nil {
		return fmt.Errorf("view: no content tree for %q", name)
	}
	if _, err := clone.AddParseTree("content", contentTree.Copy()); err != nil {
		return fmt.Errorf("view: bind content for %q: %w", name, err)
	}
	if err := clone.ExecuteTemplate(w, layout, data); err != nil {
		return fmt.Errorf("view: render %q via layout %q: %w", name, layout, err)
	}
	return nil
}

// RenderToString renders the view into a string. Convenient for mail bodies,
// tests, and anywhere an io.Writer is awkward.
func (e *Engine) RenderToString(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := e.Render(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// mergeGlobals layers e.globals beneath map data (per-render keys win) and
// exposes the full globals map under "globals". Non-map data is returned
// unchanged.
func (e *Engine) mergeGlobals(data any) any {
	if len(e.globals) == 0 {
		return data
	}
	m, ok := data.(map[string]any)
	if !ok {
		return data
	}
	merged := make(map[string]any, len(m)+len(e.globals)+1)
	maps.Copy(merged, e.globals)
	maps.Copy(merged, m)
	merged["globals"] = e.globals
	return merged
}

// filepath_ToSlash converts OS separators to "/". fs.FS always uses "/" but
// WalkDir paths are already slash-based; this keeps viewName robust if a
// caller passes an OS-native Root.
func filepath_ToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
