# Sessions

`lagodev/session` provides cookie-backed sessions modelled on Laravel's
`Session` facade. A session is a `map[string]any` persisted server-side
under an opaque ID; the client carries only that ID in an
`HttpOnly`+`Secure` cookie.

> The package speaks plain `net/http`, so it drops into `lagodev/web`,
> Chi, Echo, Gin/Fiber adapters, or a raw `http.ServeMux` unchanged.

## Overview

The moving parts are small and explicit:

| Type            | Role                                                            |
|-----------------|----------------------------------------------------------------|
| `Store`         | Persists per-session data. Memory ships built-in; Redis/DB plug in via the interface. |
| `Manager`       | Owns the cookie config + a `Store`. One per app, shared across requests. |
| `Session`       | The per-request handle — `Get`/`Put`/`Forget`, regeneration, destroy. |
| `Options`       | Cookie name, TTL, Secure, SameSite. Zero values are production-safe. |

The only argument that changes per request is `*Session`; everything
else is wired once at boot.

## Quick start

Build a `Manager` at startup and install its middleware. The middleware
starts the session on every request, exposes it via
`session.FromRequest(r)`, and saves it before the response body is
written (so `Set-Cookie` always lands first).

```go
package main

import (
    "fmt"
    "net/http"
    "time"

    "github.com/devituz/lagodev/session"
)

func main() {
    store := session.NewMemoryStore(2 * time.Hour)
    mgr := session.NewManager(store, session.Options{
        CookieName: "myapp_session",
        TTL:        2 * time.Hour,
        // Insecure: true,   // local HTTP only — omit in production
    })

    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        s := session.FromRequest(r)
        n, _ := s.Get("views")
        count, _ := n.(int)
        count++
        s.Put("views", count)
        fmt.Fprintf(w, "views: %d", count)
    })

    // Wrap the whole mux once.
    handler := mgr.Middleware()(mux)
    http.ListenAndServe(":8080", handler)
}
```

`mgr.Middleware()` returns a `func(http.Handler) http.Handler`, the
standard stdlib shape — chain it like any other middleware.

### With `lagodev/web`

The `web` framework's middleware is `func(next Handler) Handler`, not the
stdlib shape, so adapt at the edge: start the session manually in a
`web.Middleware` and stash it on the context.

```go
func Sessions(mgr *session.Manager) web.Middleware {
    return func(next web.Handler) web.Handler {
        return func(c *web.Context) (any, error) {
            s, err := mgr.Start(c.Ctx(), c.Request)
            if err != nil {
                return nil, err
            }
            out, herr := next(c)
            _ = s.Save(c.Ctx(), c.Writer) // persist + (re)issue cookie
            return out, herr
        }
    }
}

app.Use(Sessions(mgr))
```

Inside a handler, call `mgr.Start` again — it reloads the same session by
cookie — or pass the `*Session` through `c.Set`/`c.Get`.

## Reading and writing session data

`Session` is the per-request handle. It is **not** safe for concurrent
use across goroutines — one request, one `*Session`.

```go
s := session.FromRequest(r)

// Read
v, ok := s.Get("user_id")          // (any, bool); ok=false when missing
name := s.GetString("name")        // "" if missing or not a string
all := s.All()                     // copy of the whole bag

// Write (each marks the session dirty)
s.Put("user_id", 42)
s.Put("name", "ali")

// Remove
s.Forget("name")                   // delete one key
s.Flush()                          // clear all keys, keep the ID
```

| Method                  | Returns        | Notes                                   |
|-------------------------|----------------|-----------------------------------------|
| `Get(key)`              | `(any, bool)`  | `ok=false` on miss                      |
| `GetString(key)`        | `string`       | `""` on miss or type mismatch           |
| `All()`                 | `map[string]any` | a copy — mutating it does nothing     |
| `Put(key, value)`       | —              | sets dirty                              |
| `Forget(key)`           | —              | sets dirty only if the key existed      |
| `Flush()`               | —              | clears data, preserves ID               |
| `ID()`                  | `string`       | the opaque identifier                   |
| `IsNew()`               | `bool`         | created this request (no prior cookie)  |

Changes are not persisted until `Save` runs. Under `mgr.Middleware()`
that happens automatically on the first response write; with the manual
`web` adapter above you call `s.Save(ctx, w)` yourself.

```go
// Manual flow without the middleware:
s, _ := mgr.Start(ctx, r)
s.Put("flash", "saved")
if err := s.Save(ctx, w); err != nil { /* store unreachable */ }
```

`ErrMissing` is exported for callers that prefer comparing against a
sentinel rather than the `ok` boolean.

## Stores

A `Store` is the persistence backend. The interface is deliberately tiny
so a Redis or SQL driver plugs in without touching call sites:

```go
type Store interface {
    Read(ctx context.Context, id string) (map[string]any, bool, error)
    Write(ctx context.Context, id string, data map[string]any, ttl time.Duration) error
    Destroy(ctx context.Context, id string) error
}
```

`Write` TTL semantics: `ttl == 0` means "use the store default";
`ttl < 0` means the record is already expired and should be removed.

### MemoryStore

The built-in store keeps everything in a `sync.RWMutex`-guarded map with
lazy expiry plus a background sweeper (runs every 5 minutes):

```go
store := session.NewMemoryStore(2 * time.Hour) // default TTL per entry
defer store.Close()                            // stops the sweeper goroutine
```

Pass `0` for "no expiry" — entries then live until `Destroy`. Always call
`store.Close()` on shutdown to stop the sweeper.

`MemoryStore` is correct for single-replica apps and tests. It does
**not** survive process restarts and is not shared across replicas — see
[Production notes](#production-notes).

## Security

Session security is built into the defaults; you mostly need to call
`Regenerate` at the right moments.

### Session fixation — regenerate on privilege change

Issue a fresh ID after login and logout to defeat session fixation. The
data is preserved; only the identifier rotates and the old record is
destroyed.

```go
func login(w http.ResponseWriter, r *http.Request) {
    s := session.FromRequest(r)
    // ... verify credentials ...
    if err := s.Regenerate(r.Context()); err != nil {
        http.Error(w, "session error", http.StatusInternalServerError)
        return
    }
    s.Put("user_id", user.ID)
}
```

### Logout — Destroy

`Destroy` removes the record from the store and expires the cookie
(`MaxAge=-1`). A destroyed session is never resurrected: the middleware
and `Save` both no-op afterwards, so no live cookie is re-issued.

```go
func logout(w http.ResponseWriter, r *http.Request) {
    s := session.FromRequest(r)
    _ = s.Destroy(r.Context(), w)
}
```

### Cookie flags

`Options` zero values are production-safe — you don't configure security,
you opt *out* of it for local development:

| Field        | Default            | Notes                                           |
|--------------|--------------------|-------------------------------------------------|
| `CookieName` | `lagodev_session`  | override per app                                |
| `TTL`        | `2h`               | also becomes the cookie `MaxAge`                |
| `Insecure`   | `false`            | **inverted** — cookies are `Secure` by default; set `true` only for local HTTP |
| `SameSite`   | `Lax`              | `http.SameSite*Mode`                            |

Cookies are always `HttpOnly` (no JS access) and `Path=/`. The ID is 32
bytes from `crypto/rand`, hex-encoded — not guessable.

## Production notes

**Swap the store for multi-replica / restart survival.** `MemoryStore`
lives in one process. Behind a load balancer, or across deploys, sessions
created on one replica are invisible to the others. Implement `Store`
against Redis or your database and pass it to `NewManager` — no other
code changes:

```go
mgr := session.NewManager(myRedisStore, session.Options{TTL: 2 * time.Hour})
```

**Garbage collection.** `MemoryStore` sweeps expired entries every 5
minutes and also evicts lazily on `Read`. An external store should lean
on the backend's own TTL (Redis `EXPIRE`, a DB `expires_at` index + cron)
rather than re-implementing a sweeper. The `ttl` argument to `Write` is
the contract: honour `ttl <= 0` as "delete".

**TTL alignment.** The cookie `MaxAge` is derived from `Options.TTL`, so
keep the store's default TTL and the manager TTL in sync — otherwise the
cookie can outlive its server-side record (or vice-versa).

**Save failures.** Under `mgr.Middleware()`, a failed `Save` (store
unreachable) is absorbed silently because the response has already begun
streaming. If you need to be notified, use the manual flow and inspect
the error from `s.Save(ctx, w)` yourself before writing the response.

**Context plumbing.** `session.FromRequest(r)` reads off the request
context; `session.FromContext(ctx)` is the bare-`ctx` variant. Both
return `nil` when the middleware wasn't applied — guard accordingly in
shared helpers.
```
