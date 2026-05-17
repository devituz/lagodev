# Factories & seeders

## Factories

A factory is a generic `*factory.Factory[T]` constructed from a
definition function that returns a fresh `T`:

```go
import (
    "github.com/devituz/lagodev/factory"
    "myapp/models"
)

func UserFactory(conn *database.Connection) *factory.Factory[models.User] {
    return factory.New(conn, func(f *factory.Faker) models.User {
        return models.User{
            Name:    f.Name(),
            Email:   f.Email(),
            IsAdmin: false,
        }
    })
}
```

### Building vs. persisting

```go
u  := UserFactory(conn).MakeOne()              // build, don't insert
us := UserFactory(conn).Count(5).Make()        // []User without IDs

u,   _ := UserFactory(conn).CreateOne(ctx)     // INSERT, ID populated
us,  _ := UserFactory(conn).Count(100).Create(ctx)
```

### States

A *state* is a function that mutates the model in place. Use it for
named variants:

```go
adminState := func(f *factory.Faker, u *models.User) { u.IsAdmin = true }

admins, _ := UserFactory(conn).
    State(adminState).
    Count(3).
    Create(ctx)
```

States compose — chaining `.State(adminState).State(verifiedState)`
applies both, in order.

### One-off overrides

`Set` is sugar for an inline closure that wants to pin a specific field:

```go
UserFactory(conn).
    Set(func(u *models.User) { u.TenantID = 7 }).
    Count(10).
    Create(ctx)
```

### Hooks

```go
UserFactory(conn).
    AfterMake(func(u *models.User) { u.Email = strings.ToLower(u.Email) }).
    AfterCreate(func(ctx context.Context, u *models.User) error {
        return enqueueWelcomeEmail(ctx, u.ID)
    }).
    Count(50).
    Create(ctx)
```

### Deterministic data

For reproducible test fixtures, seed the faker:

```go
fk := factory.NewSeeded(42)
// ... use fk directly, or pass it into a Factory via a custom builder
```

## Seeders

A seeder is a named runner. Implement the `Seeder` interface (and
optionally `Dependent`):

```go
type UserSeeder struct{}

func (UserSeeder) Name() string             { return "UserSeeder" }
func (UserSeeder) Dependencies() []string   { return nil }
func (UserSeeder) Run(ctx context.Context, conn *database.Connection) error {
    _, err := factories.UserFactory(conn).Count(20).Create(ctx)
    return err
}

func init() { seeder.Register(&UserSeeder{}) }
```

Then either run all seeders:

```bash
lago db:seed
```

Or a single one (its declared dependencies still run first):

```bash
lago db:seed UserSeeder
lago db:seed --class UserSeeder          # Laravel-style alias
```

### Dependency ordering

```go
type PostSeeder struct{}

func (PostSeeder) Name() string             { return "PostSeeder" }
func (PostSeeder) Dependencies() []string   { return []string{"UserSeeder"} }
func (PostSeeder) Run(ctx context.Context, conn *database.Connection) error { ... }
```

The runner topologically sorts the dependency graph; cycles fail loudly.

### Transactional seeders

```bash
lago db:seed --transactional
```

Each seeder runs inside its own transaction; failure rolls the seeder
back without affecting the previously-run ones.

### Programmatic

```go
runner := seeder.NewRunner(conn, nil, seeder.Options{
    Transactional: true,
    Logger:        appLogger,
    Only:          []string{"UserSeeder", "PostSeeder"},
})
_ = runner.Run(ctx)
```

## Faker reference

`factory.Faker` wraps `brianvoe/gofakeit`. The pre-baked methods:

| Method                 | Returns                              |
|------------------------|--------------------------------------|
| `Name()` / `FirstName()` / `LastName()` | Random person name      |
| `Email()` / `UserName()`               | Email / username         |
| `Phone()`                              | Phone number             |
| `URL()` / `UUID()`                     | URL / UUID v4            |
| `Address()` / `City()` / `Country()`   | Geographic strings       |
| `Password()`                           | Strong 16-char password  |
| `Word()` / `Sentence(n)`               | Short text               |
| `Paragraph(p, s, w, sep)`              | Lorem-ipsum-style block  |
| `Bool()`                               | Random bool              |
| `IntRange(lo, hi)`                     | Bounded integer          |
| `Float64Range(lo, hi)`                 | Bounded float            |
| `PickString([]string{...})`            | One of the slice values  |
| `Raw()`                                | Underlying `*gofakeit.Faker` for anything else |

When `--fields` includes well-known column names (`email`, `phone`,
`title`, `body`, …), `lago make:factory` wires the matching faker method
automatically:

```bash
lago make:factory PostFactory --model=Post \
    --fields="title:string,body:text,published_at:datetime"

# Generates:
#   Title:       f.Sentence(5),
#   Body:        f.Paragraph(2, 3, 5, " "),
#   PublishedAt: time.Now(),
```
