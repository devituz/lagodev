# Migrations

## Anatomy

A migration is anything implementing the `Migration` interface:

```go
type Migration interface {
    Name() string
    Up(ctx *migrations.Context) error
    Down(ctx *migrations.Context) error
}
```

In practice you use `migrations.Define(name, up, down)`:

```go
package migrations

import (
    "github.com/devituz/lagodev/migrations"
    "github.com/devituz/lagodev/schema"
)

func init() {
    migrations.Register(migrations.Define("2026_01_01_000001_create_users_table",
        func(ctx *migrations.Context) error {
            return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
                t.ID()
                t.String("name")
                t.String("email").Unique()
                t.Timestamps()
                t.SoftDeletes()
            }))
        },
        func(ctx *migrations.Context) error {
            return ctx.Schema(schema.DropIfExists("users"))
        },
    ))
}
```

## Filenames & order

Convention: timestamp prefix + snake_case name:

```
2026_01_01_000001_create_users_table.go
2026_01_02_140530_add_role_to_users_table.go
```

The runner sorts by `Name()` lexically — timestamp prefixes guarantee
chronological order across collaborators.

## Blueprint reference

```go
schema.Create("posts", func(t *schema.Blueprint) {
    // Primary key shortcuts
    t.ID()                          // big auto-increment "id"
    t.UUID("uid")

    // Strings
    t.String("name")                // VARCHAR(255)
    t.String("code", 32)            // VARCHAR(32)
    t.Char("country", 2)            // CHAR(2)
    t.Text("body")                  // TEXT
    t.LongText("legal")             // LONGTEXT
    t.Binary("blob")

    // Numerics
    t.Integer("age").Default(18)
    t.BigInteger("counter")
    t.SmallInteger("flags")
    t.TinyInteger("level")
    t.UnsignedInteger("votes")
    t.Float("ratio")
    t.Double("score")
    t.Decimal("price", 10, 2)

    // Booleans / JSON
    t.Boolean("active").Default(true)
    t.JSON("meta")
    t.JSONB("payload")             // postgres-native; falls back to JSON

    // Dates / times
    t.Date("birthday")
    t.DateTime("starts_at")
    t.Time("opens_at")
    t.Timestamp("published_at").Nullable()
    t.TimestampTz("scheduled_at")
    t.Timestamps()                  // created_at + updated_at
    t.SoftDeletes()                 // deleted_at

    // Enum / Set
    t.Enum("status", "draft", "published", "archived")
    t.Set("permissions", "read", "write", "admin")

    // Foreign keys
    t.ForeignId("user_id").
        Constrained("users").       // FK → users.id
        OnDelete("cascade")

    // Indexes
    t.Unique("email")
    t.AddIndex("created_at")
    t.Primary("id", "tenant_id")    // composite PK
})
```

### Modifiers on a column

```go
t.String("email").Unique()
t.String("phone").Nullable()
t.Boolean("is_admin").Default(false)
t.Integer("age").Default(18)
t.String("country").Index()
t.Integer("count").Unsigned()
t.String("legacy_id").Comment("imported from v1")
t.String("title").After("id")       // MySQL only
t.String("note").First()             // MySQL only
```

### Foreign key fluent API

```go
// shortest — infers referenced table from column name
t.ForeignId("user_id").Constrained()              // → users.id

// explicit table
t.ForeignId("user_id").Constrained("accounts").OnDelete("cascade")

// composite or non-id reference
t.ForeignId("tenant_id").
    References("uuid").
    On("tenants").
    OnDelete("restrict")

t.ForeignId("manager_id").Cascade("employees")    // shortcut for cascade-on-delete
```

`OnDelete` / `OnUpdate` accept: `"cascade"`, `"restrict"`, `"set null"`, `"no action"`.

### Alter / drop / rename

```go
// Add a column
schema.Table("users", func(t *schema.Blueprint) {
    t.String("phone").Nullable()
})

// Drop a column
schema.Table("users", func(t *schema.Blueprint) {
    t.DropColumn("phone")
})

// Drop the whole table
schema.DropIfExists("legacy_records")

// Rename
schema.Rename("old_name", "new_name")
```

## Running migrations

```bash
lago migrate                           # apply all pending
lago migrate --pretend                 # print SQL, no execution
lago migrate --path 2026_01_01_...     # only one migration
lago migrate:status                    # tabular state
lago migrate:rollback                  # roll back the most recent batch
lago migrate:rollback --step=3
lago migrate:reset                     # roll back everything
lago migrate:refresh                   # reset + up
lago migrate:fresh                     # drop ALL tables + up
lago migrate:fresh --seed              # fresh + run all seeders
lago migrate:step --n=2                # apply at most 2
```

## Programmatic use

```go
migrator := migrations.New(conn, nil, migrations.Options{
    Logger:           appLogger,
    Pretend:          false,
    AllowOutOfOrder:  false,
    EnforceChecksums: true,
    LockTimeout:      30 * time.Second,
})

applied, err := migrator.Up(ctx)        // []string of newly-applied names
rolled, err  := migrator.Rollback(ctx, 0)
rows, err    := migrator.Status(ctx)    // []StatusRow
```

## Safety guarantees

- **Transactional** — each migration runs inside its own transaction; a
  partial failure leaves the schema untouched.
- **Locked** — the runner acquires `pg_advisory_lock` on Postgres, or a
  row in `migrations_lock` elsewhere, so two simultaneous deploys can't
  collide.
- **Checksum-verified** — the runner records SHA-256 of the emitted SQL.
  If you edit an already-applied migration, the next run refuses to
  proceed (under `EnforceChecksums`).
- **Out-of-order guard** — if migration `B` was applied before `A`'s
  timestamp, the runner errors instead of applying `A` silently. Set
  `AllowOutOfOrder: true` to override.
- **Preview** — `--pretend` exercises every code path except the actual
  exec, so you can paste the SQL into a code review.

## Custom drivers

Adding a new SQL dialect is a `Grammar` implementation + a `DriverFactory`
registered in init():

```go
func init() {
    database.Register("cockroach", func(dsn string) (*sql.DB, database.Grammar, error) {
        db, err := sql.Open("postgres", dsn)
        if err != nil {
            return nil, nil, err
        }
        return db, postgres.Grammar{}, nil  // CRDB is wire-compatible
    })
}
```
