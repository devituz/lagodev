# API resources & collections

Two small, dependency-free packages sit between your domain models and the
JSON you send over the wire:

- **`resource/`** — an API-resource / serializer layer (the Laravel API
  Resource / Django REST serializer equivalent). It describes the
  model-to-JSON mapping once and renders it consistently for single objects,
  lists, and paginated pages.
- **`collection/`** — a generic, mostly-immutable wrapper over Go slices
  (`map`, `filter`, `reduce`, `group by`, …) for shaping data before it
  reaches a resource.

Both are leaf packages: `resource` imports only `orm` (for `Paginator`), and
`collection` imports only the standard library. Neither imports `web`, so
there is no import cycle and you can return their output straight from a
`*web.Context` handler.

## Overview

A **resource** is anything that implements:

```go
type Resource[Model any] interface {
    Transform(Model) any
}
```

`Transform` receives one model and returns its serialisable shape — normally a
`resource.Fields` map, but any value `encoding/json` can marshal (including a
typed DTO struct) works. Wrap a plain function with `resource.Func` to define a
resource inline.

## Quick start — model → JSON resource

```go
import "github.com/devituz/lagodev/resource"

type User struct {
    ID        int
    Name      string
    Email     string
    Password  string // never serialised
    Admin     bool
    AvatarURL string
}

var UserResource = resource.Func(func(u User) any {
    return resource.Fields{
        "id":     u.ID,
        "name":   u.Name,
        "email":  u.Email,
        "avatar": u.AvatarURL, // computed / renamed field
    }.
        When(u.Admin, "is_admin", true) // conditional: only admins carry it
})
```

`Password` is simply never added to the map, so it can never leak. Render a
single model and return it from a handler:

```go
import (
    "github.com/devituz/lagodev/resource"
    "github.com/devituz/lagodev/web"
)

func (h *Handler) Show(c *web.Context) (any, error) {
    u, err := h.users.Find(c.Ctx(), id)
    if err != nil {
        return nil, err
    }
    return resource.Item(u, UserResource), nil
}
```

`resource.Item(u, UserResource)` returns the transformed value itself, with no
envelope, ready for the web layer to encode:

```json
{ "id": 1, "name": "Ada", "email": "ada@example.com", "avatar": "https://…" }
```

`Item` is nil-resource-safe: `resource.Item(u, nil)` passes the model through
unchanged.

## Single vs collection resources

The same resource drives single and list rendering — you choose the wrapper:

| Helper                            | Output shape                                   |
|-----------------------------------|------------------------------------------------|
| `Item(m, r)`                      | the transformed value (no envelope)            |
| `Collection(models, r)`           | `{"data": [...]}`                              |
| `Paginated(p, r)`                 | `{"data": [...], "meta": {...}}`               |

```go
func (h *Handler) Index(c *web.Context) (any, error) {
    users, err := h.users.All(c.Ctx())
    if err != nil {
        return nil, err
    }
    return resource.Collection(users, UserResource), nil
}
```

`Collection` returns a `CollectionResponse`:

```go
type CollectionResponse struct {
    Data []any          `json:"data"`
    Meta map[string]any `json:"meta,omitempty"`
}
```

A `nil` slice renders as `{"data": []}` — never `null`. Attach top-level meta
with the chainable `WithMeta`:

```go
return resource.
    Collection(users, UserResource).
    WithMeta(map[string]any{"count": len(users)}), nil
```

## Field selection & relations

`resource.Fields` is a `map[string]any` with chainable, nil-safe helpers. Every
method returns `Fields` so a resource body reads as a single expression, and
calling on a `nil` Fields lazily allocates (the zero value is usable).

| Method                                | Effect                                                          |
|---------------------------------------|----------------------------------------------------------------|
| `Set(key, val)`                       | add / overwrite a key                                          |
| `When(cond, key, val)`                | set `key` only when `cond` is true (omit, not null)            |
| `WhenFunc(cond, key, func() any)`     | like `When`, but the value is computed lazily                 |
| `Unless(cond, key, val)`              | inverse of `When` (set only when `cond` is false)             |
| `Merge(other Fields)`                 | splice in another block (`other` wins on collisions)          |
| `Only(keys...)`                       | whitelist — keep just the listed keys                         |
| `Except(keys...)`                     | blacklist — drop the listed keys                              |
| `Rename(from, to)`                    | move a value to a new key                                     |
| `OmitEmpty(keys...)`                  | drop keys whose value is a Go zero value (opt-in `omitempty`) |

`Only`, `Except`, and `OmitEmpty` (with arguments) return a fresh `Fields`
without modifying the receiver; the others mutate-and-return for chaining.

```go
var UserResource = resource.Func(func(u User) any {
    return resource.Fields{
        "id":         u.ID,
        "name":       u.Name,
        "email":      u.Email,
        "last_login": u.LastLogin, // may be zero
    }.
        WhenFunc(u.Admin, "audit_log", func() any { return loadAudit(u.ID) }).
        OmitEmpty("last_login")
})
```

### Embedding related models

`Embed` and `EmbedMany` render related models through their own resource so you
can nest relations under a key:

```go
var PostResource = resource.Func(func(p Post) any {
    return resource.Fields{
        "id":      p.ID,
        "title":   p.Title,
        "author":  resource.Embed(p.Author, UserResource),     // single → object or null
        "tags":    resource.EmbedMany(p.Tags, TagResource),    // slice → [] never null
    }
})
```

- `Embed(m, r)` returns `nil` when `m` is a nil pointer/interface/slice/map, so
  a missing relation renders as JSON `null` instead of panicking.
- `EmbedMany(models, r)` always returns a non-`nil` `[]any`, so an empty
  relation renders as `[]`.

## Pagination & meta

`Paginated` renders an `*orm.Paginator[T]` — the result of
`orm.Query[T](conn).Paginate(ctx, page, perPage)` — deriving the `meta` block
straight from the paginator:

```go
func (h *Handler) Index(c *web.Context) (any, error) {
    page, err := orm.Query[User](h.conn).
        OrderBy("id", "desc").
        Paginate(c.Ctx(), 2, 15)
    if err != nil {
        return nil, err
    }
    return resource.Paginated(page, UserResource), nil
}
```

```json
{
  "data": [ { "id": 16, "name": "…" }, … ],
  "meta": { "total": 137, "page": 2, "per_page": 15, "last_page": 10, "has_more": true }
}
```

The envelope types:

```go
type Meta struct {
    Total    int64 `json:"total"`
    Page     int   `json:"page"`
    PerPage  int   `json:"per_page"`
    LastPage int   `json:"last_page"`
    HasMore  bool  `json:"has_more"`
}

type PaginatedResponse struct {
    Data  []any          `json:"data"`
    Meta  Meta           `json:"meta"`
    Links *Links         `json:"links,omitempty"`
    With  map[string]any `json:"with,omitempty"`
}
```

A `nil` paginator yields an empty page with zero-value meta (never a panic).

### Pager links and extra meta

`WithLinks(base)` builds `first` / `last` / `prev` / `next` URLs from a base
path using a `?page=N` query (it picks `?` or `&` automatically). `prev` is
omitted on the first page; `next` is omitted when `has_more` is false. Both are
chainable:

```go
return resource.
    Paginated(page, UserResource).
    WithLinks(c.Request.URL.Path).
    WithMeta(map[string]any{"server_time": time.Now().Unix()}), nil
```

```go
type Links struct {
    First string `json:"first,omitempty"`
    Last  string `json:"last,omitempty"`
    Prev  string `json:"prev,omitempty"`
    Next  string `json:"next,omitempty"`
}
```

`PaginatedResponse.WithMeta` merges into a top-level `with` block (kept separate
from the derived `meta`, which is owned by the paginator).

## Generic collection helpers

`collection.Collection[T]` wraps a slice with chainable, mostly-immutable
operations. Reshaping methods copy the backing slice, so the original is never
mutated. The zero value is an empty, ready-to-use collection.

```go
import "github.com/devituz/lagodev/collection"

even := collection.New(1, 2, 3, 4, 5, 6).
    Filter(func(n int) bool { return n%2 == 0 }).
    Reverse().
    Take(2).
    All() // []int{6, 4}
```

Constructors: `New(items...)`, `From(slice)` (copies the input), `Collect(slice)`
(alias of `From`), and `FromSeq(iter.Seq[T])`.

### Same-type methods (chainable)

Because Go methods cannot introduce new type parameters, operations that keep
the element type `T` are **methods** and chain:

| Method                          | Returns        | Notes                                       |
|---------------------------------|----------------|---------------------------------------------|
| `Filter(keep func(T) bool)`     | `Collection[T]`| keep matching elements                      |
| `Reject(drop func(T) bool)`     | `Collection[T]`| inverse of `Filter`                         |
| `Map(fn func(T) T)`             | `Collection[T]`| same-type map (use free `Map` to change type) |
| `Sort(less func(a, b T) bool)`  | `Collection[T]`| stable sort                                 |
| `SortBy(keyFn func(T) int)`     | `Collection[T]`| sort ascending by int key                   |
| `Reverse()`                     | `Collection[T]`|                                             |
| `Take(n)` / `Skip(n)`           | `Collection[T]`| negative `Take` takes from the end          |
| `Slicing(start, length)`        | `Collection[T]`| half-open range, negative start from end    |
| `UniqueBy(keyFn func(T) any)`   | `Collection[T]`| dedupe by derived key                       |
| `Push(items...)` / `Prepend(items...)` | `Collection[T]` | the only "mutators" — still return a copy |
| `Merge(other)`                  | `Collection[T]`| append another collection                   |

Terminal / inspection methods:

```go
c.All()                 // []T copy (Slice is an alias)
c.Len()                 // int (Count is an alias)
c.IsEmpty()             // bool (IsNotEmpty is the inverse)
c.Get(i)                // (T, bool)
c.First()               // (T, bool)
c.Last()                // (T, bool)
c.FirstWhere(pred)      // (T, bool) — first matching element
c.ContainsBy(pred)      // bool
c.Partition(pred)       // (yes, no Collection[T])
c.Chunk(size)           // [][]T
c.Each(func(i int, v T)){…}  // observe in place, returns receiver
c.Tap(func(Collection[T])){…} // side effect on the whole collection
c.Seq()  / c.Seq2()     // iter.Seq[T] / iter.Seq2[int, T] for range-over-func
```

`Each` and `Tap` are the only methods that iterate without copying; everything
else leaves the receiver untouched.

### Type-changing free functions

Operations whose result element type differs from `T` are **package functions**:

```go
type User struct {
    Name string
    Age  int
}
users := collection.New(
    User{"Ada", 36}, User{"Linus", 54}, User{"Grace", 36},
)

// map User -> string (element type changes)
names := collection.Map(users, func(u User) string { return u.Name }).All()

// group / index
byAge := collection.GroupBy(users, func(u User) int { return u.Age }) // map[int]Collection[User]
byName := collection.KeyBy(users, func(u User) string { return u.Name }) // map[string]User

// fold to a single value
totalAge := collection.Reduce(users, 0, func(acc int, u User) int { return acc + u.Age })
```

| Function                              | Result                          |
|---------------------------------------|---------------------------------|
| `Map(c, fn func(T) U)`                | `Collection[U]`                 |
| `FlatMap(c, fn func(T) []U)`          | `Collection[U]` (concatenated)  |
| `Pluck(c, fn func(T) U)`              | `Collection[U]` (alias of `Map`)|
| `Reduce(c, init U, fn)`               | `U`                             |
| `GroupBy(c, keyFn func(T) K)`         | `map[K]Collection[T]`           |
| `KeyBy(c, keyFn func(T) K)`           | `map[K]T` (last wins)           |
| `Associate(c, fn func(T) (K, V))`     | `map[K]V`                       |
| `ChunkInto(c, size)`                  | `Collection[Collection[T]]`     |
| `SortOrdered(c)`                      | `Collection[T]` (natural order) |
| `SortByKey(c, keyFn func(T) K)`       | `Collection[T]` (ordered key)   |
| `Unique(c)`                           | `Collection[T]` (comparable T)  |
| `Contains(c, target)`                 | `bool` (comparable T)           |
| `Sum(c)` / `SumBy(c, fn)`             | numeric total                   |
| `Avg(c)`                              | `(float64, bool)`               |
| `Min(c)` / `Max(c)`                   | `(T, bool)` (ordered T)         |

`Sum`, `SumBy`, and `Avg` are constrained to `collection.Number` (any
`~int`/`~uint`/`~float` kind); `Min`, `Max`, `SortOrdered`, and `SortByKey`'s
key require `cmp.Ordered`; `Unique` and `Contains` require `comparable`.

### Collections feeding resources

The two packages compose naturally: shape with `collection`, render with
`resource`.

```go
active := collection.From(users).
    Filter(func(u User) bool { return u.Active }).
    SortBy(func(u User) int { return u.ID }).
    All()

return resource.Collection(active, UserResource), nil
```

## Production notes

- **No secrets by construction.** A field that is never added to `Fields`
  cannot leak. Prefer omitting sensitive fields outright over filtering them
  out later — `Password` in the quick-start example is simply never mapped.
- **Empty ≠ null.** `Collection`, `Paginated`, and `EmbedMany` always emit a
  non-nil `[]` for empty input, so clients never special-case `null` arrays.
- **`When` omits, it does not nullify.** A field guarded by `When`/`Unless` is
  absent from the JSON when the condition fails — not present as `null`. Use
  this for role-gated fields rather than sending `null` to every client.
- **Resources are stateless and reusable.** Define each resource once as a
  package-level `var` (as with `UserResource`) and share it across handlers;
  `Func` values carry no per-request state.
- **Immutability has a copy cost.** Every reshaping `collection` method copies
  its backing slice. For hot paths over very large slices, prefer a single
  pass (one `Filter` + free `Map`) over long chains, or drop to a plain
  `for` loop. `Each`/`Tap` iterate without copying when you only need to
  observe.
- **Determinism.** `Fields` marshals like any `map[string]any` (keys sorted by
  `encoding/json`), so resource output is stable across runs and easy to
  snapshot-test.
- **Pagination bounds.** `Paginate` clamps page/per-page bounds in `orm`;
  `Paginated` tolerates a `nil` paginator by emitting an empty page, so a
  not-found page never panics the handler.
