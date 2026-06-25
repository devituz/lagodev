# Database layer

This page covers everything *under* the model layer: connection
management (`database`), the standalone query builder (`query`), model
relationships (`relations`), and database seeding (`seeder`). For the
model API itself — struct tags, `orm.Save`, hooks, casts — see
[`ORM.md`](ORM.md). For schema changes see [`MIGRATIONS.md`](MIGRATIONS.md).

## Connections & managers

A `*database.Connection` wraps `*sql.DB` with the dialect `Grammar`, the
resolved `Config`, a logger, and the active `*time.Location`. It is
goroutine-safe and is the canonical handle passed everywhere across the
framework. You rarely build one by hand — open it through a `Manager`,
which holds a set of *named* connections.

```go
import (
    "github.com/devituz/lagodev/database"
    _ "github.com/devituz/lagodev/drivers/postgres" // registers "postgres"
)

mgr := database.NewManager()

conn, err := mgr.Open("default", database.Config{
    Driver: "postgres", Host: "127.0.0.1", Port: 5432,
    Username: "app", Password: os.Getenv("DB_PASSWORD"), Database: "app",
    SSLMode: "require", TimeZone: "Asia/Tashkent",

    MaxOpenConns: 25, MaxIdleConns: 10,
    ConnMaxIdleTime: 5 * time.Minute, ConnMaxLifetime: 30 * time.Minute,

    LogQueries: true, SlowQuery: 200 * time.Millisecond,
})
if err != nil {
    log.Fatal(err)
}
defer mgr.Close()
```

The driver name must be **registered** — that happens in the driver
package's `init()`, so a blank import (`_ ".../drivers/postgres"`) is
enough. Built-in drivers: `postgres`, `mysql`, `sqlite` (and `redis`).
`Open` returns a clear error if you forget the import.

### Config

`database.Config` describes one connection. Either set `DSN` directly
(it wins) or let `BuildDSN()` assemble one from the field-based options:

| Field                            | Purpose                                              |
|----------------------------------|------------------------------------------------------|
| `Driver`                         | Registered driver name (`postgres`/`mysql`/`sqlite`) |
| `DSN`                            | Pre-built connection string; overrides the fields below |
| `Host`/`Port`/`Username`/`Password`/`Database`/`Schema` | Field-based connection params |
| `SSLMode`                        | e.g. `disable`, `require`, `verify-full` (postgres)  |
| `TimeZone`                       | IANA zone (`Asia/Tashkent`), `UTC`, or `Local`       |
| `Params`                         | Extra driver DSN params (`map[string]string`)        |
| `MaxOpenConns`                   | Hard cap on open connections (0 = unbounded)         |
| `MaxIdleConns`                   | Idle pool size                                        |
| `ConnMaxIdleTime`/`ConnMaxLifetime` | Recycle idle / cap max age of a connection       |
| `LogQueries`/`SlowQuery`         | Emit SQL to the logger; slow-query threshold         |
| `TablePrefix`/`MigrationsTable`  | Table-name prefix; migrations bookkeeping table      |

`TimeZone` drives `conn.Location()` / `conn.Now()`, which the ORM uses
for `CreatedAt`/`UpdatedAt` so written timestamps honor the app's zone.

### Multiple connections

The whole point of a `Manager` is juggling more than one database — a
primary plus a read replica, or a separate analytics store:

```go
_, _ = mgr.Open("default", primaryCfg)
_, _ = mgr.Open("replica", replicaCfg)
_, _ = mgr.Open("analytics", warehouseCfg)

mgr.SetDefault("default")

primary, _   := mgr.Default()                 // the default connection
replica, _   := mgr.Connection("replica")     // by name
analytics, _ := mgr.Connection("analytics")

// Health-check every connection at once.
if err := mgr.Ping(ctx); err != nil {
    log.Fatal(err)
}
```

`Manager.Use(name, conn)` registers an externally-built connection (e.g.
a test harness handing you an in-memory SQLite); `mgr.SetLogger(l)`
attaches a logger to connections opened afterwards. There is also a
process-wide `database.Global` manager — application code typically uses
it, while **library and test code should construct their own** `Manager`
to stay isolated.

### Raw access

`Connection` implements the standard `database/sql` surface
(`QueryContext`, `QueryRowContext`, `ExecContext`, `PrepareContext`), so
you can always drop to raw SQL when the builder isn't enough:

```go
rows, err := conn.QueryContext(ctx, "SELECT id, total FROM orders WHERE status = $1", "paid")
res, _    := conn.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < $1", time.Now())
```

These calls reject usage after `Close()` with `database.ErrClosed`, and
feed the query log when `LogQueries` is on.

## Transactions

`Connection.Transaction` runs a closure inside a transaction, committing
on `nil` and **rolling back automatically** on any returned error or
panic:

```go
err := conn.Transaction(ctx, func(tx *database.Tx) error {
    if _, err := tx.ExecContext(ctx,
        "UPDATE accounts SET balance = balance - $1 WHERE id = $2", 100, from); err != nil {
        return err
    }
    _, err := tx.ExecContext(ctx,
        "UPDATE accounts SET balance = balance + $1 WHERE id = $2", 100, to)
    return err
})
```

`*database.Tx` embeds `*sql.Tx` and satisfies `database.Executor` — the
same minimal interface (`ExecContext`/`QueryContext`/`QueryRowContext`/
`PrepareContext`) that a raw connection satisfies. That means every query
target works transparently inside or outside a transaction. Pass `tx` to
the query builder via `SetExecutor(tx)`, or to the ORM via `WithTx(tx)`.

Need explicit isolation? Use `TransactionWith`:

```go
err := conn.TransactionWith(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable},
    func(tx *database.Tx) error { /* ... */ return nil })
```

### Savepoints

`Tx` supports nested savepoints for partial rollback:

```go
conn.Transaction(ctx, func(tx *database.Tx) error {
    if err := tx.Savepoint(ctx, "sp1"); err != nil {
        return err
    }
    if err := dodgy(tx); err != nil {
        return tx.RollbackTo(ctx, "sp1") // undo dodgy(), keep the rest
    }
    return nil
})
```

`tx.Connection()` gives you back the parent `*Connection` from inside the
closure if you need its grammar or location.

## The query builder

`query.New(conn, "table")` returns a fluent `*query.Builder`. It is used
by the ORM but is fully usable on its own — terminal methods that read
rows hand you a `*sql.Rows`, so you stay in control of scanning.

```go
import "github.com/devituz/lagodev/query"

rows, err := query.New(conn, "users").
    Select("id", "name", "email").
    Where("is_admin", "=", true).
    Where("created_at", ">", "2026-01-01").
    OrderBy("id", "desc").
    Limit(20).
    Get(ctx)
defer rows.Close()
```

> A `Builder` is **single-use**: chaining methods mutate and return the
> receiver, so build it, execute once, discard. Aggregate helpers
> (`Count`, `Sum`, …) are the exception — they run on an internal clone
> and may be called mid-chain. Use `Clone()` to fork a builder explicitly.

### Where clauses

`Where` is variadic — three forms:

```go
.Where("status", "=", "active")  // column, operator, value
.Where("status", "active")       // two args ⇒ "=" implied
.Where(func(q *query.Builder) {  // one arg ⇒ nested group
    q.Where("role", "=", "admin").OrWhere("role", "=", "owner")
})
```

The last form produces `(role = ? OR role = ?)`. Operator constants live
in the package (`query.OpEq`, `OpNe`, `OpLt`, `OpLte`, `OpGt`, `OpGte`,
`OpLike`, `OpILike`, `OpIn`, `OpNotIn`, `OpBetween`, …). Dedicated
helpers cover the rest:

```go
.WhereIn("id", []int{1, 2, 3})
.WhereNotIn("status", []string{"banned", "deleted"})
.WhereNull("verified_at")
.WhereNotNull("verified_at")
.WhereBetween("age", 18, 65)
.WhereRaw("LOWER(name) = LOWER(?)", "Ada")
.OrWhere("role", "=", "owner")
```

### Joins, grouping, ordering

```go
rows, err := query.New(conn, "orders").
    Select("orders.id", "users.name").
    Join("users", "users.id = orders.user_id").
    LeftJoin("coupons", "coupons.id = orders.coupon_id").
    Where("orders.status", "=", "paid").
    GroupBy("users.id").
    Having("count(*)", ">", 5).
    OrderByRaw("sum(orders.total) DESC").
    Get(ctx)
```

`Latest(col...)` / `Oldest(col...)` are shorthands for ordering by a
timestamp column (defaults to `created_at`). `Distinct()`, `Offset(n)`,
and `Limit(n)` round out shaping.

### Aggregates

These run on an internal clone and return scalars directly:

```go
n, _      := query.New(conn, "users").Where("is_admin", "=", true).Count(ctx)
exists, _ := query.New(conn, "users").Where("email", "=", e).Exists(ctx)
total, _  := query.New(conn, "orders").Where("status", "=", "paid").Sum(ctx, "total")
avg, _    := query.New(conn, "reviews").Avg(ctx, "score")
hi, _     := query.New(conn, "orders").Max(ctx, "total")
lo, _     := query.New(conn, "orders").Min(ctx, "total")
```

### Writes

```go
res, _  := query.New(conn, "users").Insert(ctx, map[string]any{"name": "Ada", "email": "ada@x.io"})
id, _   := query.New(conn, "users").InsertGetID(ctx, map[string]any{"name": "Grace"}, "id")
_, _     = query.New(conn, "users").InsertBatch(ctx, []map[string]any{{"name": "A"}, {"name": "B"}})

affected, _ := query.New(conn, "users").Where("id", "=", 42).Update(ctx, map[string]any{"is_admin": true})
deleted, _  := query.New(conn, "sessions").Where("expires_at", "<", time.Now()).Delete(ctx)
_ = query.New(conn, "temp_import").Truncate(ctx)
```

### Locking & inspection

```go
.LockForUpdate()   // SELECT ... FOR UPDATE
.SharedLock()      // SELECT ... FOR SHARE / LOCK IN SHARE MODE
```

`ToSQL()` returns the rendered SQL + bound args without executing —
handy in tests and code review:

```go
sql, args, _ := query.New(conn, "users").Where("id", "=", 1).ToSQL()
// SELECT * FROM users WHERE id = ?   [1]
```

### Targeting a transaction

`SetExecutor` repoints a builder at any `database.Executor` — a `*Tx`, or
a raw `*sql.DB`:

```go
conn.Transaction(ctx, func(tx *database.Tx) error {
    _, err := query.New(conn, "users").
        SetExecutor(tx).
        Where("id", "=", 1).
        Update(ctx, map[string]any{"locked": true})
    return err
})
```

## Relations

The `relations` package loads related rows. Relationships are **free
functions** that take the parent and a connection and return a
`*relations.Relation`; you then call `Load` (one parent) or `LoadMany`
(a batch — eager loading without the N+1).

| Constructor                                                         | Kind            | SQL shape                              |
|---------------------------------------------------------------------|-----------------|----------------------------------------|
| `HasOneOf(conn, parent, &child, parentFK)`                          | `HasOne`        | `child.<parentFK> = parent.id`         |
| `HasManyOf(conn, parent, &children, parentFK)`                      | `HasMany`       | `children.<parentFK> = parent.id`      |
| `BelongsToOf(conn, parent, &owner, foreignKey)`                     | `BelongsTo`     | `owner.id = parent.<foreignKey>`       |
| `BelongsToManyOf(conn, parent, &children, pivot, parentFK, relatedFK)` | `BelongsToMany` | through a pivot table                  |
| `MorphManyOf(conn, parent, &children, morphName, morphValue)`       | `MorphMany`     | polymorphic `morphName_type/_id`       |

`Kind` enumerates `HasOne`, `HasMany`, `BelongsTo`, `BelongsToMany`,
`MorphOne`, `MorphMany`.

### has-one / has-many / belongs-to

```go
import "github.com/devituz/lagodev/relations"

author := &Author{ID: 1}

var books []Book
// books = every row where books.author_id = author.id
_ = relations.HasManyOf(conn, author, &books, "author_id").Load(ctx)

var profile Profile
_ = relations.HasOneOf(conn, author, &profile, "author_id").Load(ctx)

// inverse side — the child carries the foreign key
book := &Book{AuthorID: 1}
var owner Author
_ = relations.BelongsToOf(conn, book, &owner, "author_id").Load(ctx)
```

### many-to-many

```go
user := &User{ID: 1}

var roles []Role
_ = relations.BelongsToManyOf(conn, user, &roles,
    "role_user", // pivot table
    "user_id",   // pivot FK → parent
    "role_id",   // pivot FK → related
).Load(ctx)
```

### Polymorphic

```go
post := &Post{ID: 1}

var comments []Comment
// matches comments WHERE commentable_type = 'posts' AND commentable_id = 1
_ = relations.MorphManyOf(conn, post, &comments, "commentable", "posts").Load(ctx)
```

### Eager loading a batch — `LoadMany`

`Load` fires one query per parent. For a slice of parents, build the
relation once and call `LoadMany` — it issues a **single** query covering
all parent keys, then distributes children back via your `assignFn`,
avoiding the N+1:

```go
authors := []*Author{{ID: 1}, {ID: 2}, {ID: 3}}
parents := []any{authors[0], authors[1], authors[2]}

rel := relations.HasManyOf(conn, authors[0], &[]Book{}, "author_id")
err := rel.LoadMany(ctx, parents, func(parent any, children any) {
    parent.(*Author).Books = *(children.(*[]Book))
})
```

### Constraining a load — `Apply`

Set `Relation.Apply` to layer extra constraints onto the child query — a
soft-delete scope, an extra `WHERE`, an `ORDER BY`, a `LIMIT`:

```go
rel := relations.HasManyOf(conn, author, &books, "author_id")
rel.Apply = func(qb *query.Builder) {
    qb.WhereNull("deleted_at").OrderBy("published_at", "desc").Limit(5)
}
_ = rel.Load(ctx)
```

`Apply` is honored by the `query.Builder`-based loaders (HasOne/HasMany/
BelongsTo/Morph*). The `BelongsToMany` loader builds raw SQL and does
**not** invoke it.

## Seeders

A seeder is anything implementing `seeder.Seeder`
(`Name()` + `Run(ctx, conn)`). Declare ordering by also implementing the
optional `Dependent` interface (`Dependencies() []string`) — the runner
topologically sorts on it.

### Defining seeders

Struct form:

```go
type CategorySeeder struct{}

func (CategorySeeder) Name() string { return "categories" }
func (CategorySeeder) Run(ctx context.Context, conn *database.Connection) error {
    _, err := query.New(conn, "categories").
        InsertBatch(ctx, []map[string]any{{"name": "Tech"}, {"name": "Life"}})
    return err
}

// Optional Dependent interface: declares it must run after "categories".
type PostSeeder struct{}
func (PostSeeder) Name() string            { return "posts" }
func (PostSeeder) Dependencies() []string  { return []string{"categories"} }
func (PostSeeder) Run(ctx context.Context, conn *database.Connection) error { /* ... */ return nil }
```

Inline form with `seeder.Define(name, deps, fn)` (builds a `FuncSeeder`):

```go
s := seeder.Define("admin", []string{"users"},
    func(ctx context.Context, conn *database.Connection) error {
        _, err := query.New(conn, "users").
            Insert(ctx, map[string]any{"email": "admin@example.com", "is_admin": true})
        return err
    })
```

### Running seeders

Register into a `Registry` and run with a `Runner`:

```go
reg := seeder.NewRegistry()
reg.Register(CategorySeeder{})
reg.Register(PostSeeder{})

runner := seeder.NewRunner(conn, reg, seeder.Options{
    Transactional: true,        // wrap each seeder in its own transaction
    Logger:        appLogger,
    Only:          nil,         // or []string{"posts"} to scope the run + its deps
})
if err := runner.Run(ctx); err != nil {
    log.Fatal(err)
}
```

There is also a package-level `seeder.Default` registry with the helper
`seeder.Register(s)` — handy when seeders self-register from `init()`,
mirroring how migrations register. The CLI's `migrate:fresh --seed` runs
against it.

### The DatabaseSeeder pattern

For Laravel-style orchestration, use `seeder.Call` inside a parent
seeder's `Run` to fan out to children in explicit order:

```go
type DatabaseSeeder struct{}

func (DatabaseSeeder) Name() string { return "database" }
func (DatabaseSeeder) Run(ctx context.Context, conn *database.Connection) error {
    return seeder.Call(ctx, conn,
        CategorySeeder{},
        UserSeeder{},
        PostSeeder{},
    )
}
```

`Call` runs the seeders in the order passed and wraps any error with the
failing seeder's `Name()` for fast triage.

## Production notes

**Pool sizing.** lagodev applies *no* default pool limits —
`MaxOpenConns == 0` means unbounded. Always set `MaxOpenConns` in
production so total connections across all app instances sit comfortably
under the server's `max_connections`. Keep `MaxIdleConns` ≤
`MaxOpenConns`, and set a `ConnMaxLifetime` (e.g. 30 min) so connections
cycle past load-balancer and DB-side idle timeouts; pair
`ConnMaxIdleTime` with it to shed idle capacity off-peak.

**Context timeouts.** Every entrypoint takes a `context.Context` — use a
per-request deadline so a slow query can't pin a pooled connection
indefinitely:

```go
ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
defer cancel()
rows, err := query.New(conn, "reports").Get(ctx)
```

Keep transactions short — they hold a pooled connection for their whole
lifetime; never block on network I/O or user input inside a
`Transaction` closure.

**Logging & N+1.** Set `LogQueries: true` with a `SlowQuery` threshold on
non-prod/canaries to surface N+1s and missing indexes early, and prefer
`LoadMany` over per-row `Load`.

**Lifecycle.** Open connections once at startup, share the goroutine-safe
`*Connection`, and `defer mgr.Close()` at shutdown. Calls after close
fail fast with `database.ErrClosed`.
