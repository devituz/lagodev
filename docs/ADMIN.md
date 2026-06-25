# Admin panel

The `admin` package is a Django-admin-style auto-CRUD panel built entirely
from struct reflection. You register your domain models against a `Panel`,
and it serves a self-contained web UI — a resource index, paginated and
searchable list views, and create/edit/delete forms — over a plain
`http.Handler` you mount wherever you like (conventionally `/admin/`).

It is stdlib-only (`net/http`, `html/template`, `reflect`) and never touches
a database directly. Persistence lives behind the `DataSource` interface, so
the same UI runs against an in-memory slice (`SliceSource`, shipped), an
orm-backed adapter you write, or any other store.

## Quick start

Register a model against the shipped in-memory source and mount the handler:

```go
package main

import (
    "log"
    "net/http"

    "github.com/devituz/lagodev/admin"
)

type User struct {
    ID    uint64 `column:"id" orm:"primary"`
    Name  string `column:"name"`
    Email string `column:"email"`
}

func main() {
    // In-memory data source (great for demos/tests).
    src := admin.NewSliceSource("id")
    src.Seed(
        map[string]any{"id": 1, "name": "Ada", "email": "ada@example.com"},
        map[string]any{"id": 2, "name": "Linus", "email": "linus@example.com"},
    )

    p := admin.New(admin.WithTitle("Acme Admin"))
    p.Register(User{}, admin.Resource{
        Source: src,
        Search: []string{"name", "email"},
    })

    mux := http.NewServeMux()
    mux.Handle("/admin/", http.StripPrefix("/admin", p.Handler()))

    log.Println("admin on http://localhost:8080/admin/")
    log.Fatal(http.ListenAndServe(":8080", mux))
}
```

`admin.New(opts...)` builds an empty `Panel`. `Register` adds a model: pass a
zero/sample value (`User{}` or `&User{}`) whose struct tags drive field
discovery, plus a `Resource` carrying the `DataSource` and any overrides.
`Register` **panics** on misuse (non-struct model, missing `Source`, duplicate
slug) — these are startup-time programmer errors, matching the framework's
other registries.

`Handler()` returns the `http.Handler`. Mount it under the panel's base path
(default `/admin`) with `http.StripPrefix` so internal link building lines up.

### Routes

Relative to the mount point, `Handler()` serves:

```
GET  /                         resource index
GET  /{resource}               paginated, searchable list
GET  /{resource}/new           create form
POST /{resource}/new           create
GET  /{resource}/{id}          edit form
POST /{resource}/{id}          update
POST /{resource}/{id}/delete   delete (soft-delete aware)
```

`{resource}` is the model's slug (default: snake_case of the type name, e.g.
`User` -> `user`).

## Resource configuration

`Resource` declares how one model is exposed. Only `Source` is required;
everything else defaults from reflected struct tags.

```go
p.Register(BlogPost{}, admin.Resource{
    Source:   postSource,                       // required: persistence backend
    Slug:     "posts",                           // URL segment (default: snake_case type)
    Label:    "Blog Posts",                      // UI label (default: humanized type)
    Columns:  []string{"id", "title", "published"}, // list-table columns, in order
    Editable: []string{"title", "body", "published"}, // form inputs
    Search:   []string{"title", "body"},         // free-text search fields
    Filters:  []string{"published"},             // exact-match filter inputs
    PerPage:  25,                                 // list page size (default 20)
})
```

| Field      | Default when zero                                                   |
|------------|---------------------------------------------------------------------|
| `Slug`     | `inflect.Snake(TypeName)` — `BlogPost` -> `blog_post`               |
| `Label`    | Humanized type name — `BlogPost` -> `Blog Post`                     |
| `Columns`  | Every non-relation field                                            |
| `Editable` | Every field except the PK and managed timestamps                   |
| `Search`   | Empty — search box disabled                                         |
| `Filters`  | Empty — filtering disabled                                          |
| `PerPage`  | `admin.DefaultPerPage` (20)                                         |

### Field discovery

Columns come from reflecting the struct, mirroring the ORM's tag conventions
(see [ORM.md](ORM.md)):

| Tag / convention      | Effect                                                  |
|-----------------------|--------------------------------------------------------|
| `column:"name"`       | Storage/data-source key (default: snake_case of field) |
| `column:"-"`          | Skip the field entirely                                |
| `orm:"-"`             | Skip the field entirely                                |
| `orm:"primary"`       | Mark as primary key (the row identifier)               |

Well-known fields are recognised by name automatically: `ID` is the primary
key; `CreatedAt` / `UpdatedAt` are read-only timestamps (shown but not
editable); a `DeletedAt` field (`time.Time` or `*time.Time`) marks the model
soft-delete aware. Embedded anonymous structs — such as a shared `orm.Model`
mixin — are flattened, so the standard timestamp/PK fields are picked up.

Each discovered `Field` carries a `Kind` that classifies its form input:
`text`, `number`, `bool`, or `datetime`. Slice or pointer-to-struct fields are
treated as relations and hidden from the list table by default.

## CRUD, list, filters, pagination

The list view (`GET /{resource}`) reads three query parameters:

```
GET /admin/user?q=ada&filter_published=true&page=2
```

- `q` — free-text search, matched case-insensitively as a substring against
  the fields named in `Resource.Search`. No `Search` fields means the box is
  hidden.
- `filter_<column>` — exact-match filter for each column in `Resource.Filters`.
  Multiple filters are AND-ed.
- `page` — 1-indexed page number; the source clamps out-of-range values.

The panel passes these to the source as `ListParams`:

```go
type ListParams struct {
    Page             int               // 1-indexed
    PerPage          int               // page size
    Search           string            // free-text term
    SearchFields     []string          // columns Search matches against
    Filters          map[string]string // exact-match, AND-ed
    IncludeDeleted   bool              // include soft-deleted rows
    SoftDeleteColumn string            // "" when not soft-delete aware
}
```

Create and update submit the form as `POST`, coercing each editable field to a
native Go type by its `Kind` (checkbox -> `bool`, numeric input -> `int64` /
`float64`, otherwise `string`) before handing the `map[string]any` to the
source. Delete is a `POST` to `/{resource}/{id}/delete`. When the model is
soft-delete aware, the source decides whether to stamp the `DeletedAt` column
or physically remove the row.

### Writing a DataSource

The panel programs against `DataSource`. `SliceSource` is the reference
in-memory implementation; for live persistence you supply your own. Rows are
flat `map[string]any` keyed by column name (the same keys reflection derives).

```go
type DataSource interface {
    List(ctx context.Context, params ListParams) (rows []map[string]any, total int, err error)
    Get(ctx context.Context, id string) (map[string]any, error)
    Create(ctx context.Context, data map[string]any) (map[string]any, error)
    Update(ctx context.Context, id string, data map[string]any) (map[string]any, error)
    Delete(ctx context.Context, id string) error
}
```

`Get` / `Update` / `Delete` return `admin.ErrNotFound` when no row matches the
id; the panel translates that to HTTP 404. Implementations must be safe for
concurrent use.

An orm-backed adapter over `orm.Query[T]` (see [ORM.md](ORM.md)) looks like:

```go
type userSource struct{ conn *database.Connection }

func (s userSource) List(ctx context.Context, p admin.ListParams) ([]map[string]any, int, error) {
    q := orm.Query[User](s.conn)
    for col, val := range p.Filters {
        q = q.Where(col, "=", val)
    }
    if p.Search != "" {
        for _, f := range p.SearchFields {
            q = q.OrWhere(f, "ilike", "%"+p.Search+"%")
        }
    }
    total, err := q.Count(ctx)
    if err != nil {
        return nil, 0, err
    }
    var users []User
    err = q.OrderBy("id", "desc").
        Limit(p.PerPage).
        Offset((p.Page - 1) * p.PerPage).
        Get(ctx, &users)
    if err != nil {
        return nil, 0, err
    }
    rows := make([]map[string]any, len(users))
    for i, u := range users {
        rows[i] = u.ToMap()
    }
    return rows, total, nil
}

func (s userSource) Get(ctx context.Context, id string) (map[string]any, error) {
    u, err := orm.Query[User](s.conn).Find(ctx, id)
    if errors.Is(err, orm.ErrNotFound) {
        return nil, admin.ErrNotFound
    }
    if err != nil {
        return nil, err
    }
    return u.ToMap(), nil
}

// Create / Update build a *User from data and call orm.Save; Delete calls
// orm.Delete (soft-delete aware when the model embeds orm.Model).
```

Map `orm.ErrNotFound` to `admin.ErrNotFound` so the panel renders 404 rather
than 500.

## Authorizing the panel

The panel ships no authentication. Gate it two ways — usually both.

**1. Front the mount with middleware.** Because `Handler()` is a plain
`http.Handler`, wrap it with any auth middleware before mounting:

```go
func requireAdmin(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !isAdminSession(r) {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        next.ServeHTTP(w, r)
    })
}

mux.Handle("/admin/", requireAdmin(http.StripPrefix("/admin", p.Handler())))
```

**2. Per-action RBAC with an `Authorizer`.** `WithAuthorizer` installs a hook
consulted before every action; returning `false` yields HTTP 403:

```go
type Authorizer func(r *http.Request, action, model string) bool
```

`action` is one of `admin.ActionList`, `ActionView`, `ActionCreate`,
`ActionUpdate`, `ActionDelete`. `model` is the resource slug (empty for the
top-level index).

```go
p := admin.New(
    admin.WithTitle("Acme Admin"),
    admin.WithAuthorizer(func(r *http.Request, action, model string) bool {
        u := currentUser(r)
        if u == nil || !u.IsAdmin {
            return false
        }
        // Only owners may delete the users resource.
        if model == "user" && action == admin.ActionDelete {
            return u.IsOwner
        }
        return true
    }),
)
```

A nil `Authorizer` allows everything.

## Customization

- **Title** — `admin.WithTitle("Acme Admin")` sets the header and page titles.
- **Mount path** — `admin.WithBasePath("/backoffice")` changes the prefix used
  to build internal links. Mount `Handler()` under the matching prefix:
  `http.StripPrefix("/backoffice", p.Handler())`.
- **Columns and inputs** — `Resource.Columns` and `Resource.Editable` pick and
  order which fields show in the list table and which render as form inputs.
- **Labels** — `Resource.Label` and `Resource.Slug` override the display name
  and URL segment per model.

The rendered HTML is a fixed, self-contained template set (no external assets,
no JS framework); customization is through configuration rather than template
overrides.

## Production notes

- **CSRF** — every mutating request (create/update/delete) is `POST`-only and
  guarded by a double-submit token (`lago_admin_csrf` cookie echoed in the
  `csrf` form field). Forms carry the token automatically. Drive the panel
  through its own forms; a bare scripted `POST` must replay both the cookie and
  the field.
- **Auto-escaping** — all output goes through `html/template`, so row values
  are escaped on render.
- **Always gate it** — there is no built-in login. Put the panel behind auth
  middleware and, for least privilege, an `Authorizer`. Never expose it on a
  public path unauthenticated.
- **Serve over TLS** — the CSRF cookie is `SameSite=Lax`; terminate TLS at your
  ingress/proxy and restrict the route to trusted networks where possible.
- **Register at startup** — register every resource during setup before serving.
  A `Panel` is safe for concurrent use after registration but registration
  itself is not concurrency-safe.
- **Honour `ctx`** — your `DataSource` should respect `ctx` cancellation and
  enforce sane `PerPage` bounds so a hostile `page`/filter query cannot exhaust
  the backend.

## See also

- [ORM.md](ORM.md) — query builder used to back a real `DataSource`.
- [GETTING_STARTED.md](GETTING_STARTED.md) — project scaffolding and the `web`
  server the panel mounts alongside.
