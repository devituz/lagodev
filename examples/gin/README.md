# Gin example

REST API for `User` using [Gin](https://gin-gonic.com/) and lagodev.

```bash
cd examples/gin
go mod tidy
go run .
```

Endpoints:

| Method | Path           | Action          |
|--------|----------------|-----------------|
| GET    | `/users`       | list users      |
| GET    | `/users/:id`   | fetch one       |
| POST   | `/users`       | create          |
| PATCH  | `/users/:id`   | update          |
| DELETE | `/users/:id`   | soft-delete     |

### What this example shows

- The `UserService` struct is **framework-agnostic** — it returns
  `(*User, error)`. The Gin handlers are thin wrappers that call the
  service and serialize the result.
- The same service plugs into Fiber/Echo/Chi/net/http unchanged. See the
  sibling directories for proof.
- Migrations are registered in `init()` and applied at startup, so the
  binary is self-contained (no external `artisan migrate` step needed).
- Soft deletes are on by default; `DELETE /users/:id` sets `deleted_at`
  instead of removing the row.

### Want it generated for you?

```bash
go install github.com/devituz/lagodev/cmd/lago@latest
lago make:model User -mfsc --fields="name:string,email:string:unique"
```

That produces the same layout (`models/`, `migrations/`, `services/`,
`controllers/`) — drop the controllers next to a Gin router and you're done.
