<div align="center">

# lagodev

### The batteries-included web framework for Go.

[![Go Reference](https://pkg.go.dev/badge/github.com/devituz/lagodev.svg)](https://pkg.go.dev/github.com/devituz/lagodev)
[![Go Report Card](https://goreportcard.com/badge/github.com/devituz/lagodev)](https://goreportcard.com/report/github.com/devituz/lagodev)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![Tests](https://img.shields.io/badge/tests-passing-brightgreen.svg)](#testing)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Discussions](https://img.shields.io/badge/Q%26A-discussions-blueviolet?logo=github)](https://github.com/devituz/lagodev/discussions)

**ORM · Migrations · Router · Auth · Authz · Admin · GraphQL · Realtime · Queue · Scheduler · Mail · Cache · OpenAPI · CLI — one cohesive stack.**

[Quick start](#60-second-quick-start) · [Why lagodev](#why-lagodev) · [Features](#feature-matrix) · [Docs](#documentation) · [vs Laravel / Django / NestJS / Express](#at-a-glance-vs-the-rest)

</div>

---

lagodev is a **full-stack web framework for Go** — not a starter template, not a
loose bag of libraries. It gives you everything you need to ship a production
backend: a generics-typed ORM with relations and migrations, an HTTP router and
web layer, first-class authentication and authorization, an auto-generated admin
panel, GraphQL, WebSocket realtime, a queue with a dashboard, a scheduler,
events, mail, cache, OpenAPI 3.1 generation, and a Laravel-style `lago`
CLI that scaffolds it all. Every subsystem shares the same connection, config,
container, and logger — so the parts fit together instead of fighting each other.

If you've used **Laravel**, **Django**, **NestJS**, or **Express**, lagodev will
feel immediately familiar — with Go's static typing, single-binary deploys, and
goroutine concurrency underneath. Take the whole framework, or drop individual
packages into an existing Gin / gRPC / Echo app.

## Why lagodev

- **Complete, not minimal.** Auth, authz, admin, realtime, queue, scheduler,
  mail, cache, search, observability — in the box, sharing one runtime. No
  weekend spent wiring five third-party libraries into a coherent stack.
- **Typed end to end.** `orm.Query[T]`, `collection.Collection[T]`, and a
  generics-based DI container give you compile-time-checked data access with
  **zero codegen**. The OpenAPI generator and typed client codegen close the
  loop out to your API consumers.
- **Secure by default.** CSRF, security headers, body limits, per-IP rate
  limiting, hardened CORS, and `HttpOnly`/`Secure`/`SameSite` cookies are one
  line each — not an afterthought. See [SECURITY.md](SECURITY.md).
- **Productive like Laravel.** The `lago` CLI scaffolds models, migrations,
  factories, seeders, services, controllers, policies, resources, and full CRUD
  in a single command. `gen:client` emits a typed client from your routes.
- **Native to Go.** Single static binary, no runtime, no VM. Goroutine-backed
  realtime and queue workers, `context.Context` threaded throughout, and
  allocation-conscious reflection caches.
- **Use the whole stack — or just a piece.** The ORM works identically under
  Gin, Fiber, Echo, Chi, or gRPC. See
  [docs/FRAMEWORK_INTEGRATION.md](docs/FRAMEWORK_INTEGRATION.md).

## Feature matrix

| Domain | What ships in the box |
|---|---|
| **Data / ORM** | Generic `Query[T]`, `orm.Model`, lifecycle hooks, soft deletes, casts, [relations](docs/ORM.md) (`HasOne`/`HasMany`/`BelongsTo`/`BelongsToMany`/polymorphic) with eager loading, chainable query builder |
| **Migrations & schema** | Transactional `up`/`rollback`/`refresh`/`fresh`/`reset`/`status`/`step`, advisory locks, checksums, dry-run; dialect-correct schema DSL for **SQLite / MySQL / Postgres** ([MIGRATIONS.md](docs/MIGRATIONS.md)) |
| **HTTP / Web** | Router, middleware, typed requests, JSON responses with status mapping, resource routes, [validation](docs/WEB.md) (struct-tag rules → 422), [server-rendered views](docs/VIEWS.md) |
| **Auth** | Guards, sessions, tokens, JWT, OAuth (PKCE), account + password-reset flows ([AUTHENTICATION.md](docs/AUTHENTICATION.md)) |
| **Authz** | Gates and policies, least-privilege checks ([AUTHORIZATION.md](docs/AUTHORIZATION.md)) |
| **Admin** | Auto-CRUD admin panel built on generics ([ADMIN.md](docs/ADMIN.md)) |
| **APIs** | [API resources / serializers](docs/API_RESOURCES.md), [OpenAPI 3.1 generation + typed client codegen](docs/OPENAPI.md), GraphQL ([GRAPHQL.md](docs/GRAPHQL.md)) |
| **Realtime** | WebSocket hub with presence, [broadcasting](docs/REALTIME.md) |
| **Background work** | [Queue + jobs + dashboard](docs/QUEUE.md), scheduler, events, notifications |
| **Messaging** | Mail, notifications |
| **Infrastructure** | Cache, session, [DI container](docs/ARCHITECTURE.md), config, i18n, carbon dates, filesystem (local/S3), httpclient, crypt |
| **Reliability** | [Resilience](docs/RESILIENCE.md) (circuit breaker, retry), [observability / OpenTelemetry](docs/OBSERVABILITY.md), [Telescope debug dashboard](docs/TELESCOPE.md) |
| **Search** | Full-text [search](docs/SEARCH.md) |
| **DX** | `lago` CLI (`make:*` generators, `migrate*`, `db:*`, `gen:client`), [factories](docs/FACTORIES.md) + testing harness, generic [collections](docs/ORM.md), live reload |
| **Integrations** | Adapters for **gin / grpc / websocket**, **redis** driver, drop-in ORM for existing apps |

## 60-second quick start

```bash
mkdir myapp && cd myapp
go mod init github.com/you/myapp

go get github.com/devituz/lagodev@latest
go install github.com/devituz/lagodev/cmd/lago@latest   # `lago` CLI (alias: artisan)

lago init        # writes lago.json, config/, routes/
lago env:init    # writes .env with documented defaults

# Generate model + migration + factory + seeder + service + controller in one shot
lago make:model Post -mfsc \
    --fields="title:string,body:text,published:bool:default(false)"
```

Wire the route in `routes/api.go`:

```go
package routes

import (
    "github.com/devituz/lagodev/web"
    "github.com/you/myapp/controllers"
)

func Register(app *web.App) {
    app.Get("/health", func(c *web.Context) (any, error) {
        return map[string]string{"status": "ok"}, nil
    })

    app.Group("/api/v1", func(g *web.Router) {
        g.Resource("posts", controllers.NewPostController(app.DB()))
    })
}
```

Bootstrap in `main.go`:

```go
package main

import (
    "log"

    "github.com/devituz/lagodev/config"
    "github.com/devituz/lagodev/database"
    _ "github.com/devituz/lagodev/drivers/sqlite"
    "github.com/devituz/lagodev/web"

    _ "github.com/you/myapp/migrations" // registers migrations via init()

    appcfg "github.com/you/myapp/config"
    "github.com/you/myapp/routes"
)

func main() {
    _ = config.LoadEnv()

    mgr := database.NewManager()
    conn, err := mgr.Open("default", appcfg.Database())
    if err != nil {
        log.Fatal(err)
    }

    app := web.New(
        web.WithDatabase(conn),
        web.WithManager(mgr),
        web.WithMigrations(nil),
        web.WithAddr(appcfg.App().Addr),
    )
    routes.Register(app)
    app.MustRun()
}
```

```bash
lago migrate          # apply migrations
go run .              # serves on :8080, prints every registered route

curl -X POST http://localhost:8080/api/v1/posts \
    -H "Content-Type: application/json" \
    -d '{"Title":"Hello","Body":"World"}'   # → 201 Created
curl http://localhost:8080/api/v1/posts     # → 200 OK
```

`Resource()` registers the six CRUD routes (`Index`/`Show`/`Store`/`Update`/
`Destroy`) in a single call. Handlers return `(any, error)` and the framework
maps them to JSON with the correct `200`/`201`/`204`/`404`/`422`/`500` status.

> Full walkthrough: **[docs/GETTING_STARTED.md](docs/GETTING_STARTED.md)**.

### CLI at a glance

```bash
lago make:model        lago make:migration    lago make:factory
lago make:seeder       lago make:service      lago make:controller
lago make:resource     lago make:policy       lago make:test
lago make:scheme       lago make:crud         # full CRUD stack in one command

lago migrate           lago migrate:fresh --seed   lago migrate:rollback
lago db:show           lago env:init

lago gen:client        # typed API client from your routes / OpenAPI spec
```

Two interchangeable binaries — `lago` and `artisan`. See [docs/CLI.md](docs/CLI.md).

## Installation

```bash
go get github.com/devituz/lagodev@latest
go install github.com/devituz/lagodev/cmd/lago@latest   # or .../cmd/artisan@latest
```

Blank-import the database driver you need:

```go
_ "github.com/devituz/lagodev/drivers/postgres" // pgx
_ "github.com/devituz/lagodev/drivers/mysql"    // go-sql-driver/mysql
_ "github.com/devituz/lagodev/drivers/sqlite"   // mattn/go-sqlite3
```

### Live reload

lagodev ships a ready-to-use [`air`](https://github.com/air-verse/air) config at
the repo root. Install once (`go install github.com/air-verse/air@latest`), then
run `air` from your project root — every save rebuilds and restarts the binary.

## Documentation

| Area | Guide |
|---|---|
| Getting started | [GETTING_STARTED.md](docs/GETTING_STARTED.md) |
| Web — routing, middleware, controllers | [WEB.md](docs/WEB.md) |
| Server-rendered views | [VIEWS.md](docs/VIEWS.md) |
| ORM — `Query[T]`, hooks, relations, casts | [ORM.md](docs/ORM.md) |
| Migrations & schema DSL | [MIGRATIONS.md](docs/MIGRATIONS.md) |
| Authentication (guard/session/token/JWT/OAuth) | [AUTHENTICATION.md](docs/AUTHENTICATION.md) |
| Authorization (gates & policies) | [AUTHORIZATION.md](docs/AUTHORIZATION.md) |
| Admin panel (auto-CRUD) | [ADMIN.md](docs/ADMIN.md) |
| GraphQL | [GRAPHQL.md](docs/GRAPHQL.md) |
| Realtime — WebSocket, presence, broadcasting | [REALTIME.md](docs/REALTIME.md) |
| Queue, jobs & dashboard | [QUEUE.md](docs/QUEUE.md) |
| API resources / serializers | [API_RESOURCES.md](docs/API_RESOURCES.md) |
| OpenAPI 3.1 + typed client codegen | [OPENAPI.md](docs/OPENAPI.md) |
| Full-text search | [SEARCH.md](docs/SEARCH.md) |
| Resilience (circuit breaker / retry) | [RESILIENCE.md](docs/RESILIENCE.md) |
| Observability (OpenTelemetry) | [OBSERVABILITY.md](docs/OBSERVABILITY.md) |
| Telescope debug dashboard | [TELESCOPE.md](docs/TELESCOPE.md) |
| Factories & seeders | [FACTORIES.md](docs/FACTORIES.md) |
| `lago` / `artisan` CLI reference | [CLI.md](docs/CLI.md) |
| Configuration — `.env` & `lago.json` | [CONFIGURATION.md](docs/CONFIGURATION.md) |
| Gin / Fiber / Echo / Chi / gRPC integration | [FRAMEWORK_INTEGRATION.md](docs/FRAMEWORK_INTEGRATION.md) |
| Architecture deep-dive | [ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Benchmarks | [BENCHMARKS.md](docs/BENCHMARKS.md) |
| Release notes | [CHANGELOG.md](CHANGELOG.md) |

## At a glance vs the rest

| | **lagodev** (Go) | Laravel (PHP) | Django (Python) | NestJS (Node) | Express (Node) |
|---|---|---|---|---|---|
| Typed ORM, no codegen | ✅ generics | runtime | runtime | via TS+ORM | — |
| Migrations in core | ✅ | ✅ | ✅ | add-on | — |
| Auth + authz in core | ✅ | ✅ | ✅ | add-on | — |
| Auto admin panel | ✅ | add-on | ✅ | add-on | — |
| GraphQL in core | ✅ | add-on | add-on | ✅ | add-on |
| Realtime (WS + presence) | ✅ | add-on | add-on (Channels) | gateway | add-on |
| Queue + scheduler in core | ✅ | ✅ | add-on (Celery) | add-on | — |
| OpenAPI + typed client gen | ✅ | add-on | add-on | ✅ | add-on |
| Scaffolding CLI | ✅ `lago` | ✅ artisan | ✅ manage.py | ✅ nest | — |
| Single static binary | ✅ | — | — | — | — |

lagodev brings the full "framework with everything" model of Laravel/Django to
Go, with the typed DX of NestJS and the deploy story of a single Go binary. See
**[COMPARISON.md](docs/COMPARISON.md)** for the detailed breakdown and
**[BENCHMARKS.md](docs/BENCHMARKS.md)** for performance numbers.

## Examples

| Folder | What it shows |
|---|---|
| `examples/basic/` | Connection → migrations → ORM → query, ~30 lines |
| `examples/blog/` | Full showcase — 3 models, FKs, services, factories, seeders |
| `examples/gin/` · `fiber/` · `echo/` · `chi/` | Same service layer, swapped HTTP framework |
| `examples/microservice/` | Queue worker with `LockForUpdate` + transactions |

```bash
cd examples/blog && go mod tidy && go run .
curl http://localhost:8080/posts
```

## Testing

```bash
go test ./...                          # full suite (in-memory SQLite)
go test -race ./...
go test -bench=. -benchmem ./benchmarks
```

```go
import lagotest "github.com/devituz/lagodev/testing"

func TestSomething(t *testing.T) {
    conn, cleanup := lagotest.SQLite(t) // migrations already applied
    defer cleanup()
    // ORM, factory, service — all wired and ready.
}
```

## Requirements

- **Go 1.25+** (generics-heavy APIs throughout).
- Database: SQLite, MySQL, or PostgreSQL. Redis driver for cache/queue/realtime
  fan-out (optional).

## Community

- [💬 Discussions](https://github.com/devituz/lagodev/discussions) — Q&A, ideas, show & tell
- [🐛 Issues](https://github.com/devituz/lagodev/issues) — bugs and feature requests
- [📖 Wiki](https://github.com/devituz/lagodev/wiki) — long-form guides
- [🚀 Showcase](SHOWCASE.md) — apps built with lagodev
- [Code of Conduct](CODE_OF_CONDUCT.md) · [Contributing](CONTRIBUTING.md)

## License

MIT — see [LICENSE](LICENSE).

<div align="center">

If lagodev saved you time, a ⭐ helps other Go developers find it.

[![Star History Chart](https://api.star-history.com/svg?repos=devituz/lagodev&type=Date)](https://star-history.com/#devituz/lagodev&Date)

</div>
