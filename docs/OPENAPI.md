# OpenAPI 3.1 + typed client

The `openapi` package generates a valid OpenAPI 3.1 document for a lagodev
app using only the standard library and reflection — no codegen, no
struct registration, no annotations file. Describe operations with Go
types, render the spec, serve a Swagger-UI page, and (via
`lago gen:client`) generate a fully typed Go HTTP client from the result.

There are three layers:

| Layer            | What it does                                              | Entry points                         |
|------------------|-----------------------------------------------------------|--------------------------------------|
| Struct → Schema  | Reflect a Go type into JSON Schema, honouring `json`/`validate` tags | `SchemaOf[T]`, `SchemaFor`     |
| Spec builder     | Declare operations; bodies become reusable `$ref` components | `New`, `Spec.Operation`, `FromApp` |
| Serve            | Render JSON, expose handlers, ship a docs page            | `Spec.JSON`, `Spec.Handler`, `Spec.DocsHandler` |

## Quick start — a spec from your routes

`FromApp` harvests every route registered on a `*web.App` and returns a
serveable `*Spec` with one bare operation per route (method, path, path
params, a default `200`):

```go
import (
    "github.com/devituz/lagodev/openapi"
    "github.com/devituz/lagodev/web"
)

func Register(app *web.App) {
    app.Group("/api/v1", func(g *web.Router) {
        g.Resource("posts", controllers.NewPostController(app.DB()))
    })

    spec := openapi.FromApp(app, "Blog API", "1.0.0")
    spec.AddServer("https://api.example.com", "production")

    // Serve the document through a web.Handler — Spec.Map() returns the
    // document as a plain map the framework renders as JSON.
    app.Get("/openapi.json", func(c *web.Context) (any, error) {
        return spec.Map()
    })
}
```

The `Spec.Handler()` / `Spec.DocsHandler()` helpers return `http.Handler`
values — mount those on a standalone `http.ServeMux` (see
[Serving the spec](#serving-the-spec)). On a `*web.App`, return
`spec.Map()` from a `web.Handler` as above, or render `spec.JSON()` to a
file at build time.

`FromApp` reads route metadata only — `web` does not carry request and
response types — so each operation starts as a skeleton. Refine
individual operations afterwards (a repeated method+path **replaces** the
prior operation). To harvest without building a full spec, use
`openapi.Harvest(app) []RouteInfo` (or `HarvestRouter([]web.Route)` for a
sub-group); each `RouteInfo` carries `Method`, `Path` (normalized to brace
form) and `PathParams`.

## Describing operations

`New(title, version)` returns an empty `*Spec`; `Operation` declares one
operation. Request and response bodies are described by Go types and
registered once under `components/schemas`, referenced elsewhere by
`$ref`.

```go
type CreateUser struct {
    Name  string `json:"name"  validate:"required,max=64"`
    Email string `json:"email" validate:"required,email"`
    Role  string `json:"role"  validate:"required,in=admin|user"`
}
type User struct {
    ID    int    `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}

spec := openapi.New("Users API", "1.0.0")
spec.AddServer("https://api.example.com", "production")

spec.Operation(http.MethodPost, "/users", openapi.OperationConfig{
    Summary:     "Create a user",
    Tags:        []string{"users"},
    OperationID: "createUser",
    RequestBody: openapi.BodyOf[CreateUser](),
    Responses: map[int]openapi.Response{
        201: openapi.JSONResponse[User]("created"),
        422: openapi.TextResponse("validation failed"),
    },
})

spec.Operation(http.MethodGet, "/users/{id}", openapi.OperationConfig{
    Summary:     "List or fetch a user",
    Tags:        []string{"users"},
    OperationID: "getUser",
    Query: []openapi.Param{
        {Name: "expand", Description: "comma-separated relations"},
    },
    Responses: map[int]openapi.Response{
        200: openapi.JSONResponse[User]("the user"),
        404: openapi.TextResponse("not found"),
    },
})
```

`OperationConfig` fields:

| Field         | Purpose                                                          |
|---------------|------------------------------------------------------------------|
| `Summary`     | Short operation title                                            |
| `Description` | Longer prose                                                     |
| `Tags`        | Grouping labels (drives `Spec.Tags()` and Swagger-UI sections)   |
| `OperationID` | Stable id — becomes the **Go method name** in the generated client |
| `PathParams`  | `[]Param` docs for path placeholders                            |
| `Query`       | `[]Param` query parameters                                      |
| `RequestBody` | `*Body` (see below)                                            |
| `Responses`   | `map[int]Response` keyed by HTTP status                        |

Path parameters are auto-derived from `{id}` / `:id` segments and merged
with explicit `PathParams` docs — any placeholder you do not document is
added as a required `string` param. Both brace and colon forms are
accepted and normalized to brace form. An empty `Responses` map defaults
to a single `200 OK` so the operation stays valid.

### Bodies and responses

| Constructor                     | Result                                          |
|---------------------------------|--------------------------------------------------|
| `openapi.BodyOf[T]()`           | Request body schema from type `T` (`application/json`, required) |
| `openapi.RawBody(schema)`       | Request body from an explicit `*Schema`         |
| `openapi.JSONResponse[T](desc)` | JSON response whose body is `T`                 |
| `openapi.ListResponse[T](desc)` | JSON response whose body is `[]T`               |
| `openapi.TextResponse(desc)`    | Bodyless response (e.g. `204`, error statuses)  |

`Body` and `Response` are plain structs — set `MediaType` to override the
default `application/json`, or `Required`/`Description` as needed.

## Schemas from Go types

`SchemaOf[T]()` (or `SchemaFor(reflect.Type)`) reflects a type into JSON
Schema. It honours `json` tags (names, `omitempty`, `-`), recurses
through nested structs, slices, maps and pointers, flattens embedded
structs the way `encoding/json` promotes their fields, and emits
`time.Time` as `{type: string, format: date-time}`. Pointer and
`omitempty` fields become nullable using the OpenAPI 3.1 type-union idiom
(`["string", "null"]`).

`validate` tags map onto schema keywords:

| Validate rule                  | Schema keyword                          |
|--------------------------------|------------------------------------------|
| `required`                     | adds the field to the object's `required` |
| `email` / `url` / `uuid` / `datetime` | `format`                          |
| `alpha` / `alphanumeric` / `regex=…` | `pattern`                          |
| `min` / `max` / `len`          | `minLength`/`maxLength` (strings), `minItems`/`maxItems` (arrays), or `minimum`/`maximum` (numbers) |
| `gte` / `lte`                  | `minimum` / `maximum`                    |
| `gt` / `lt`                    | `minimum`/`maximum` + `exclusive…`       |
| `in=admin\|user`               | `enum` (numeric when the field is numeric) |

Whether `min`/`max` constrains length or magnitude follows the field's
type, matching the `validation` package's own behaviour. A `required`
field that is also `omitempty` is **not** added to `required` (it can be
absent on the wire).

When operations register types through a `*Spec`, named structs become
`$ref` components automatically — `SchemaOf`/`SchemaFor` build **inline**
schemas only. Use `spec.Registry()` to pre-register or inspect:

```go
name := openapi.Register[User](spec.Registry()) // "User"
all  := spec.Registry().Schemas()               // map[string]*openapi.Schema
```

## Serving the spec

```go
spec := openapi.New("Users API", "1.0.0")
// ... declare operations ...

mux := http.NewServeMux()
mux.Handle("/openapi.json", spec.Handler())
mux.Handle("/docs", spec.DocsHandler("/openapi.json"))
```

- `Spec.Handler()` renders the document once, caches the bytes, and serves
  them as `application/json`. Only `GET`/`HEAD` are answered (`405`
  otherwise).
- `Spec.DocsHandler(specURL)` serves a self-contained Swagger-UI page
  pointing at `specURL` (defaults to `/openapi.json` when empty). Assets
  load from a public CDN; for offline use, host them yourself and adapt
  `openapi.SwaggerHTML(title, specURL)`.

On a `*web.App` (which routes through `web.Handler`, not `http.Handler`),
serve the document by returning `spec.Map()` from a handler instead, as
shown in the [quick start](#quick-start--a-spec-from-your-routes).

Other render outputs:

```go
b, err := spec.JSON()   // indented []byte, valid OpenAPI 3.1
m, err := spec.Map()    // map[string]any (JSON round-trip) for post-processing
tags   := spec.Tags()   // distinct operation tags, sorted
```

Write the document to disk to feed the client generator:

```go
b, _ := spec.JSON()
_ = os.WriteFile("openapi.json", b, 0o644)
```

## Generating a typed Go client

`lago gen:client` reads an OpenAPI 3.x JSON document (anything the
`openapi` package emits, or any compliant spec) and writes a single
`client.go`: a `Client` struct, one method per operation, and a Go struct
for every component schema.

```bash
lago gen:client --spec openapi.json --dir client --package client
```

| Flag         | Default                       | Purpose                              |
|--------------|-------------------------------|--------------------------------------|
| `--spec`     | — (**required**)              | Path to the OpenAPI JSON document    |
| `--dir`      | `client`                      | Output directory (`<dir>/client.go`) |
| `--package`  | derived from `--dir`          | Package name                         |
| `--base-url` | first `server` URL in the spec | Baked-in `DefaultBaseURL`           |
| `--force`    | `false`                       | Overwrite an existing `client.go`    |

`artisan gen:client …` is the interchangeable alias.

### What gets generated

- `DefaultBaseURL` constant from the spec's first server (or `--base-url`).
- A `Client{BaseURL string; HTTPClient *http.Client}` with
  `New(baseURL string) *Client` (`http.DefaultClient` by default).
- An `APIError{StatusCode int; Body string}` returned for every non-2xx
  response.
- One method per operation. The method name comes from `OperationID`
  (sanitized + PascalCased); without one it falls back to verb + path
  segments (`GetUsersId`). Each method takes `ctx context.Context` first,
  then typed path params, query params, and a typed `body` when the
  operation has a request body. The return is `error`, or
  `(<RespType>, error)` when the lowest 2xx response carries a JSON body.
- A struct per `components/schemas` entry, fields tagged with the JSON
  wire name (`,omitempty` unless the schema marks the property required).

For the two operations declared above, the generated client exposes:

```go
client := client.New("") // uses DefaultBaseURL

u, err := client.CreateUser(ctx, client.CreateUser{
    Name:  "Ada",
    Email: "ada@example.com",
    Role:  "admin",
})

u, err := client.GetUser(ctx, "42", "profile") // id, expand
```

Bodies are JSON-encoded with `Content-Type: application/json`; 2xx JSON
responses are decoded into the typed return; everything else comes back as
`*APIError`. The file carries a `// Code generated … DO NOT EDIT.` header —
regenerate, don't hand-edit.

### End-to-end

```bash
# 1. emit the spec from your running app (or a small main that builds it)
go run ./cmd/genspec > openapi.json

# 2. generate the client into ./client
lago gen:client --spec openapi.json --dir client --package client --force

# 3. import and call it
go build ./client
```

## Production notes

- **Spec hosting.** Render once at startup — `Spec.Handler()` already
  caches the JSON, so it is cheap to keep mounted. If you don't want the
  doc public, gate `/openapi.json` and `/docs` behind the same auth
  middleware as the rest of the API, or only register them when an env
  flag is set.
- **Swagger-UI assets.** `DocsHandler` pulls Swagger-UI from jsDelivr. For
  air-gapped or strict-CSP deployments, vendor the assets and build your
  own page from `SwaggerHTML` as a template.
- **`OperationID` is contract.** It becomes the client method name. Set it
  deliberately and keep it stable across versions — renaming it is a
  breaking change for every generated client downstream.
- **Spec is the source of truth for codegen.** `gen:client` consumes a
  static JSON file, so wire spec generation into CI: build the spec, write
  `openapi.json`, run `gen:client --force`, and fail the build on a dirty
  diff to catch drift between the API and its published client.
- **Reflection limits.** Schemas come from struct tags only. Computed
  fields, custom `MarshalJSON`, and `interface{}` values reflect as their
  literal Go shape (open `{}` for interfaces) — model wire DTOs explicitly
  rather than reusing ORM models with hidden columns.

## See also

- **[WEB.md](WEB.md)** — the `web` framework whose routes `Harvest`/`FromApp` read.
- **[CLI.md](CLI.md)** — full `lago`/`artisan` command reference.
- **[ORM.md](ORM.md)** — models that back your handlers.
</content>
</invoke>
