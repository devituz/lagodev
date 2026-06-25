# HTTP client cookbook

`httpclient` is a fluent wrapper around `net/http`, modelled on Laravel's
`Http` facade. It folds away the boilerplate every service caller writes
by hand: JSON marshalling, query strings, headers, basic/bearer auth,
timeouts, and retries with exponential backoff.

```go
import "github.com/devituz/lagodev/httpclient"
```

## Overview

A `*httpclient.Client` is an **immutable configuration**. Every setter
(`BaseURL`, `Header`, `Retry`, …) returns a clone, so a base client can
be shared and specialised without mutating the original:

```go
base := httpclient.New().BaseURL("https://api.example.com").BearerToken(tok)

// Neither call mutates base — each returns its own clone.
traced := base.Header("X-Trace", "1")
slow   := base.Timeout(30 * time.Second)
```

Requests return a `*httpclient.Response` with the body **already drained
and closed** — you never call `resp.Body.Close()` yourself. Transport
errors are wrapped and returned; a non-2xx status is **not** an error
(inspect `resp.Status()` / `resp.OK()`).

`New()` ships safe defaults: a 10s timeout, no retries, the stdlib
transport, and a 10 MiB response-body cap.

## Quick start

### GET + decode JSON

```go
type User struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
}

resp, err := httpclient.New().
    BaseURL("https://api.example.com").
    Get(ctx, "/users/42")
if err != nil {
    return err // transport / timeout failure
}
if !resp.OK() {
    return fmt.Errorf("unexpected status %d: %s", resp.Status(), resp.String())
}

var u User
if err := resp.JSON(&u); err != nil {
    return err
}
```

### POST JSON

`PostJSON` marshals the body and sets `Content-Type: application/json`:

```go
payload := map[string]any{"name": "Ada", "email": "ada@example.com"}

resp, err := httpclient.New().
    BaseURL("https://api.example.com").
    BearerToken(tok).
    PostJSON(ctx, "/users", payload)
if err != nil {
    return err
}
if resp.Status() != http.StatusCreated {
    return fmt.Errorf("create failed: %s", resp.String())
}

var created User
_ = resp.JSON(&created)
```

`PutJSON` and `PatchJSON` work identically for `PUT` / `PATCH`, and
`Delete(ctx, path)` issues a bodyless `DELETE`.

```go
_, err = httpclient.New().BaseURL(api).Delete(ctx, "/users/42")
```

## Building a request

All builders take `ctx` first and a path that is either relative (joined
to `BaseURL`) or absolute (`http://…` / `https://…`, used as-is).

| Method                              | Verb     | Body                       |
|-------------------------------------|----------|----------------------------|
| `Get(ctx, path)`                    | `GET`    | none                       |
| `Delete(ctx, path)`                 | `DELETE` | none                       |
| `PostJSON(ctx, path, body)`         | `POST`   | `body` marshalled to JSON  |
| `PutJSON(ctx, path, body)`          | `PUT`    | `body` marshalled to JSON  |
| `PatchJSON(ctx, path, body)`        | `PATCH`  | `body` marshalled to JSON  |

### Headers

```go
c := httpclient.New().
    Header("Accept", "application/json").
    Headers(map[string]string{
        "X-Request-Id": reqID,
        "X-Tenant":     tenant,
    })
```

`Header` adds or overwrites a single header; `Headers` merges a map in
one call. Both return a clone.

### Auth

```go
c1 := httpclient.New().BearerToken(tok)             // Authorization: Bearer <tok>
c2 := httpclient.New().BasicAuth("user", "secret")  // HTTP Basic
```

### Query parameters

`Query` **appends** — call it repeatedly (including with the same key)
to build up repeated parameters:

```go
resp, err := httpclient.New().
    BaseURL("https://api.example.com").
    Query("page", "2").
    Query("tag", "go").
    Query("tag", "http"). // → ?page=2&tag=go&tag=http
    Get(ctx, "/articles")
```

Query values set on the client apply to every request it issues, and are
merged onto absolute URLs too.

### JSON body

The `*JSON` helpers accept any value that `encoding/json` can marshal —
a struct, a `map[string]any`, a slice, etc. A marshalling failure is
returned (wrapped) before any request goes out:

```go
type CreatePost struct {
    Title string `json:"title"`
    Body  string `json:"body"`
}

resp, err := c.PostJSON(ctx, "/posts", CreatePost{Title: "Hi", Body: "…"})
```

## Handling the response

The body is read into memory once, so `*Response` is cheap to inspect
repeatedly:

```go
resp.Status()        // int — HTTP status code
resp.OK()            // bool — true for 2xx
resp.Header("ETag")  // string — a single response header value
resp.Body()          // []byte — raw body
resp.String()        // string — body as text
resp.JSON(&dst)      // unmarshal body into dst
```

Always branch on status before trusting the payload — a non-2xx response
still carries a (usually error-shaped) body:

```go
resp, err := c.Get(ctx, "/users/42")
if err != nil {
    return err // network / timeout
}
switch {
case resp.OK():
    var u User
    return resp.JSON(&u)
case resp.Status() == http.StatusNotFound:
    return ErrUserNotFound
default:
    return fmt.Errorf("upstream %d: %s", resp.Status(), resp.String())
}
```

`JSON` returns an error if the body is empty, so a `204 No Content`
response should be checked with `OK()` rather than decoded.

## Timeouts

`Timeout` overrides the per-request deadline (default 10s) and rebuilds
the underlying `http.Client`, preserving any custom transport:

```go
c := httpclient.New().Timeout(5 * time.Second)
```

The `ctx` passed to every request is also honoured — cancelling it
aborts an in-flight request (and any pending retry backoff).

## Retries

`Retry(n)` retries up to `n` times on **transport errors** and on
retryable statuses (`429 Too Many Requests` and any `5xx`). Backoff
starts at the `Backoff` value (default 100ms) and **doubles** each
attempt:

```go
c := httpclient.New().
    BaseURL("https://api.example.com").
    Retry(3).                     // up to 1 + 3 = 4 total attempts
    Backoff(200 * time.Millisecond) // 200ms → 400ms → 800ms
```

Request bodies are buffered once and **replayed** on each attempt, so
`PostJSON`/`PutJSON`/`PatchJSON` retry safely. Note that a retry on a
non-idempotent verb can double-apply on the server — reserve `Retry` for
idempotent calls, or make the endpoint idempotent (e.g. an
`Idempotency-Key` header). 4xx other than 429 are returned immediately
(not retried). If `ctx` is cancelled during a backoff, the request fails
with `ctx.Err()`.

## Custom transport

`Transport` swaps the underlying `http.RoundTripper` — for TLS pinning,
connection-pool tuning, instrumentation, or a mock in tests:

```go
c := httpclient.New().Transport(myRoundTripper)
```

This is the seam used by the package's own tests: supply a
`RoundTripper` that returns canned responses and assert on the request
without touching the network.

## Response size cap

To stop a hostile or runaway upstream from OOMing the process, the
client buffers at most `DefaultMaxResponseBytes` (10 MiB) of the response
body. A larger body fails with `ErrResponseTooLarge`:

```go
resp, err := c.Get(ctx, "/big")
if errors.Is(err, httpclient.ErrResponseTooLarge) {
    // upstream sent more than the cap
}
```

Raise, lower, or disable the cap with `MaxResponseBytes`:

```go
c := httpclient.New().MaxResponseBytes(50 << 20) // 50 MiB
c := httpclient.New().MaxResponseBytes(0)         // disable (trust upstream)
```

## Base URL & shared config

Build one configured client at startup and derive per-call variants from
it. Because every setter clones, the base is immutable and safe to share
across goroutines:

```go
// internal/gateway/client.go
package gateway

import (
    "time"

    "github.com/devituz/lagodev/httpclient"
)

func New(token string) *httpclient.Client {
    return httpclient.New().
        BaseURL("https://api.example.com/v1").
        BearerToken(token).
        Header("Accept", "application/json").
        Timeout(8 * time.Second).
        Retry(2)
}
```

```go
gw := gateway.New(cfg.Token)

// Each call below starts from the shared config; per-call tweaks
// (an extra header, a query param) don't leak back into gw.
resp, err := gw.Header("X-Request-Id", reqID).Get(ctx, "/users/42")
```

Path resolution rules:

- Relative path (`/users/42` or `users/42`) → joined onto `BaseURL`.
- Absolute path (`https://other.example.com/x`) → used verbatim,
  bypassing `BaseURL`.
- A relative path with no `BaseURL` set returns an error.

## Production notes

- **`New()` per chain, not per call.** Construct a configured base client
  once and reuse it; setters clone cheaply, but the base lets you share
  the connection pool of the underlying `http.Client`.
- **Always pass a real `context.Context`** with a deadline or
  cancellation tied to the inbound request — `Timeout` and `ctx` are
  independent; the effective limit is whichever fires first.
- **Treat non-2xx as data, not an error.** `err != nil` means the call
  never produced a response (DNS, dial, TLS, timeout, retries exhausted).
  Status handling is always your job via `OK()`/`Status()`.
- **Retry only idempotent calls** (or idempotent-keyed ones). Retrying a
  plain `POST` can create duplicates.
- **Keep the body cap on** unless you control the upstream; an
  unbounded body is a memory-exhaustion vector. Use
  `MaxResponseBytes(0)` deliberately, not by default.
- **Use `Transport` to inject mocks in tests** rather than hitting real
  endpoints — fast, deterministic, and offline.
- `Body()` returns the live backing slice; copy it if you intend to
  retain or mutate it beyond the response's lifetime.
</content>
</invoke>
