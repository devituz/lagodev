# Container & application lifecycle

This document covers two layers: the `container/` package — a type-safe,
generics-based dependency-injection (DI) container — and the `app/` package,
which composes that container with the web, config and migration layers into a
two-phase application lifecycle.

If you've used Laravel's service container or NestJS's modules before, the
concepts map closely; the difference is that everything here is keyed on Go
*types* instead of string class names, so resolution is checked at compile time
and you never write a type assertion.

## Overview

```
┌───────────────────────────────────────────────────────────────┐
│  app.App — owns the lifecycle (Register → Boot → Run)         │
└───────────────────────────────────────────────────────────────┘
            │ register/boot providers & modules
            ▼
┌─────────────────┐  ┌─────────────────┐  ┌────────────────────┐
│  app.Provider   │  │  app.Module     │  │  app.Command       │
│  (Register/Boot)│  │  (group of ↑)   │  │  (collected)       │
└─────────────────┘  └─────────────────┘  └────────────────────┘
            │ all bind/resolve through one
            ▼
┌───────────────────────────────────────────────────────────────┐
│  container.Container — Bind / Singleton / Instance / Make     │
│  keyed on reflect.Type, concurrency-safe, child Scope()s      │
└───────────────────────────────────────────────────────────────┘
```

The container is a standalone package — you can use it on its own. The `app`
package never modifies it; it just owns one `*container.Container` and drives
providers against it.

## Quick start — bind & resolve

Registration and resolution go through package-level generic functions, so the
type you bind is the type you get back:

```go
package main

import (
	"fmt"

	"github.com/devituz/lagodev/container"
)

type Config struct{ DSN string }
type DB struct{ cfg Config }
type UserRepo struct{ db *DB }

func main() {
	c := container.New()

	// An already-built value, returned as-is on every resolve.
	container.Instance(c, Config{DSN: "postgres://localhost/app"})

	// A singleton: the factory runs at most once, result is cached & shared.
	container.Singleton(c, func(c *container.Container) (*DB, error) {
		cfg, err := container.Make[Config](c)
		if err != nil {
			return nil, err
		}
		return &DB{cfg: cfg}, nil
	})

	// A transient binding: the factory runs on every resolve.
	container.Bind(c, func(c *container.Container) (*UserRepo, error) {
		db, err := container.Make[*DB](c)
		if err != nil {
			return nil, err
		}
		return &UserRepo{db: db}, nil
	})

	repo := container.MustMake[*UserRepo](c)
	fmt.Println(repo.db.cfg.DSN) // postgres://localhost/app
}
```

`container.New()` returns a ready container; its zero value is not usable. Every
exported operation is safe for concurrent use.

### Resolving

```go
repo, err := container.Make[*UserRepo](c) // returns (T, error)
repo := container.MustMake[*UserRepo](c)  // panics on error
```

When no binding matches the requested type, `Make` returns `ErrNotBound`:

```go
if _, err := container.Make[*Missing](c); errors.Is(err, container.ErrNotBound) {
	// not registered (in this container or any ancestor scope)
}
```

## Singleton vs transient vs instance

Three registration verbs, three lifetimes:

| Verb        | Lifetime  | Factory runs                                       |
| ----------- | --------- | -------------------------------------------------- |
| `Bind`      | transient | on **every** `Make` — a fresh value each time      |
| `Singleton` | singleton | at most **once**; the result is cached and shared  |
| `Instance`  | instance  | never — you supply the already-built value          |

`Singleton` caching is concurrency-safe: under concurrent `Make` the factory
runs exactly once and every caller observes the same value. Use `Singleton` for
shared, expensive-to-build resources (database connections, HTTP clients,
config-derived state) and `Bind` for cheap, stateful-per-use objects you don't
want to share.

```go
container.Bind(c, func(c *container.Container) (*UserRepo, error) { ... })      // new each Make
container.Singleton(c, func(c *container.Container) (*DB, error) { ... })       // built once
container.Instance(c, Config{DSN: "..."})                                       // pre-built
```

## Generic typed resolution

Bindings are keyed on the static type of `T`, so `*DB` and `DB` are distinct
keys, as are `*UserRepo` and `UserRepo`. There is no string registry and no
`interface{}` to assert — resolution is fully type-checked:

```go
container.Make[*DB](c)  // resolves the *DB binding
container.Make[DB](c)   // ErrNotBound — different key
```

You can bind an interface to a concrete implementation by giving the factory the
interface as `T`:

```go
type Mailer interface{ Send(to, body string) error }
type smtpMailer struct{}

func (smtpMailer) Send(to, body string) error { return nil }

container.Singleton(c, func(c *container.Container) (Mailer, error) {
	return smtpMailer{}, nil
})

m := container.MustMake[Mailer](c) // typed as Mailer
```

## Named bindings

When several implementations of one type must coexist, register them under
distinct names with the `*Named` variants:

```go
container.SingletonNamed(c, "primary", func(c *container.Container) (*DB, error) {
	return &DB{cfg: Config{DSN: "postgres://primary/app"}}, nil
})
container.SingletonNamed(c, "replica", func(c *container.Container) (*DB, error) {
	return &DB{cfg: Config{DSN: "postgres://replica/app"}}, nil
})

primary := container.MustMakeNamed[*DB](c, "primary")
replica := container.MustMakeNamed[*DB](c, "replica")
```

The named verbs mirror the unnamed ones: `BindNamed`, `SingletonNamed`,
`InstanceNamed`, `MakeNamed`, `MustMakeNamed`. An unnamed binding and a named
binding of the same type are independent.

## Scopes (per-request lifetimes)

`c.Scope()` creates a child container that inherits the parent's bindings and
may override them. Resolution walks from the child up through its ancestors:

```go
root := container.New()
container.Singleton(root, func(c *container.Container) (*DB, error) {
	return &DB{cfg: Config{DSN: "shared"}}, nil
})

req := root.Scope() // child scope, e.g. one per HTTP request

// Inherited: a parent singleton resolves to the parent's shared instance
// even when made from the child.
db := container.MustMake[*DB](req)

// Overridden: a singleton (re)defined in the scope caches inside that scope,
// giving a per-scope (per-request) lifetime.
type RequestID struct{ V string }
container.Instance(req, RequestID{V: "abc-123"})
id := container.MustMake[RequestID](req)
```

This is the mechanism behind per-request services: bind shared infrastructure on
the root, create a `Scope()` per request, and override the request-specific
values there.

## Autowiring structs with Build

`Build` is opt-in, reflection-based autowiring for struct types. It allocates a
value of struct type `T` and fills each exported field whose type is already
bound:

```go
type Handler struct {
	DB    *DB     // filled from the *DB binding
	Repo  *UserRepo
	Cache *Cache  `inject:"-"`      // skipped, left zero
	Mailer Mailer `inject:""`       // required: missing binding is an error
}

h, err := container.Build[Handler](c)
```

Rules (see `go doc container.Build` for the canonical list):

- `T` (after dereferencing one pointer level) must be a struct.
- Each exported field is resolved by its **exact type** (unnamed binding). A
  field tagged `inject:"name"` resolves the **named** binding instead;
  `inject:"-"` is skipped.
- An unbound field is left at its zero value — unless tagged `inject:""` or
  `inject:"name"`, in which case the missing binding is a hard error.
- Unexported fields are never touched.

`Build` resolves through `c` (and its ancestors/overrides) and participates in
cycle detection, so a dependency cycle fails loudly instead of recursing.

## Service providers & the application lifecycle (`app/`)

The `app` package turns a pile of bindings into an orchestrated program. The
unit of wiring is a **Provider**, which contributes services in two phases:

```go
type Provider interface {
	Register(c *container.Container) error // bind only — do NOT resolve here
}

type Booter interface {
	Boot(c *container.Container) error // resolve & use — runs after every Register
}
```

The split matters: **Register binds, Boot resolves.** During `Register` other
providers may not have run yet, so you only declare factories. By the time
`Boot` runs, every provider has registered, so a provider may safely depend on
bindings owned by any other provider. Providers boot in registration order,
which is deterministic.

```go
type UserServiceProvider struct{}

func (UserServiceProvider) Register(c *container.Container) error {
	container.Singleton(c, func(c *container.Container) (*UserRepo, error) {
		db, err := container.Make[*DB](c)
		if err != nil {
			return nil, err
		}
		return &UserRepo{db: db}, nil
	})
	return nil
}

// Optional second phase.
func (UserServiceProvider) Boot(c *container.Container) error {
	repo := container.MustMake[*UserRepo](c)
	_ = repo // attach routes, schedule jobs, warm caches, etc.
	return nil
}
```

A provider that only binds services can skip `Boot` entirely. For trivial
bind-only providers, `RegisterFunc` adapts a plain function:

```go
app.RegisterFunc(func(c *container.Container) error {
	container.Singleton(c, newClock)
	return nil
})
```

### Modules

A **Module** groups everything a feature needs — providers, routes, migrations
and CLI commands — under one name, so a whole feature is wired with a single
`Register` call. A `Module` is itself a `Provider`:

```go
var UsersModule = app.NewModule("users",
	app.WithProviders(&UserServiceProvider{}),
	app.WithRoutes(func(r *web.Router) {
		r.Get("/users", listUsers)
	}),
	app.WithModuleMigrations(func(reg *migrations.Registry) {
		reg.Register(createUsersTable{})
	}),
	app.WithModuleRegister(func(c *container.Container) error {
		// module-level bindings, after the module's providers register
		return nil
	}),
	app.WithModuleBoot(func(c *container.Container) error {
		// module-level wiring, after the module's providers boot
		return nil
	}),
	app.WithCommands(func() []app.Command {
		return []app.Command{usersPruneCommand{}}
	}),
)
```

Registering a module registers all of its providers (in slice order), then the
module's own `Register`/`Boot` callbacks last so they observe the providers'
bindings. The module's route and migration callbacks run during the boot phase,
giving the feature access to the web router and migration registry. The
`Module` fields (`Providers`, `RegisterFn`, `BootFn`, `Routes`, `Migrations`,
`Commands`) can also be assembled as a struct literal if you prefer.

### The App

`app.New(opts...)` owns the container, the `web.App`, the migration registry and
the ordered provider set, and drives the lifecycle:

```go
package main

import (
	"context"
	"log"

	"github.com/devituz/lagodev/app"
)

func main() {
	a := app.New(
		app.WithAddr(":8080"),
		// app.WithContainer(c), app.WithMigrations(reg),
		// app.WithWebApp(w), app.WithWebOptions(web.With...),
	)

	a.Register(UsersModule, BillingModule)

	if err := a.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

Lifecycle methods:

- `Register(providers...) *App` — appends providers/modules (chainable).
- `Boot() error` — runs every provider's `Register`, then every `Boot`, in
  registration order. `Booted()` reports whether this has happened.
- `Run(ctx) error` — boots, then starts the HTTP server via `web.App.Run`,
  preserving its graceful-shutdown semantics. It blocks until the server stops.
  Cancelling `ctx` before boot completes aborts startup.
- `Test() error` — boots without binding a port, for tests. Afterwards drive
  HTTP requests through `a.Web().Test(req)`.

Accessors expose the composed pieces: `Container()`, `Web()`, `Migrations()`,
`Commands()` (collected from modules; the app does not dispatch a CLI itself —
that is left to the host program or the `cli` package), and `Scope()` for a
fresh child container.

Construction options (`app.Option`): `WithAddr`, `WithContainer`,
`WithMigrations`, `WithWebApp`, `WithWebOptions`. `WithWebOptions` is ignored if
`WithWebApp` already supplies a ready `web.App`.

### Testing an app

```go
func TestUsersRoute(t *testing.T) {
	a := app.New()
	a.Register(UsersModule)
	if err := a.Test(); err != nil { // boot without a port
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/users", nil)
	resp := a.Web().Test(req)
	if resp.Code != http.StatusOK {
		t.Fatalf("got %d", resp.Code)
	}
}
```

## Production notes — avoiding global state

The whole point of the container is that there is **no global registry** to
mutate. Concrete practices:

- **One root container per process.** Build it in `main` (or let `app.New`
  own one) and thread it explicitly. Resist the urge to stash it in a package
  variable — pass it through providers and `Make` instead.
- **Shared infrastructure is a `Singleton`; per-request state lives in a
  `Scope()`.** Don't reach for package-level `var db *sql.DB`; bind the
  connection once and resolve it where needed. For request-scoped values
  (request ID, authenticated user), create `root.Scope()` per request and
  override there — the override caches inside that scope and is GC'd with it.
- **Keep `Register` pure.** It must not resolve other providers' bindings or
  perform side effects that depend on the rest of the app; that belongs in
  `Boot`. Violating this re-introduces ordering coupling, the exact thing the
  two-phase lifecycle removes.
- **Boot ordering is deterministic** (registration order), so wiring is
  reproducible across runs — no map-iteration nondeterminism.
- **The container is concurrency-safe; the `App` is not.** Configure and boot
  the app from a single goroutine, then serve. Once booted, the container it
  owns can be hit from many goroutines (e.g. one resolve per request).
- **Fail loudly.** Factories return `error`; let it propagate. `Build` and
  scoped resolution include cycle detection, so a dependency loop surfaces as an
  error rather than a stack overflow.

## See also

- [ARCHITECTURE.md](ARCHITECTURE.md) — how the data packages fit together.
- [GETTING_STARTED.md](GETTING_STARTED.md) — project scaffolding and the web layer.
- `go doc .../container` and `go doc .../app` — the canonical API reference.
