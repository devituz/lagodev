# lagodev

[![Go Reference](https://pkg.go.dev/badge/github.com/devituz/lagodev.svg)](https://pkg.go.dev/github.com/devituz/lagodev)
[![Go Report Card](https://goreportcard.com/badge/github.com/devituz/lagodev)](https://goreportcard.com/report/github.com/devituz/lagodev)
[![Tests](https://img.shields.io/badge/tests-passing-brightgreen.svg)](#testing)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Discussions](https://img.shields.io/badge/Q%26A-discussions-blueviolet?logo=github)](https://github.com/devituz/lagodev/discussions)

<!-- TODO: add a 10s GIF of `lago make:crud` generating a full CRUD API -->

**Laravel-grade developer experience for Go.** lagodev is a full backend
toolkit — migrations, an Eloquent-style ORM, factories, an Artisan-style
CLI, **and a native HTTP framework** — that you can drop into any Go
project. Use the whole stack, or pick the parts that fit.

> No router required. lagodev's `web` package is a complete Laravel-style
> HTTP framework. Already on Gin/Fiber/Echo? The ORM works identically
> there — see [docs/FRAMEWORK_INTEGRATION.md](docs/FRAMEWORK_INTEGRATION.md).

## Why lagodev?

One cohesive stack instead of gluing five libraries together — the ORM,
migrations, factories, scheduler, queue, mailer, and HTTP framework all
share the same DB connection, logger, and config object. The developer
experience reads like Laravel; the ORM is generics-based, so `Query[T]`
gives you compile-time-typed rows without code generation. The `web`
package is secure-by-default — CSRF, security headers, body limits,
rate-limit middleware, and validation are one line each, not a weekend
of integration.

---

## Highlights

- **Web framework** — `web.App`, `web.Router`, `web.Context`. Handlers
  return `(any, error)` and the framework turns them into JSON responses,
  including the 404 / 500 / 204 status mapping. Resource routes register
  in a single call: `app.Resource("posts", ctrl)`.
- **Secure by default** — `web.SecurityHeaders()` (CSP / X-Frame-Options /
  Referrer-Policy / Permissions-Policy / nosniff), `web.CSRF()` with
  double-submit cookie + constant-time compare, `web.RateLimit()` /
  `web.Throttle()` per IP, `web.BodyLimit()` against payload-DoS,
  `web.RequestID()` for tracing, hardened `web.CORS()` (rejects unsafe
  wildcard + credentials), `c.SetCookie()` with `HttpOnly`/`Secure`/
  `SameSite=Lax` defaults. See [SECURITY.md](SECURITY.md).
- **Validation** — `c.BindAndValidate(&dst)` with struct-tag rules
  (`required,min=N,max=N,email,url,oneof=...,uuid,...`). Failures auto-map
  to HTTP 422 with `{"errors": {field: msg}}`.
- **Schema builder** — `schema.Create("users", func(t *schema.Blueprint) { … })`
  compiles to PostgreSQL, MySQL, or SQLite. Extensible via
  `database.Grammar`.
- **Migration engine** — transactional `up`/`rollback`/`refresh`/`fresh`/
  `reset`/`status`/`step`, batching, advisory locking, checksums, SQL
  preview, dry-run mode.
- **ORM** — `orm.Model` base type, generic `orm.Query[T]`, hooks
  (`BeforeCreate`/`AfterUpdate`/…), soft deletes, casts, allocation-
  conscious reflection cache.
- **Query builder** — chainable `Where`/`OrWhere`/`WhereIn`/`OrderBy`/
  `Latest`/`Join`/`GroupBy`/`Having`/`LockForUpdate` with dialect-correct
  placeholders.
- **Relations** — `HasOne` / `HasMany` / `BelongsTo` / `BelongsToMany` /
  polymorphic `Morph*` with single-query eager-loading.
- **Factories & seeders** — `factory.New[T]` with states + faker,
  topologically-ordered seeders, transactional execution.
- **Artisan CLI** — `make:model`, `make:migration`, `make:seeder`,
  `make:factory`, `make:test`, `make:service`, `make:controller`,
  `make:crud`, `migrate*`, `db:*`, `env*`, `init`. Stub-based, with
  override hooks. Two interchangeable binaries: `lago` and `artisan`.
- **Driver-agnostic** — PostgreSQL (pgx), MySQL (mysql), SQLite
  (mattn/go-sqlite3) ship in-box. Add your own with a `Grammar`
  implementation.

## Quick tour — the full stack in 60 lines

```go
package main

import (
    "github.com/devituz/lagodev/database"
    _ "github.com/devituz/lagodev/drivers/sqlite"
    "github.com/devituz/lagodev/migrations"
    "github.com/devituz/lagodev/orm"
    "github.com/devituz/lagodev/schema"
    "github.com/devituz/lagodev/web"
)

type Post struct {
    orm.Model
    Title string
    Body  string
}

func init() {
    migrations.Register(migrations.Define("0001_posts",
        func(c *migrations.Context) error {
            return c.Schema(schema.Create("posts", func(t *schema.Blueprint) {
                t.ID(); t.String("title"); t.Text("body")
                t.Timestamps()
            }))
        },
        func(c *migrations.Context) error {
            return c.Schema(schema.DropIfExists("posts"))
        },
    ))
}

type PostController struct{ Conn *database.Connection }

func (p *PostController) Index(c *web.Context) (any, error) {
    var out []Post
    return out, orm.Query[Post](p.Conn).OrderBy("id", "desc").Get(c.Ctx(), &out)
}
func (p *PostController) Show(c *web.Context) (any, error) {
    return orm.Query[Post](p.Conn).Find(c.Ctx(), c.ParamUint("id"))
}
func (p *PostController) Store(c *web.Context) (any, error) {
    var x Post
    if err := c.Bind(&x); err != nil { return nil, err }
    return c.Created(x), orm.Save(c.Ctx(), p.Conn, &x)
}
func (p *PostController) Update(c *web.Context) (any, error)  { return nil, nil }
func (p *PostController) Destroy(c *web.Context) (any, error) { return nil, nil }

func main() {
    conn, _ := database.Global.Open("default", database.Config{
        Driver: "sqlite", DSN: "file::memory:?cache=shared",
    })

    app := web.New(
        web.WithDatabase(conn),
        web.WithMigrations(nil),
    )
    app.Resource("posts", &PostController{Conn: conn})
    app.MustRun(":8080")
}
```

```bash
go run .
curl -X POST http://localhost:8080/posts -d '{"title":"hi","body":"x"}'
curl http://localhost:8080/posts
```

## Installation

```bash
go get github.com/devituz/lagodev@latest
```

Blank-import the driver you want:

```go
_ "github.com/devituz/lagodev/drivers/postgres"  // pgx
_ "github.com/devituz/lagodev/drivers/mysql"     // go-sql-driver/mysql
_ "github.com/devituz/lagodev/drivers/sqlite"    // mattn/go-sqlite3
```

The CLI ships as two interchangeable binaries:

```bash
go install github.com/devituz/lagodev/cmd/lago@latest      # → lago
# or
go install github.com/devituz/lagodev/cmd/artisan@latest   # → artisan

lago init                                       # lago.json + config/ + routes/
lago env:init                                   # starter .env
lago make:model Post -mfsc \
    --fields="title:string,body:text,published:bool:default(false)"
lago migrate
lago migrate:fresh --seed
lago db:show
```

## Live reload (Laravel-style `php artisan serve --watch`)

lagodev ships a ready-to-use [`air`](https://github.com/air-verse/air)
config at the repo root. Install once:

```bash
go install github.com/air-verse/air@latest
```

Then from any lagodev project root:

```bash
air
```

Every save under the watched directories rebuilds the binary and
restarts the process — no manual `Ctrl+C` / `go run` cycle. The shipped
`.air.toml` targets `examples/secure` so contributors can iterate on
framework changes immediately; copy the file into your own project and
adjust the `cmd` / `bin` lines to point at your `main` package.

## Scaffolded layout — Laravel-style

```
myapp/
├── main.go                       — ~30 lines; calls routes.Register(app)
├── lago.json                     — directory layout for the generators
├── .env                          — DB_CONNECTION, DB_*, APP_*
├── config/
│   ├── app.go                    — AppConfig from env
│   └── database.go               — database.Config from env
├── routes/
│   └── api.go                    — routes.Register(app)
├── models/                       — embed orm.Model
├── migrations/                   — schema.Create("...")
├── factories/                    — faker-driven builders
├── seeders/                      — Seeder interface impls
├── services/                     — framework-agnostic CRUD
└── controllers/                  — *web.Context handlers, (any, error)
```

## Documentation

| Topic                                                       | File                                              |
|-------------------------------------------------------------|---------------------------------------------------|
| Getting started (10-minute intro)                           | [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) |
| **Web framework — routing, middleware, controllers**        | [docs/WEB.md](docs/WEB.md)                        |
| ORM cookbook — `Query[T]`, hooks, casts, transactions       | [docs/ORM.md](docs/ORM.md)                        |
| Migrations & schema DSL                                     | [docs/MIGRATIONS.md](docs/MIGRATIONS.md)          |
| Factories & seeders                                         | [docs/FACTORIES.md](docs/FACTORIES.md)            |
| `lago` / `artisan` CLI reference                            | [docs/CLI.md](docs/CLI.md)                        |
| `.env` and `lago.json` configuration                        | [docs/CONFIGURATION.md](docs/CONFIGURATION.md)    |
| Integration with Gin / Fiber / Echo / Chi / gRPC            | [docs/FRAMEWORK_INTEGRATION.md](docs/FRAMEWORK_INTEGRATION.md) |
| Architecture deep-dive                                      | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)      |
| Release notes                                               | [CHANGELOG.md](CHANGELOG.md)                      |

## Examples

| Folder                      | What it shows                                              |
|-----------------------------|------------------------------------------------------------|
| `examples/basic/`           | Connection → migrations → ORM → query, ~30 lines           |
| `examples/blog/`            | **Full showcase** — 3 models, FKs, services, factories, seeders |
| `examples/gin/`             | Real Gin v1 wrapping the same service layer                |
| `examples/fiber/`           | Fiber v2 — identical service, swapped HTTP layer           |
| `examples/echo/`            | Echo v4                                                    |
| `examples/chi/`             | Chi v5                                                     |
| `examples/microservice/`    | Queue worker with `LockForUpdate` + transactions           |

```bash
cd examples/blog && go mod tidy && go run .
curl http://localhost:8080/posts
```

## Testing

```bash
go test ./...                     # full suite (in-memory SQLite)
go test -race ./...
go test -bench=. -benchmem ./benchmarks
```

A test using the harness:

```go
import lagotest "github.com/devituz/lagodev/testing"

func TestSomething(t *testing.T) {
    conn, cleanup := lagotest.SQLite(t)
    defer cleanup()

    // Migrations already applied. ORM, factory, service — all work.
}
```

## Roadmap

| Area    | Planned                                                         |
|---------|-----------------------------------------------------------------|
| Web     | WebSockets, HTML templates, request validation objects          |
| Drivers | SQL Server, CockroachDB, TiDB grammars                          |
| ORM     | `Pluck` / `Chunk` / cursor pagination, JSON path queries        |
| CLI     | `make:request`, `make:command`, project scaffolding (`new`)     |
| Tooling | OpenTelemetry spans on every query, structured query logger sink |
| Schema  | Online ALTERs, native Postgres types (cidr, tsvector, hstore)   |

## Community

- [💬 Discussions](https://github.com/devituz/lagodev/discussions) — Q&A, ideas, show & tell
- [🐛 Issues](https://github.com/devituz/lagodev/issues) — bugs and feature requests
- [📖 Wiki](https://github.com/devituz/lagodev/wiki) — long-form guides
- [🚀 Showcase](SHOWCASE.md) — apps built with lagodev
- [Code of Conduct](CODE_OF_CONDUCT.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `make check` (or
`go test ./...` + `go vet ./...`) before opening a PR. New features
should ship with a test and a one-paragraph update to the relevant doc.

## License

MIT — see [LICENSE](LICENSE).

## ⭐ Star the project

If lagodev saved you time, a star helps other Go developers find it.

[![Star History Chart](https://api.star-history.com/svg?repos=devituz/lagodev&type=Date)](https://star-history.com/#devituz/lagodev&Date)
