# Getting started

This guide takes you from `go mod init` to a running JSON API with
migrations, models, factories, and a Laravel-style HTTP server in about
ten minutes.

## 1. Install

```bash
mkdir myapp && cd myapp
go mod init github.com/you/myapp
go get github.com/devituz/lagodev@latest
go install github.com/devituz/lagodev/cmd/lago@latest
```

`lago` is the CLI binary (`artisan` is an interchangeable alias). It
lives in `$(go env GOPATH)/bin`.

## 2. Scaffold the project

```bash
lago init           # writes lago.json, config/, routes/
lago env:init       # writes .env with documented defaults
```

You now have:

```
myapp/
├── .env                  ← DB_CONNECTION, APP_*, etc.
├── lago.json             ← directory layout
├── config/
│   ├── app.go            ← AppConfig from env
│   └── database.go       ← database.Config from env
└── routes/
    └── api.go            ← routes.Register(app)
```

## 3. Generate your first resource

```bash
lago make:model Post -mfsc \
    --fields="title:string,body:text,published:bool:default(false)"
```

This adds — all in one shot, with field types kept in lockstep:

```
models/post.go
migrations/2026_01_01_000000_create_posts_table.go
factories/post_factory.go
seeders/post_seeder.go
services/post_service.go
controllers/post_controller.go
```

Inspect `controllers/post_controller.go` — every handler is just one or
two lines because the framework auto-converts return values into JSON
responses (see [`docs/WEB.md`](WEB.md) for details).

## 4. Wire the route

Edit `routes/api.go`:

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

`Resource()` registers six routes in one call:

```
GET     /api/v1/posts            → Index
GET     /api/v1/posts/{id}       → Show
POST    /api/v1/posts            → Store
PUT     /api/v1/posts/{id}       → Update
PATCH   /api/v1/posts/{id}       → Update
DELETE  /api/v1/posts/{id}       → Destroy
```

## 5. Write `main.go`

```go
package main

import (
    "log"

    "github.com/devituz/lagodev/config"
    "github.com/devituz/lagodev/database"
    _ "github.com/devituz/lagodev/drivers/sqlite"
    "github.com/devituz/lagodev/web"

    _ "github.com/you/myapp/migrations"   // registers migrations

    appcfg "github.com/you/myapp/config"
    "github.com/you/myapp/routes"
)

func main() {
    _ = config.LoadEnv()
    cfg   := appcfg.App()
    dbCfg := appcfg.Database()

    mgr := database.NewManager()
    conn, err := mgr.Open("default", dbCfg)
    if err != nil { log.Fatal(err) }

    app := web.New(
        web.WithDatabase(conn),
        web.WithManager(mgr),
        web.WithMigrations(nil),
        web.WithAddr(cfg.Addr),
    )
    routes.Register(app)
    app.MustRun()
}
```

## 6. Run it

```bash
go run .
```

The server prints every registered route and starts listening on
`:8080`:

```
ro'yxatdan o'tgan marshrutlar:
  GET     /health
  GET     /api/v1/posts
  GET     /api/v1/posts/{id}
  POST    /api/v1/posts
  PUT     /api/v1/posts/{id}
  PATCH   /api/v1/posts/{id}
  DELETE  /api/v1/posts/{id}
server tinglamoqda: http://localhost:8080
```

Try it:

```bash
curl -X POST http://localhost:8080/api/v1/posts \
    -H "Content-Type: application/json" \
    -d '{"Title":"Hello","Body":"World"}'
# → 201 Created

curl http://localhost:8080/api/v1/posts
# → 200 OK with the list

curl -X DELETE http://localhost:8080/api/v1/posts/1
# → 204 No Content (soft-delete)
```

## 7. Test

```go
// services/post_service_test.go
package services_test

import (
    "context"
    "testing"

    lagotest "github.com/devituz/lagodev/testing"
    "github.com/you/myapp/services"
)

func TestList(t *testing.T) {
    conn, cleanup := lagotest.SQLite(t)
    defer cleanup()

    svc := services.NewPostService(conn)
    out, err := svc.List(context.Background())
    if err != nil { t.Fatal(err) }
    if len(out) != 0 { t.Fatalf("expected empty, got %d", len(out)) }
}
```

`lagotest.SQLite(t)` spins up an in-memory database and applies every
migration registered via `init()`.

## 8. Where next?

- **[WEB.md](WEB.md)** — full reference for the `web` framework
  (Context, middleware, lifecycle).
- **[CLI.md](CLI.md)** — every command and flag.
- **[ORM.md](ORM.md)** — query builder cookbook.
- **[examples/blog/](../examples/blog)** — a more realistic project
  with three models, foreign keys, and seeders.
