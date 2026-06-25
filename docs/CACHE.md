# Cache

The `cache` package is a small key-value abstraction modelled on Laravel's
`Cache` facade. The core ships with one driver — a process-local in-memory
store — and a `Store` interface narrow enough to back with Redis, Memcached,
or a database from a separate sub-package without dragging extra dependencies
into the core module.

```go
import "github.com/devituz/lagodev/cache"
```

## Overview

Two layers make up the API:

- **`Store`** — the driver interface. Every backend implements `Get`, `Put`,
  `Forget`, `Flush`, `Has`.
- **Free helpers** — `Remember`, `Pull`, `Add`, `Increment`, `Decrement`,
  `Many`, `PutMany`. They take a `Store` as their first non-context argument
  and transparently use a richer codepath when the driver implements an
  optional extension interface (atomic put-if-absent, atomic counters, batch
  ops), falling back to a portable emulation otherwise.

Values are always `[]byte`. Serialization is your call — `json.Marshal`,
`gob`, protobuf, a raw string. The cache never inspects the bytes (except
counters, which are stored as decimal ASCII).

`*Memory` returns **copies** on every read, so a value you `Get` can be
mutated freely without corrupting the stored entry.

## Quick start

```go
ctx := context.Background()

c := cache.NewMemory()
defer c.Close() // stops the background sweeper

// put with a 5-minute TTL
_ = c.Put(ctx, "user:1", []byte("Ada"), 5*time.Minute)

// get → (value, found, error)
v, ok, err := c.Get(ctx, "user:1")
if err != nil {
    log.Fatal(err)
}
if ok {
    fmt.Println(string(v)) // Ada
}

// forget
_ = c.Forget(ctx, "user:1")
```

### `Remember` — cache-aside

The workhorse helper: return the cached value if present, otherwise call `fn`,
store its result under `ttl`, and return it.

```go
raw, err := cache.Remember(ctx, c, "users:list", time.Minute, func() ([]byte, error) {
    users, err := listUsers(ctx)
    if err != nil {
        return nil, err
    }
    return json.Marshal(users)
})
if err != nil {
    return err
}

var users []User
_ = json.Unmarshal(raw, &users)
```

If `fn` returns an error nothing is stored and the error propagates. A `Put`
failure after a successful `fn` returns the freshly computed value *and* the
error, so the caller still gets a usable result.

## TTL and expiration

`Put` (and every TTL-taking helper) interprets the duration the same way:

| `ttl`   | Meaning                                                              |
|---------|---------------------------------------------------------------------|
| `> 0`   | Expires `ttl` from now. Reads after expiry are a miss.              |
| `== 0`  | Stored forever — until an explicit `Forget` or `Flush`.            |
| `< 0`   | Treated as already-expired: the key is **removed**, nothing stored. |

Expiry on `*Memory` is lazy — an expired entry is detected and deleted on the
next `Get`/`Has` touch. A background goroutine also sweeps the map every five
minutes so abandoned keys can't grow it without bound. That goroutine is why
you should `Close()` a `Memory` you're discarding:

```go
c := cache.NewMemory()
defer c.Close()
```

After `Close()` the cache stays usable; you just lose the periodic sweep
(entries are still evicted lazily on access).

`Forever` is sugar for a zero TTL:

```go
_ = c.Forever(ctx, "config:flags", payload) // == Put(ctx, key, payload, 0)
```

## Stores and drivers

### The `Store` interface

```go
type Store interface {
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Forget(ctx context.Context, key string) error
    Flush(ctx context.Context) error
    Has(ctx context.Context, key string) (bool, error)
}
```

That's the whole contract. `Get` reports absence with `ok == false` (not an
error); `ErrMiss` exists for higher layers that prefer an error sentinel but
the interface itself never returns it. Implement these five methods and every
free helper in this package works against your driver.

### Memory driver

`NewMemory()` is the only built-in. It's a `sync.RWMutex`-guarded map, safe
for concurrent use, with the lazy expiry + background sweeper described above.
Good for single-process apps, tests, and per-instance hot caches. For
cross-process or persistent caching, plug in a `Store` backed by Redis or the
database — the helpers don't care which.

`*Memory` implements `Store` **and** every optional extension interface, so
all helpers run on their atomic, single-critical-section codepaths.

## Reading and removing in one step

### `Pull` — get and delete atomically

```go
v, ok, err := cache.Pull(ctx, c, "one-shot-token")
```

On `*Memory` the read-and-delete happens in one critical section, so of N
concurrent `Pull`s for the same key at most one observes the value — handy for
single-use tokens. On a driver that doesn't implement the optional `Pull`
extension, the helper degrades to a non-atomic `Get` then `Forget`, which can
race a concurrent `Pull`/`Put` of the same key.

## Atomic operations

### `Add` — put-if-absent (the lock primitive)

`Add` stores only when the key is absent or expired, returning `true` when it
won the write. There's no dedicated `Lock` type — `Add` *is* the lock
primitive:

```go
ok, err := cache.Add(ctx, c, "lock:report:42", []byte("1"), 30*time.Second)
if err != nil {
    return err
}
if !ok {
    return errors.New("report already being generated")
}
defer cache.Forget /* via Store */ // release when done: c.Forget(ctx, "lock:report:42")

generateReport(ctx, 42)
```

On `*Memory` the check-and-set is a single critical section: of N concurrent
`Add`s for one key exactly one returns `true`. On a driver lacking the atomic
extension, `Add` falls back to `Has` + `Put`, which can race — fine for
best-effort caching, **not** safe for locking. The TTL acts as a lease so a
crashed holder eventually releases the lock.

### `Increment` / `Decrement` — atomic counters

```go
n, err := cache.Increment(ctx, c, "hits:/home", 1) // n == new value
m, err := cache.Decrement(ctx, c, "stock:sku-9", 3)
```

Counters are stored as their decimal ASCII representation, so a plain `Get`
after `Increment(..., 5)` reads back `[]byte("5")`. A missing or expired key
starts from `0`. Incrementing a key whose value isn't a base-10 integer
returns an error and leaves the value untouched. Counters are stored **without
expiry** — use `Put`/`Forget` to reset or bound their lifetime.

These helpers require a store that implements the atomic counter extension. On
any other driver they return `ErrUnsupported` rather than silently losing
updates under concurrency through a non-atomic `Get`+`Put`:

```go
_, err := cache.Increment(ctx, someBasicStore, "k", 1)
if errors.Is(err, cache.ErrUnsupported) {
    // driver can't do atomic counters
}
```

## Batch operations

`Many` and `PutMany` move several keys at once. On `*Memory` they run in a
single critical section (a consistent snapshot for reads, an all-or-nothing
write); on a basic store they degrade to a per-key loop.

```go
// write a batch under one TTL
_ = cache.PutMany(ctx, c, map[string][]byte{
    "u:1": []byte("Ada"),
    "u:2": []byte("Linus"),
}, time.Hour)

// read a batch — missing/expired keys are omitted from the map
got, err := cache.Many(ctx, c, "u:1", "u:2", "u:404")
// len(got) may be < len(keys); got["u:404"] is absent on a miss
```

`PutMany` honours the same TTL rules as `Put`, including `ttl < 0` deleting
every listed key.

## Errors

| Sentinel         | When                                                                 |
|------------------|----------------------------------------------------------------------|
| `ErrMiss`        | Informational "key not found" for layers that prefer an error to `ok=false`. The `Store` interface itself never returns it. |
| `ErrUnsupported` | Returned by `Increment`/`Decrement` when the store can't do atomic counters. Compare with `errors.Is`. |

## API summary

### Driver

| Symbol                         | Purpose                                  |
|--------------------------------|------------------------------------------|
| `type Store interface`         | Driver contract (`Get/Put/Forget/Flush/Has`) |
| `NewMemory() *Memory`          | In-memory store with background sweeper  |
| `(*Memory).Close()`            | Stop the sweeper goroutine               |

### Helpers (take a `Store`)

| Helper                                              | Notes                              |
|-----------------------------------------------------|------------------------------------|
| `Remember(ctx, s, key, ttl, fn)`                    | Cache-aside get-or-compute         |
| `Pull(ctx, s, key)`                                 | Get + delete (atomic on `*Memory`) |
| `Add(ctx, s, key, value, ttl)`                      | Put-if-absent / lock primitive     |
| `Increment(ctx, s, key, delta)`                     | Atomic counter (`+`)               |
| `Decrement(ctx, s, key, delta)`                     | Atomic counter (`-`)               |
| `Many(ctx, s, keys...)`                             | Batch get                          |
| `PutMany(ctx, s, items, ttl)`                       | Batch put under one TTL            |

`*Memory` also exposes `Forever`, plus driver-level `Add`, `Increment`,
`Decrement`, `Many`, `PutMany`, and `Pull` methods directly if you hold a
`*Memory` rather than a `Store`.

## Production notes

- **Always serialize deliberately.** Values are opaque bytes. Pick one
  encoding per key namespace and stick to it; a `Get` that fails to
  `json.Unmarshal` is a versioning bug, not a cache bug.
- **Pick TTLs, not `Forever`.** Unbounded entries on `*Memory` live in process
  memory until `Forget`/`Flush`. Counters are the deliberate exception — reset
  them explicitly.
- **Lock with a lease.** Use `Add` with a TTL longer than the protected work
  but short enough that a crashed holder releases in reasonable time. Release
  with `Forget` in a `defer`.
- **Mind the driver tier.** `Increment`/`Decrement` need an atomic store or
  they return `ErrUnsupported`; `Add`/`Pull`/`Many`/`PutMany` silently fall
  back to non-atomic emulation on basic stores. The built-in `*Memory` is
  fully atomic — handle the unsupported/racy cases only when you wire up a
  custom driver.
- **`*Memory` is per-process.** Two server instances behind a load balancer
  hold independent caches. For shared state (sessions, rate limits, distributed
  locks) back the `Store` with Redis so the atomicity guarantees span
  processes.
- **`Close()` long-lived caches** to stop the sweeper; short-lived test caches
  can rely on `lagotest` teardown.
