# Architecture

This document walks through how the packages fit together. If you've used
Laravel/Eloquent before, most concepts map 1:1; if you're coming from raw
`database/sql`, this gives you a tour of the abstractions involved.

## Layered overview

```
┌───────────────────────────────────────────────────────────────┐
│  Application code (HTTP handlers, gRPC servers, jobs, CLIs)   │
└───────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────┐  ┌─────────────────┐  ┌────────────────────┐
│  orm (Query[T]) │  │  factory.New[T] │  │  cli.App (cobra)   │
└─────────────────┘  └─────────────────┘  └────────────────────┘
                            │                       │
                            ▼                       ▼
┌─────────────────┐  ┌─────────────────┐  ┌────────────────────┐
│ query.Builder   │  │ schema.Blueprint│  │ migrations.Migrator│
└─────────────────┘  └─────────────────┘  └────────────────────┘
                            │
                            ▼
┌───────────────────────────────────────────────────────────────┐
│  database.Connection (Tx, Executor, Grammar) → *sql.DB        │
└───────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│ postgres    │  │ mysql       │  │ sqlite      │  ← drivers/* register
└─────────────┘  └─────────────┘  └─────────────┘    grammars + factories
```

## Driver abstraction (`database/`)

The framework is built on three small interfaces:

```go
type Executor interface {
    ExecContext(...)   (sql.Result, error)
    QueryContext(...)  (*sql.Rows, error)
    QueryRowContext(...) *sql.Row
    PrepareContext(...) (*sql.Stmt, error)
}

type Grammar interface {
    Name() string
    Quote(ident string) string
    Placeholder(n int) string
    CompileType(kind string, opts ColumnTypeOptions) string
    SupportsReturning() bool
    LastInsertIDStrategy() InsertIDStrategy
}
```

`*database.Connection` and `*database.Tx` both satisfy `Executor`, so query
code is agnostic about whether it's inside a transaction. `Grammar`
encapsulates everything dialect-specific: identifier quoting (`"…"` vs
`` `…` ``), placeholders (`?` vs `$1`), and column type rendering.

**Adding a driver** is a matter of writing a `Grammar` implementation,
calling `database.Register("yourdialect", openFn)` in `init()`, and
blank-importing the package from your binary.

## Schema (`schema/`)

`schema.Create("users", func(t *schema.Blueprint) {...})` returns a
`*schema.Definition`. The migration runner calls
`schema.NewCompiler(grammar).Compile(def)`, which produces one or more
`Statement`s — usually a single `CREATE TABLE` plus separate `CREATE INDEX`
statements.

Column kinds are *portable strings* (`"bigInteger"`, `"jsonb"`, …); each
`Grammar.CompileType` maps them to dialect SQL. This is the seam that lets
the same blueprint compile to BIGSERIAL on Postgres, BIGINT AUTO_INCREMENT on
MySQL, and INTEGER PRIMARY KEY on SQLite.

## Migrations (`migrations/`)

A migration is anything implementing:

```go
type Migration interface {
    Name() string
    Up(ctx *Context) error
    Down(ctx *Context) error
}
```

In practice you use `migrations.Define(name, up, down)` and register it in
`init()`. The `Registry` keeps them sorted by name (timestamp-prefixed by
convention), and `Migrator`:

1. opens a transaction per migration,
2. acquires an advisory lock (`pg_advisory_lock` on Postgres, a `migrations_lock`
   row elsewhere),
3. applies pending migrations in batch `N+1`,
4. records each application with its **SHA-256 checksum** so subsequent runs
   can detect if a migration was edited after being applied.

`Pretend` mode never executes — it captures emitted SQL via the
`Context.preview` flag and prints it.

## Query builder (`query/`)

`query.New(conn, "users")` produces a chainable builder. Internally it holds
slices of `wheres`, `joins`, `orders`, `groups`, `havings`; `ToSQL()` walks
them in one pass and emits a single string with positional arguments. The
hot path is allocation-conscious (we pre-size `args`) — see
[`benchmarks/`](../benchmarks).

Key features:

- Nested `Where(func(q *Builder) { … })` groups
- `WhereIn([]any | []int | []string | []int64 | []uint64)`
- Aggregates (`Count`, `Sum`, `Avg`, `Max`, `Min`)
- `LockForUpdate` / `SharedLock`
- Insert / `InsertGetID` (returning-aware) / `InsertBatch`
- Update with WHERE binding placeholder reuse
- Postgres `$N` placeholder numbering is preserved across WHERE/INSERT
  binding boundaries

## ORM (`orm/`)

Models embed `orm.Model`, which gives them `ID`, `CreatedAt`, `UpdatedAt`,
`DeletedAt`. The reflection cache in `internal/reflectutil` parses each
struct exactly once and stores:

- column ↔ field map
- primary-key field
- `created_at`/`updated_at`/`deleted_at` field pointers
- cast tags (`orm:"cast:json"`)

`orm.Save` is a single function — it inserts when the primary key is zero,
otherwise updates. Hooks (`BeforeCreate`/…) are dispatched as interface
assertions at runtime: there's no global state to register them with.

Soft-delete behavior:

- Default scope appends `WHERE deleted_at IS NULL` automatically.
- `Builder.WithTrashed()` removes the filter.
- `Builder.OnlyTrashed()` flips it.
- `orm.Restore` and `orm.ForceDelete` are explicit operations.

## Relations (`relations/`)

Each relation is a plain function:

```go
relations.HasManyOf(conn, parent, &dst, "user_id").Load(ctx)
```

For eager-loading a batch of parents, use `LoadMany` — it issues a *single*
`WHERE foreign_key IN (...)` query and distributes results by callback. This
avoids the N+1 problem without needing global state.

`BelongsToMany` joins through a pivot table; `MorphMany` filters on a
`<name>_type` discriminator column.

## CLI (`cli/`)

The CLI is a tiny Cobra wrapper. `cli.New(opts)` builds the root command and
mounts:

- `make:*` generators (read `cli/stubs/*.stub` via `go:embed`),
- `migrate*` commands (call into `migrations.Migrator`),
- `db:seed` / `db:wipe`.

Stub overrides are first-class: `cmd.SetStubOverride("model", "./mystubs/model.stub")`
lets a project ship custom templates without forking. The same `Env` struct
gets passed to every command, so it's trivial to add new commands inside
your own application.

## Factory & seeder (`factory/`, `seeder/`)

`factory.New[T](conn, definition)` returns a generic builder. States are
just `func(*Faker, *T)` callbacks; states + per-instance overrides + before-
and after-hooks compose cleanly because everything is a slice of functions.

The seeder runner topologically sorts registered seeders by their declared
`Dependencies()`. Cycles fail loudly; the same seeder can be run on its own
via `--only`.

## Threading & transactions

- `*database.Manager`, `*database.Connection`, the migration `Registry`,
  the seeder `Registry`, and the `reflectutil` schema cache are all
  goroutine-safe.
- The query builder is *not* safe for concurrent reuse — make a new builder
  per goroutine. This is intentional: chaining mutates fields, and adding a
  mutex would be wasted work in 99% of use cases.
- Transactions are scoped to a function via `conn.Transaction(ctx, fn)` —
  the closure receives a `*Tx` that satisfies `Executor`; queries inside
  the closure run in the transaction.

## Performance notes

See [`benchmarks/`](../benchmarks) for live numbers. Rough indicators on
Apple M1 Pro / SQLite in-memory:

- `query.Builder.ToSQL` (10 wheres): ~1 µs / ~1 KB allocations
- `orm.Save` insert (round-trip): ~5 µs
- `InsertBatch(100)`: ~140 µs (≈ 1.4 µs/row)
- `Query[T].Get()` over 200 rows w/ hydration: ~310 µs

The reflection cache is the main correctness win; without it Save() would
re-parse the model on every call.
