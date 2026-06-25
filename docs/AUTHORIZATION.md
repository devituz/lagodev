# Authorization

The `authz` package decides **what an authenticated user is allowed to
do**. Authentication (who the user *is*) lives in
[`auth`](FRAMEWORK_INTEGRATION.md); `authz` picks up where it leaves off.

It is modelled on Laravel's Gate facade, but fully type-safe via Go
generics: the gate is parameterised on your own user type, so there is no
`any`-typed `user.(*User)` dance in your policies.

Two complementary patterns:

| Pattern  | Best for                                              |
|----------|-------------------------------------------------------|
| **Gate** | ad-hoc abilities that don't map to one resource type (`manage-billing`, `view-dashboard`) |
| **Policy** | per-resource abilities grouped on one struct (`update`/`delete`/`view` of a `Post`) |

Both register against the same `*authz.Gate[U]` and are queried through
the same `Allows` / `Authorize` / `Check` methods.

## Quick start

```go
package main

import (
    "context"
    "fmt"

    "github.com/devituz/lagodev/authz"
)

type User struct {
    ID   uint64
    Role string
}

type Post struct {
    ID       uint64
    AuthorID uint64
}

func main() {
    g := authz.New[User]()

    // Gate: a free-standing ability.
    authz.Define(g, "manage-billing",
        func(_ context.Context, u User, _ any) bool {
            return u.Role == "admin"
        })

    // Policy: per-resource abilities.
    authz.Policy[Post](g, PostPolicy{})

    ctx := context.Background()
    ada := User{ID: 7, Role: "user"}
    post := Post{ID: 1, AuthorID: 7}

    ok, _ := g.Allows(ctx, "manage-billing", ada, nil)   // false (not admin)
    ok, _ = g.Allows(ctx, "update", ada, post)           // true  (owns it)
    fmt.Println(ok)
}

type PostPolicy struct{}

func (PostPolicy) Update(_ context.Context, u User, p Post) bool {
    return p.AuthorID == u.ID
}
```

`authz.New[User]()` returns a `*authz.Gate[User]`. Build it once at
startup, register every gate/policy, and treat it as read-mostly — the
gate is safe for concurrent use, so a single instance backs the whole
process.

## Gates

A gate is an ability name bound to a closure. Register with `Define`:

```go
func Define[U any, R any](
    g *authz.Gate[U],
    ability string,
    fn func(ctx context.Context, user U, resource R) bool,
)
```

The `R` type parameter lets the closure receive a typed resource without
a manual cast:

```go
// Resource-less gate — pass any for R, ignore it.
authz.Define(g, "view-dashboard", func(_ context.Context, u User, _ any) bool {
    return u.Role != "guest"
})

// Typed-resource gate.
authz.Define(g, "delete-post", func(_ context.Context, u User, p Post) bool {
    return p.AuthorID == u.ID
})
```

When you call `Allows(ctx, "delete-post", user, somePost)`, the gate
casts the `any` resource back to `R` (`Post`). **If the runtime resource
isn't assignable to `R`, the closure is not called and the check denies**
— a mismatched type never panics. Re-registering the same ability name
replaces the previous closure.

## Policies

A policy groups the abilities of a single resource type onto one struct.
Each exported method *is* an ability:

```go
type PostPolicy struct{}

// bool signature — simple allow/deny.
func (PostPolicy) Update(_ context.Context, u User, p Post) bool {
    return p.AuthorID == u.ID
}

// (bool, error) signature — surface a reason / infrastructure error.
func (PostPolicy) Delete(_ context.Context, u User, p Post) (bool, error) {
    if p.Draft {
        return false, errors.New("drafts cannot be deleted")
    }
    return p.AuthorID == u.ID, nil
}
```

Register once per resource type:

```go
func Policy[R any, U any](g *authz.Gate[U], policy any)
```

```go
authz.Policy[Post](g, PostPolicy{})
```

`R` is the **resource** type (`Post`); `U` is inferred from the gate.
Now ability names route to methods:

```go
ok, _   := g.Allows(ctx, "update", user, post)   // → PostPolicy.Update
ok, err := g.Allows(ctx, "delete", user, post)   // → PostPolicy.Delete
```

### Method matching rules

- **Case-insensitive, separator-insensitive.** `update`, `Update`,
  `UPDATE` all match `Update`. Hyphens and underscores are stripped, so
  `update_post` matches `UpdatePost`.
- **Signature-checked.** A method matches only if its parameters are
  `(context.Context, U, R)` and it returns `bool` or `(bool, error)`. A
  name match with the wrong shape is *ignored*, not invoked — the gate
  returns `ErrUnknownAbility` rather than panicking.
- **Pointer resources are dereferenced.** Passing `&post` to a policy
  registered for `Post` works: the gate unwraps the pointer before
  dispatch.

### Gate vs policy precedence

For a given ability name, a **gate closure wins over a policy method**.
If `Define(g, "update", …)` and `Policy[Post]` both define `update`, the
`Define` closure is consulted and the policy is skipped. Keep policy
ability names resource-specific (`update`) and gate names global
(`manage-billing`) to avoid surprises.

## Abilities & permissions (RBAC)

`authz` has no built-in "role" or "permission" table — roles are just
data on your user type, and you express RBAC inside the closures/methods.
The `Before` hook is the idiomatic place for a coarse,
role-based override:

```go
func Before[U any](
    g *authz.Gate[U],
    fn func(ctx context.Context, user U, ability string) bool,
)
```

```go
// Superadmins bypass every check.
authz.Before(g, func(_ context.Context, u User, _ string) bool {
    return u.Role == "superadmin"
})
```

If a `Before` hook returns `true`, the check **short-circuits to allow**
before any gate or policy runs. Register multiple hooks if you like; the
first to return `true` wins. Use it sparingly — a `Before` that allows
too broadly silently defeats every policy you wrote.

For fine-grained permission lists, store them on the user and read them
in the closure:

```go
type User struct {
    ID          uint64
    Role        string
    Permissions []string // e.g. ["posts.publish", "users.ban"]
}

authz.Define(g, "publish-post", func(_ context.Context, u User, _ any) bool {
    return slices.Contains(u.Permissions, "posts.publish")
})
```

## Checking: Allows, Authorize, Check, Denies

Four ways to ask the same question, differing only in the return shape:

```go
// (bool, error) — error is non-nil only for ErrUnknownAbility or a
// policy method that returned an error.
ok, err := g.Allows(ctx, "update", user, post)

// error-only — returns ErrDenied when not allowed. Idiomatic in handlers.
if err := g.Authorize(ctx, "update", user, post); err != nil {
    return err
}

// boolean opposite of Allows; any error collapses to "denied" (true).
if g.Denies(ctx, "update", user, post) {
    c.Forbidden("forbidden")
    return nil, nil
}

// Decision{Allowed, Reason} — for rendering rich responses.
d := g.Check(ctx, "update", user, post)
if !d.Allowed {
    log.Printf("denied: %s", d.Reason)
}
```

`Decision`:

```go
type Decision struct {
    Allowed bool
    Reason  string // human-readable when denied; empty when allowed
}
```

Sentinel errors for mapping to HTTP status:

| Error                     | Meaning                                       | Suggested status |
|---------------------------|-----------------------------------------------|------------------|
| `authz.ErrDenied`         | check ran and denied (from `Authorize`)       | 403 Forbidden    |
| `authz.ErrUnknownAbility` | no gate/policy matched the ability name       | 500 (config bug) |

```go
switch {
case errors.Is(err, authz.ErrDenied):
    c.Forbidden("not allowed")
case errors.Is(err, authz.ErrUnknownAbility):
    return err // programmer error — surface loudly in logs
}
```

## Integration with `auth` and the web layer

`authz` is transport-agnostic — it never imports `web`. Wire it in with a
middleware that pulls the authenticated user (resolved upstream from the
`auth` JWT) and runs the gate.

The `auth` package issues JWTs whose `Claims` carry the user id and role:

```go
type Claims struct {
    UserID uint64 `json:"uid"`
    Role   string `json:"role,omitempty"`
    Type   string `json:"typ,omitempty"`
    jwt.RegisteredClaims
}
```

Your auth middleware verifies the token, loads the `User`, and stores it
on the request context with `c.Set`. A second authorization middleware —
or an inline check in the handler — then consults the gate.

### Gate-checking middleware

`web.Middleware` is `func(next Handler) Handler`, and `Handler` is
`func(c *web.Context) (any, error)`. A factory that guards a route by a
gate-only ability:

```go
package middleware

import (
    "github.com/devituz/lagodev/authz"
    "github.com/devituz/lagodev/web"

    "github.com/you/myapp/models"
)

// Can builds a middleware that authorizes a resource-less ability.
func Can(g *authz.Gate[models.User], ability string) web.Middleware {
    return func(next web.Handler) web.Handler {
        return func(c *web.Context) (any, error) {
            u, ok := c.Get("user") // set by the auth middleware
            if !ok {
                c.Unauthorized("login required")
                return nil, nil
            }
            if err := g.Authorize(c.Ctx(), ability, u.(models.User), nil); err != nil {
                c.Forbidden("forbidden")
                return nil, nil
            }
            return next(c)
        }
    }
}
```

Apply it to a route group:

```go
func Register(app *web.App, g *authz.Gate[models.User]) {
    app.Group("/api/v1/admin", func(r *web.Router) {
        r.Use(middleware.Can(g, "manage-billing"))
        r.Get("/billing", controllers.ShowBilling)
    })
}
```

### Per-resource checks in the handler

Resource-scoped abilities need the loaded resource, so they belong inside
the handler after the row is fetched:

```go
func (ctrl *PostController) Update(c *web.Context) (any, error) {
    u, _ := c.Get("user")
    user := u.(models.User)

    post, err := ctrl.svc.Find(c.Ctx(), c.ParamUint("id"))
    if err != nil {
        return nil, err
    }

    if err := ctrl.gate.Authorize(c.Ctx(), "update", user, post); err != nil {
        c.Forbidden("you cannot edit this post")
        return nil, nil
    }

    var input models.Post
    if err := c.Bind(&input); err != nil {
        return nil, err
    }
    return ctrl.svc.Update(c.Ctx(), post, input)
}
```

Pass the same `*authz.Gate[User]` into controllers at construction time —
it is concurrency-safe, so one instance is shared across all requests.

## Production notes

- **One gate, built at boot.** Register every `Define`/`Policy`/`Before`
  during startup, then never mutate. Although registration is
  mutex-guarded, treating the gate as immutable after boot keeps the
  permission model auditable in one place.
- **`ErrUnknownAbility` is a config bug, not a 403.** It means a typo in
  an ability name or a missing `Policy` registration. Map it to 500 and
  alert on it — never silently treat it as "denied", or you'll mask
  broken authorization.
- **Keep `Before` narrow.** A superadmin bypass is fine; anything broader
  silently overrides every downstream policy and is hard to reason about
  during a security review.
- **Policies should be pure and fast.** They run on the request hot path.
  Do authorization math on data already loaded (`post.AuthorID == u.ID`);
  avoid DB round-trips inside a policy method — fetch first, authorize
  second.
- **Prefer `Authorize` in handlers.** Its `ErrDenied` return slots into
  the framework's normal `(any, error)` flow and is easy to map to 403.
  Reserve `Check` for places that need to render the `Reason`.
- **Authorize at the edge *and* defend in depth.** Middleware gates the
  route; the handler re-checks the concrete resource. The two are not
  redundant — middleware can't see the row that hasn't been fetched yet.
- **Test policies directly.** No HTTP needed: construct a gate, register
  the policy, and assert `Allows` against table-driven cases (see
  `authz/authz_test.go` for the pattern).
```

