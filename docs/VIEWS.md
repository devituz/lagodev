# Views — server-side HTML

`lagodev/view` is a thin, ergonomic layer over the standard library's
`html/template`, modelled on Laravel's Blade view layer. It loads
templates from any `fs.FS` — an `embed.FS` for a single-binary deploy, or
an on-disk directory with hot-reload during development — and adds the
conveniences a real app needs on top of `html/template`: named views,
layouts, partials/components, a batteries-included `FuncMap`, and
per-render data merged over shared globals.

> The package is deliberately a **leaf**: `Engine.Render` writes to any
> `io.Writer` (an `http.ResponseWriter`, a `bytes.Buffer` for mail, …) and
> never imports the `web` package. The `web` layer depends on `view`, not
> the other way around. Auto-escaping is inherited from `html/template`
> (contextual, on by default).

The entire surface is four functions/methods and one options struct:

```go
func New(fsys fs.FS, opts Options) (*Engine, error)

func (e *Engine) Render(w io.Writer, name string, data any) error
func (e *Engine) RenderToString(name string, data any) (string, error)
func (e *Engine) Names() []string

var ErrTemplateNotFound = errors.New("view: template not found")
```

## Quick start — render a view

Lay templates out under a root directory; address them by path **without**
the root prefix or the extension (so `templates/pages/home.html` is the
view `"pages/home"`).

```
templates/
├── layouts/
│   └── app.html
├── components/
│   └── button.html
└── pages/
    └── home.html
```

```go
package main

import (
    "embed"
    "os"

    "github.com/devituz/lagodev/view"
)

//go:embed templates
var tplFS embed.FS

func main() {
    eng, err := view.New(tplFS, view.Options{Root: "templates"})
    if err != nil {
        panic(err)
    }

    _ = eng.Render(os.Stdout, "pages/home", map[string]any{"Name": "Ada"})
}
```

`view.New` parses every template immediately, so syntax errors surface at
startup rather than on the first request. A `nil` `fs.FS` is an error.

`Options`:

| Field     | Type                | Effect                                                           |
|-----------|---------------------|------------------------------------------------------------------|
| `Root`    | `string`            | Sub-directory holding templates; trimmed from every view name    |
| `Ext`     | `string`            | File extension to load, with the dot. Empty → `".html"`          |
| `Dev`     | `bool`              | Hot-reload: re-read and re-parse the FS on every `Render`        |
| `Funcs`   | `template.FuncMap`  | Extra template funcs, merged **over** the built-ins              |
| `Globals` | `map[string]any`    | Values exposed to every render (see [Passing data](#passing-data)) |

## Layouts & inheritance

A layout is an ordinary template that yields to its child through a block:

```html
{{/* templates/layouts/app.html */}}
<html>
  <body>
    <main>{{block "content" .}}{{end}}</main>
  </body>
</html>
```

A view declares which layout wraps it with a `{{layout "name"}}` directive
**on its first line** (after optional whitespace), then redefines the
`content` block:

```html
{{layout "layouts/app"}}
{{define "content"}}
  <h1>Hello {{.Name}}</h1>
{{end}}
```

At render time the engine executes the **layout** with the view's
`content` definition bound in scope. The dot (`.`) inside `content` is the
same render data passed to `Render`.

### Inline content (no `define`)

The `{{define "content"}}` wrapper is the documented style, but it is
optional. If a view declares a layout and writes its body inline (without
a `define`), the whole body becomes the `content` block:

```html
{{layout "layouts/app"}}
<h1>Hello {{.Name}}</h1>
```

renders identically to the `define` version above. Either way, a view
**without** a `{{layout}}` directive is rendered standalone — the file's
body is executed directly.

Two views may share the same layout safely: each view's `content` tree is
captured per view at parse time, so same-named `define`s in different views
never collide.

A view referencing a layout that does not exist yields
`ErrTemplateNotFound` (wrapping the layout name) at render time.

## Partials & components

Every loaded template — layout, page, or fragment — is addressable by its
view name, so partials and components are just `{{template "name" .}}`. A
fragment is any template file that does **not** declare a layout:

```html
{{/* templates/partials/nav.html → "partials/nav" */}}
<nav>
  <a href="{{url "/"}}">Home</a>
  <a href="{{url "/about"}}">About</a>
</nav>
```

```html
{{/* templates/components/button.html → "components/button" */}}
<button class="{{.kind}}">{{.label}}</button>
```

Pull them in from a page (or a layout). Forward the current data with `.`,
or pass several named values into a component with the built-in `dict`:

```html
{{layout "layouts/app"}}
{{define "content"}}
  {{template "partials/nav" .}}

  <h1>{{upper .Name}}</h1>

  {{template "components/button" (dict "label" "Save" "kind" "primary")}}
{{end}}
```

`dict` builds a `map[string]any` from alternating key/value pairs; an odd
number of arguments or a non-string key is reported by the template
executor.

## Passing data

The third argument to `Render` is the per-render payload, exposed as `.`
inside the template:

```go
eng.Render(w, "pages/home", map[string]any{
    "Name": "Ada",
    "Role": "admin",
})
```

You can pass a struct just as well — `html/template` resolves
`.FieldName` against it:

```go
type homeVM struct{ Name, Role string }
eng.Render(w, "pages/home", homeVM{Name: "Ada", Role: "admin"})
```

### Globals

`Options.Globals` are values available to every render. They apply only
when the per-render `data` is a `map[string]any`: the engine layers the
globals **beneath** the map (per-render keys win on conflict) and also
exposes the full globals map under the `"globals"` key.

```go
eng, _ := view.New(tplFS, view.Options{
    Root:    "templates",
    Globals: map[string]any{"AppName": "Acme", "Year": 2026},
})

// data wins where keys overlap; AppName/Year come from globals.
eng.Render(w, "pages/home", map[string]any{"Name": "Ada"})
```

```html
<title>{{.AppName}}</title>          {{/* from globals */}}
<footer>© {{.globals.Year}}</footer> {{/* via the "globals" key */}}
```

If `data` is **not** a map (a struct, say), globals are not merged — pass a
map when you need them.

### Built-in template functions

Every engine ships with this `FuncMap`; `Options.Funcs` is merged **over**
it, so you can override any of them.

| Category   | Functions                                                                 |
|------------|---------------------------------------------------------------------------|
| Data       | `dict`, `default`                                                         |
| Strings    | `upper`, `lower`, `title`, `trim`, `replace`, `contains`, `split`, `join`, `truncate` |
| Numbers    | `add`, `sub`, `mul`                                                       |
| Dates      | `now`, `date` (Go layout), `formatDate` (`Jan 2, 2006`)                   |
| Safe output| `safeHTML`, `safeAttr`, `safeURL`, `safeJS`, `safeCSS`                    |
| Injectable | `route`, `asset`, `url`, `csrfField`                                      |

Usage notes:

```html
{{ .Title | default "Untitled" }}     {{/* fallback on zero/empty */}}
{{ truncate 80 .Body }}               {{/* at most 80 runes, then "…" */}}
{{ date "2006-01-02" .CreatedAt }}    {{/* Go reference layout */}}
{{ formatDate .CreatedAt }}           {{/* Jan 2, 2006 */}}
```

The `route`/`asset`/`url`/`csrfField` helpers ship as no-op-ish stubs that
echo their input (so templates parse and render before an app wires real
resolvers). Inject the real ones via `Options.Funcs` — this is exactly how
`view` stays a leaf and never imports `web`:

```go
eng, _ := view.New(tplFS, view.Options{
    Root: "templates",
    Funcs: template.FuncMap{
        "route":     router.URLFor,                 // func(name string, args ...any) string
        "asset":     assets.URL,                     // func(path string) string
        "csrfField": func() template.HTML { ... },   // hidden <input>
    },
})
```

## Autoescaping & security

Output is escaped by `html/template`'s **contextual** auto-escaping —
HTML, attributes, URLs, JS, and CSS each get the right escaping for where
the value lands. This is on by default and you get it for free:

```html
<h1>{{.Title}}</h1>          {{/* HTML-escaped */}}
<a href="{{.Link}}">go</a>   {{/* URL-escaped in the href context */}}
<script>var x = {{.Data}};</script>  {{/* JS-escaped */}}
```

To emit trusted markup verbatim, use the `safe*` escape hatches — but only
on values you fully control, never on user input:

```html
{{ safeHTML .RenderedMarkdown }}   {{/* bypasses HTML escaping */}}
{{ safeURL  .SignedDownloadURL }}
```

A custom func returning a typed `template.HTML` (etc.) is likewise treated
as trusted; sanitize before you reach for either mechanism.

## Integration with web handlers

`view` does not import `web`, so wire it in yourself. Construct the engine
once at startup and stash it where your handlers can reach it (a service,
a closure, or `web.Context` via `c.Set`/`c.Get`). Because `Render` writes
to any `io.Writer`, hand it the response writer directly:

```go
import (
    "embed"
    "net/http"

    "github.com/devituz/lagodev/view"
    "github.com/devituz/lagodev/web"
)

//go:embed templates
var tplFS embed.FS

func main() {
    eng, err := view.New(tplFS, view.Options{Root: "templates"})
    if err != nil {
        log.Fatal(err)
    }

    app := web.New()
    app.Get("/", func(c *web.Context) (any, error) {
        c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
        if err := eng.Render(c.Writer, "pages/home", map[string]any{
            "Name": "Ada",
        }); err != nil {
            return nil, err   // 500 via the framework's error contract
        }
        return nil, nil       // body already written; nothing more to serialize
    })
    app.MustRun(":8080")
}
```

Returning `(nil, nil)` after writing the body yourself tells the framework
there is nothing left to serialize (see [WEB.md](WEB.md) for the
`(any, error)` contract). If you prefer to build the body in memory first
— to set headers based on the result, or to handle the render error before
any bytes go out — use `RenderToString`:

```go
html, err := eng.RenderToString("pages/home", data)
if err != nil {
    return nil, err
}
c.String(http.StatusOK, html) // c.String sets text/plain; adjust the header if needed
```

### Rendering mail bodies

The same `RenderToString` (or `Render` into a `bytes.Buffer`) produces an
HTML mail body without `view` importing `mail`:

```go
body, err := eng.RenderToString("emails/welcome", data)
if err != nil {
    return err
}
msg := mail.NewMessage().To(addr).HTML(body).Build()
```

## Production notes

- **Embed your templates.** Build with `//go:embed templates` and an
  `embed.FS` so the binary is self-contained — no template files to ship
  or path to misconfigure on the server.

- **Parse once.** With `Dev: false` (the default) the template set is
  parsed a single time at `New`, and `Render` reads it lock-free against
  an immutable set. Each `Render` executes against a fresh `Clone` of that
  set, so concurrent renders never interfere and the master stays pristine.
  `Engine` is safe for concurrent use.

- **Dev mode is for development only.** `Dev: true` re-reads and re-parses
  the whole FS on **every** `Render` (under a mutex) so on-disk edits show
  up without a restart. This needs an FS that reflects disk changes —
  `os.DirFS("templates")` does; `embed.FS` does **not** (it's frozen at
  build time). A common pattern: `embed.FS` in prod, `os.DirFS` + `Dev:
  true` in dev, selected by config.

  ```go
  var fsys fs.FS = tplFS
  opts := view.Options{Root: "templates"}
  if cfg.Dev {
      fsys = os.DirFS(".")
      opts.Dev = true
  }
  eng, _ := view.New(fsys, opts)
  ```

- **Inspect the loaded set** with `Names()` — the sorted list of every
  addressable view — when debugging a missing template or wiring up tests:

  ```go
  for _, n := range eng.Names() {
      log.Println("view:", n)
  }
  ```

- **Handle `ErrTemplateNotFound`.** `Render`/`RenderToString` wrap it for
  both a missing view and a missing layout; match with `errors.Is`:

  ```go
  if errors.Is(err, view.ErrTemplateNotFound) {
      c.NotFound("page not found")
  }
  ```
```
