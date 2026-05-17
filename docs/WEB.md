# Web framework

`lagodev/web` is a native HTTP framework with Laravel-style ergonomics.
It needs no router (no Gin, no Fiber, no Echo) — the entire pipeline
lives in lagodev itself. Use it standalone, or call lagodev's ORM from
the framework you already have.

> Available since **v0.8.0**. Handlers return `(any, error)` — the
> framework converts that into JSON responses automatically.

## Hello world

```go
package main

import (
    "github.com/devituz/lagodev/web"
)

func main() {
    app := web.New()
    app.Get("/", func(c *web.Context) (any, error) {
        return map[string]string{"hello": "world"}, nil
    })
    app.MustRun(":8080")
}
```

That's it — a JSON server in 12 lines. Graceful shutdown, panic
recovery, request logging, and route printing are all baked in.

## The `(any, error)` contract

Every handler — including those registered via `Resource()` — returns
`(any, error)`. The framework translates the two values into an HTTP
response automatically:

| `err`                       | `any`     | Status was set? | Result                       |
|-----------------------------|-----------|-----------------|------------------------------|
| `orm.ErrNotFound`           | (ignored) | —               | `404` + `{"error": "..."}`   |
| Other non-nil               | (ignored) | —               | `500` + `{"error": "..."}`   |
| `nil`                       | `nil`     | no              | `204` No Content             |
| `nil`                       | `nil`     | yes             | the status you set, no body  |
| `nil`                       | non-nil   | no              | `200` + JSON                 |
| `nil`                       | non-nil   | yes             | the status you set + JSON    |

You override the status by calling `c.Status(201)` (or by using
`c.Created(v)` for the common case). Anything else is plumbing the
framework does for you.

## Routing — Laravel style

```go
app := web.New()

// Verbs
app.Get(   "/users",        listUsers)
app.Post(  "/users",        createUser)
app.Put(   "/users/{id}",   replaceUser)
app.Patch( "/users/{id}",   updateUser)
app.Delete("/users/{id}",   deleteUser)

// Resource: registers all five REST routes in one call.
app.Resource("posts", &PostController{Service: postSvc})

// Groups: shared prefix + middleware. Composes recursively.
app.Group("/api/v1", func(g *web.Router) {
    g.Use(web.Auth())

    g.Resource("orders", &OrderController{Service: orderSvc})
    g.Group("/admin", func(a *web.Router) {
        a.Use(adminOnly)
        a.Resource("users", &AdminUserController{...})
    })
})

app.MustRun(":8080")
```

### Resource controllers

`app.Resource("posts", ctrl)` registers six routes — five verbs, six
because `PUT` and `PATCH` share the `Update` method:

```
GET     /posts            → ctrl.Index
GET     /posts/{id}       → ctrl.Show
POST    /posts            → ctrl.Store
PUT     /posts/{id}       → ctrl.Update
PATCH   /posts/{id}       → ctrl.Update
DELETE  /posts/{id}       → ctrl.Destroy
```

`ctrl` must implement `web.ResourceController`:

```go
type ResourceController interface {
    Index(c *Context)   (any, error)
    Show(c *Context)    (any, error)
    Store(c *Context)   (any, error)
    Update(c *Context)  (any, error)
    Destroy(c *Context) (any, error)
}
```

A `lago make:controller PostController --model=Post` invocation
generates exactly this shape.

## `*web.Context` reference

`Context` is the only argument every handler receives. It carries the
request, the response writer, the database connection, and a bag of
helpers Laravel developers expect.

### Path and query parameters

```go
id  := c.ParamUint("id")                         // /posts/{id}
sub := c.Param("sub")                            // string version
q   := c.Query("search")                         // ?search=foo
page := c.QueryInt("page", 1)                    // with default
```

### Binding the request body

```go
var p Post
if err := c.Bind(&p); err != nil {
    return nil, err            // 400 is auto-returned by Bind on JSON errors
}
```

`c.Bind` writes a `400 Bad Request` automatically when the body is
malformed; the returned error is just for short-circuiting your handler.

`c.MustBind(&p)` is the same but returns a bool — convenient when you
prefer early returns:

```go
var p Post
if !c.MustBind(&p) { return nil, nil }
```

### Writing responses

```go
c.JSON(200, payload)
c.String(200, "hello")
c.NoContent()
c.Created(v)                  // sets 201 and returns v (use with `return v, nil`)
c.BadRequest("invalid foo")
c.NotFound("post not found")
c.Unauthorized("")
c.Forbidden("")
c.InternalError(err)
c.Status(202)                 // status only, body comes from the return value
```

### Database access

If you called `web.New(web.WithDatabase(conn))`, every `Context` carries
the connection:

```go
func (c *PostController) Index(ctx *web.Context) (any, error) {
    return orm.Query[Post](ctx.DB).OrderBy("id", "desc").Get(ctx.Ctx(), nil)
}
```

`ctx.Ctx()` returns `context.Context` — pass it to every ORM call.

### Storing values on the context

```go
// In middleware:
c.Set("user", currentUser)

// In a downstream handler:
if u, ok := c.Get("user"); ok { ... }
```

## Middleware

Middleware is a function `func(next Handler) Handler`. The first one
registered is the outermost — it sees the request first and the
response last.

```go
app.Use(web.Logger(log.Default()))
app.Use(web.Recovery(log.Default()))
app.Use(web.CORS("*"))
app.Group("/api", func(g *web.Router) {
    g.Use(web.Auth())              // only inside /api/*
    g.Resource("orders", orderCtrl)
})
```

### Built-in

| Function                    | Purpose                                  |
|-----------------------------|------------------------------------------|
| `web.Logger(l)`             | Logs `method path status duration`       |
| `web.Recovery(l)`           | Converts panics into 500 JSON errors     |
| `web.CORS(origins...)`      | Adds CORS headers; `"*"` allows all      |
| `web.Auth()`                | Bearer-token skeleton; 401 on missing    |

`web.New()` installs `Logger` and `Recovery` for you. To opt out,
construct an empty router yourself (`web.Router{}`) and wire it via
`App.WithRouter` — or just don't call `app.Use()`.

### Writing your own

```go
func RequestID() web.Middleware {
    return func(next web.Handler) web.Handler {
        return func(c *web.Context) (any, error) {
            id := uuid.NewString()
            c.Set("request_id", id)
            c.Writer.Header().Set("X-Request-ID", id)
            return next(c)
        }
    }
}

app.Use(RequestID())
```

## Application lifecycle

`web.New` accepts functional options:

| Option                     | Effect                                                  |
|----------------------------|---------------------------------------------------------|
| `WithDatabase(conn)`       | Connection becomes `ctx.DB` for every handler           |
| `WithManager(mgr)`         | `mgr.Close()` called on graceful shutdown               |
| `WithMigrations(reg)`      | Apply migrations at startup; `nil` uses default registry|
| `WithAddr(":8080")`        | Listen address override                                 |
| `WithShutdownTimeout(d)`   | Maximum time to drain in-flight requests                |

```go
app := web.New(
    web.WithDatabase(conn),
    web.WithManager(mgr),
    web.WithMigrations(nil),
    web.WithAddr(cfg.Addr),
)
routes.Register(app)
app.MustRun()
```

`app.Run()` blocks until an interrupt; on `SIGINT` / `SIGTERM` it stops
accepting new connections and waits up to `ShutdownTimeout` for the
running requests to finish, then closes the DB manager.

`app.MustRun()` is the same but calls `log.Fatal` on a startup error.

## Layout — Laravel-style with `lago init`

```
myapp/
├── main.go                       — entry; ~30 lines
├── lago.json                     — directory layout for generators
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
└── controllers/                  — *web.Context handlers
```

Scaffold the whole thing with two commands:

```bash
lago init
lago env:init
lago make:model Post -mfsc --fields="title:string,body:text,published:bool:default(false)"
```

`main.go` is then just plumbing:

```go
func main() {
    _ = config.LoadEnv()
    cfg   := appcfg.App()
    dbCfg := appcfg.Database()

    mgr := database.NewManager()
    conn, _ := mgr.Open("default", dbCfg)

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

## Comparison cheat sheet

| Concern                       | lagodev/web                                | Gin                                          |
|-------------------------------|--------------------------------------------|----------------------------------------------|
| Register a REST resource      | `app.Resource("posts", ctrl)`              | 5 manual handler lines                       |
| `404` on missing model        | `return c.Service.Get(...)` (auto-mapped)  | check `errors.Is(...)`, write JSON, return   |
| `400` on bad JSON             | `if err := c.Bind(&v); err != nil`         | `c.ShouldBindJSON(&v); err`                  |
| `201` Created                 | `return c.Created(v), nil`                 | `c.JSON(201, v)`                             |
| Graceful shutdown             | built-in                                   | implement yourself                           |
| Print routes on startup       | built-in                                   | DIY with `routes := r.Routes()`              |
| Panic → 500                   | built-in `Recovery`                        | `gin.Recovery()`                             |
| Database injection            | `WithDatabase(conn)` → `ctx.DB`            | inject manually or via DI                    |
| Binary size (sqlite + http)   | ~7 MB                                      | ~9 MB (gin adds runtime)                     |

## When to **not** use `web`

- You're embedding lagodev's ORM in an existing Gin/Fiber/Echo app —
  use the ORM directly and ignore `web/` entirely. See
  [`FRAMEWORK_INTEGRATION.md`](FRAMEWORK_INTEGRATION.md) for examples.
- You need WebSockets — `web/` doesn't ship a WS handler yet
  (PRs welcome).
- You need server-side rendered HTML templates — same.
