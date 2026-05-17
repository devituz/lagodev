# ORM cookbook

## Defining a model

```go
import "github.com/devituz/lagodev/orm"

type User struct {
    orm.Model            // ID, CreatedAt, UpdatedAt, DeletedAt
    Name    string
    Email   string       `column:"email" orm:"unique"`
    IsAdmin bool         `column:"is_admin"`
    Meta    any          `column:"meta" orm:"cast:json"`
}
```

`orm.Model` embeds the canonical timestamp + primary-key fields. Use the
lighter `orm.Timestamps` if you don't want soft deletes.

### Struct tags

| Tag                   | Purpose                                              |
|-----------------------|------------------------------------------------------|
| `column:"name"`       | Map a field to a specific column (default: snake_case) |
| `column:"-"`          | Skip the field entirely (don't read/write)          |
| `orm:"primary"`       | Mark a field as primary key (default: `ID`)         |
| `orm:"autoincrement"` | Mark an integer primary key auto-incrementing       |
| `orm:"nullable"`      | Allow NULL                                          |
| `orm:"hidden"`        | Hide from `ToMap()` serialization                   |
| `orm:"fillable"`      | Allow mass-assignment                               |
| `orm:"cast:NAME"`     | Apply a registered cast (json, bool, int, date, …)  |
| `orm:"default:VALUE"` | Document a default (for schema generation)          |

### Overriding the table name

```go
func (User) TableName() string { return "app_users" }
```

## Querying — `orm.Query[T]`

`orm.Query[T](conn)` returns a typed `*Builder[T]`.

```go
import "github.com/devituz/lagodev/orm"

// Simple list
var users []User
err := orm.Query[User](conn).Get(ctx, &users)

// Filters
err := orm.Query[User](conn).
    Where("is_admin", "=", true).
    Where("created_at", ">", "2026-01-01").
    OrderBy("id", "desc").
    Limit(20).
    Get(ctx, &users)

// First (returns ErrNotFound when missing)
ada, err := orm.Query[User](conn).
    Where("email", "=", "ada@example.com").
    First(ctx)

// Find by PK
u, err := orm.Query[User](conn).Find(ctx, 42)

// Pluck a single column
emails, err := orm.Pluck[User, string](ctx,
    orm.Query[User](conn).Where("is_admin", "=", true),
    "email")
```

### Aggregations

```go
n, err     := orm.Query[User](conn).Count(ctx)
exists, _  := orm.Query[User](conn).Where("email", "=", e).Exists(ctx)
total, _   := orm.Query[User](conn).QB().Sum(ctx, "balance")
avg, _     := orm.Query[User](conn).QB().Avg(ctx, "score")
```

### Soft deletes

If your model embeds `orm.Model` and your table has a `deleted_at`
column, the default scope hides deleted rows:

```go
var live []User
_ = orm.Query[User](conn).Get(ctx, &live) // deleted_at IS NULL

// Include trashed
_ = orm.Query[User](conn).WithTrashed().Get(ctx, &allIncludingDeleted)

// Only trashed
_ = orm.Query[User](conn).OnlyTrashed().Get(ctx, &trashed)
```

### Operators

`Where("col", op, value)` accepts `=`, `<>`, `<`, `<=`, `>`, `>=`,
`like`, `ilike`. Use the dedicated methods for everything else:

```go
.WhereIn("id", []int{1, 2, 3})
.WhereNotIn("status", []string{"banned", "deleted"})
.WhereNull("verified_at")
.WhereNotNull("verified_at")
.WhereBetween("age", 18, 65)
.WhereRaw("LOWER(name) = LOWER(?)", "Ada")
```

### Nested groups

```go
orm.Query[User](conn).
    Where("active", "=", true).
    Where(func(q *query.Builder) {
        q.Where("role", "=", "admin").OrWhere("role", "=", "owner")
    }).
    Get(ctx, &out)
```

Produces `WHERE active = ? AND (role = ? OR role = ?)`.

## Saving — `orm.Save`

`orm.Save(ctx, conn, &model)` is the single entry point:

- inserts when `model.ID == 0`
- updates otherwise
- populates `CreatedAt`/`UpdatedAt` automatically (in `conn.Location()`)
- fires `BeforeSave`/`BeforeCreate`/`AfterCreate`/`AfterSave` hooks

```go
u := &User{Name: "Ada", Email: "ada@example.com"}
_ = orm.Save(ctx, conn, u)   // INSERT, u.ID now non-zero

u.Name = "Augusta"
_ = orm.Save(ctx, conn, u)   // UPDATE
```

## Deleting

```go
orm.Delete(ctx, conn, &u)         // soft-delete if available, else hard
orm.ForceDelete(ctx, conn, &u)    // always hard-delete
orm.Restore(ctx, conn, &u)        // unset deleted_at
```

## Hooks

Implement any subset of the hook interfaces on your model:

```go
type User struct { orm.Model; Email string }

func (u *User) BeforeCreate(*orm.HookContext) error {
    u.Email = strings.ToLower(u.Email)
    return nil
}

func (u *User) AfterCreate(ctx *orm.HookContext) error {
    return notifyWelcomeEmail(ctx.Ctx, u)
}
```

Available: `BeforeCreate`, `AfterCreate`, `BeforeUpdate`, `AfterUpdate`,
`BeforeSave`, `AfterSave`, `BeforeDelete`, `AfterDelete`, `AfterFind`.

## Casts

`orm:"cast:NAME"` runs a `casts.Cast` on the value when reading from the
database and writing back. Built-in casts: `json`, `jsonb`, `bool`,
`boolean`, `int`, `integer`, `date`.

Register custom casts:

```go
import "github.com/devituz/lagodev/casts"

casts.Register("encrypted", encryptedCast{key: getKey()})
```

## Transactions

```go
err := conn.Transaction(ctx, func(tx *database.Tx) error {
    if err := orm.Query[Account](conn).WithTx(tx).
        Where("id", "=", from).Update(ctx, map[string]any{"balance": -100}); err != nil {
        return err
    }
    return orm.Query[Account](conn).WithTx(tx).
        Where("id", "=", to).Update(ctx, map[string]any{"balance": +100})
})
```

The closure receives a `*Tx` that satisfies `database.Executor` — pass it
to `WithTx()` on a query builder, or call its methods directly. Roll-back
happens automatically if the closure returns non-nil.

### Savepoints

```go
conn.Transaction(ctx, func(tx *database.Tx) error {
    _ = tx.Savepoint(ctx, "sp1")
    if err := dodgyOperation(tx); err != nil {
        _ = tx.RollbackTo(ctx, "sp1")
    }
    return nil
})
```

## Raw access

When the builder isn't enough, drop to `*sql.DB`:

```go
rows, err := conn.QueryContext(ctx, "SELECT id, complex(:filter) FROM x", args...)
```

`conn.ExecContext` / `conn.QueryRowContext` round out the API. SQL
logging picks them up automatically when `DB_LOG_QUERIES=true`.
