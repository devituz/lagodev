# Telescope

`telescope` is an in-process debug dashboard in the spirit of Laravel
Telescope, built entirely on the Go standard library (`net/http`,
`html/template`, `sync`). It records requests, queries, logs, cache
operations, jobs, mail and exceptions into a bounded ring buffer and serves
them through an HTML dashboard plus a small JSON API.

The package is deliberately decoupled from the rest of the framework. It never
reaches into `orm`, `cache`, `mail` or `observability` internals; instead your
application *pushes* what happened through a small `Record*` API. That keeps
telescope testable in isolation and lets it sit behind any router or middleware
stack.

```go
import "github.com/devituz/lagodev/telescope"
```

## Quick start

Wire a `Recorder`, wrap your handler with its middleware, and mount the
dashboard:

```go
package main

import (
    "net/http"
    "time"

    "github.com/devituz/lagodev/telescope"
)

func main() {
    rec := telescope.NewRecorder(telescope.Options{Capacity: 500})

    mux := http.NewServeMux()
    mux.HandleFunc("/posts", func(w http.ResponseWriter, r *http.Request) {
        // Any Record* call made during the request is auto-correlated
        // with the Request entry via the context request id.
        rec.RecordQuery(r.Context(), telescope.QueryEntry{
            SQL:      "select * from posts where id = ?",
            Bindings: []any{42},
            Duration: 3 * time.Millisecond,
        })
        w.Write([]byte("ok"))
    })

    // Mount the dashboard under /telescope (strip the prefix so its
    // internal routing sees paths relative to the mount point).
    mux.Handle("/telescope/", http.StripPrefix("/telescope",
        rec.Handler(telescope.HandlerOptions{})))

    // Time + record every request that flows through the app.
    srv := rec.Middleware()(mux)
    http.ListenAndServe(":8080", srv)
}
```

Open <http://localhost:8080/telescope/> and hit `/posts` a few times to see
entries flow in.

`NewRecorder` is the only constructor; the zero `Recorder` value is not usable.

```go
type Options struct {
    Capacity int             // ring-buffer size; defaults to DefaultCapacity (500)
    Now      func() time.Time // clock override, for tests; defaults to time.Now
}
```

## What gets recorded

Everything telescope stores is an `Entry`: an immutable record with an `ID`, a
`Type`, a `Time`, an optional `RequestID` for correlation, a set of `Tags` and
a free-form, JSON-serialisable `Payload` map.

```go
type Entry struct {
    ID        string         `json:"id"`
    Type      Type           `json:"type"`
    Time      time.Time      `json:"time"`
    RequestID string         `json:"request_id,omitempty"`
    Tags      []string       `json:"tags,omitempty"`
    Payload   map[string]any `json:"payload,omitempty"`
}
```

The `Type` values are stable strings, also used as the `?type=` filter on the
dashboard and JSON API:

| Constant         | Value         |
|------------------|---------------|
| `TypeRequest`    | `"request"`   |
| `TypeQuery`      | `"query"`     |
| `TypeJob`        | `"job"`       |
| `TypeCache`      | `"cache"`     |
| `TypeMail`       | `"mail"`      |
| `TypeException`  | `"exception"` |
| `TypeLog`        | `"log"`       |

### Requests

`Middleware()` records a `Request` entry automatically when the wrapped handler
returns — you rarely call `RecordRequest` directly. It captures method, path,
status, duration and client IP, derives `client-error` / `server-error` tags
from the status code, and honours an inbound `X-Request-ID` header (or generates
a fresh id) which it mirrors onto the response.

```go
type RequestEntry struct {
    Method   string
    Path     string
    Status   int
    Duration time.Duration
    IP       string   // remote address, optional
    Tags     []string
}
```

### Queries

```go
rec.RecordQuery(ctx, telescope.QueryEntry{
    SQL:        "select * from users where id = ?",
    Bindings:   []any{42},
    Duration:   3 * time.Millisecond,
    Connection: "default",
})
```

```go
type QueryEntry struct {
    SQL        string
    Bindings   []any
    Duration   time.Duration
    Connection string   // optional
    Tags       []string
}
```

The query payload carries `sql`, `bindings`, `connection` and `duration_ms`.
Repeated statements under the same request also pick up N+1 metadata — see
below.

### Logs

```go
rec.RecordLog(ctx, telescope.LogEntry{
    Level:   "error",
    Message: "payment gateway timeout",
    Context: map[string]any{"order_id": 7, "attempt": 3},
})
```

```go
type LogEntry struct {
    Level   string         // e.g. "info", "warn", "error"
    Message string
    Context map[string]any // optional structured fields
    Tags    []string
}
```

### Cache, jobs, mail, exceptions

The remaining `Record*` helpers build the well-known shapes for the other entry
types:

```go
rec.RecordCache(ctx, telescope.CacheEntry{Operation: "get", Key: "user:42", Hit: false})
rec.RecordJob(ctx,   telescope.JobEntry{Name: "SendInvoice", Queue: "default", Status: "processed", Duration: 80 * time.Millisecond})
rec.RecordMail(ctx,  telescope.MailEntry{From: "no-reply@app.io", To: []string{"ada@example.com"}, Subject: "Welcome", Mailer: "smtp"})
rec.RecordException(ctx, telescope.ExceptionEntry{Err: err, Class: "db", Stack: stack})
```

```go
type CacheEntry struct {
    Operation string // "get", "set", "forget", ...
    Key       string
    Hit       bool
    Value     any
    Tags      []string
}

type JobEntry struct {
    Name     string
    Queue    string
    Status   string // e.g. "processed", "failed"
    Duration time.Duration
    Err      error
    Tags     []string
}

type MailEntry struct {
    From    string
    To      []string
    Subject string
    Mailer  string
    Tags    []string
}

type ExceptionEntry struct {
    Err   error
    Class string // optional logical class/category
    Stack string // optional stack trace
    Tags  []string
}
```

### Custom entries

Every `Record*` helper is a convenience over `Record(Entry)`, which accepts any
`Entry` — so you can store custom kinds with your own `Type` and `Payload`:

```go
rec.Record(telescope.Entry{
    Type:    telescope.Type("webhook"),
    Tags:    []string{"stripe"},
    Payload: map[string]any{"event": "charge.succeeded"},
})
```

### Correlation

The request id is the glue. `Middleware()` injects it into the request context
(via `ContextWithRequestID`) and any `Record*` call you make with that context
inherits it as the entry's `RequestID`. Inside a handler you can read it back:

```go
id, ok := telescope.RequestIDFromContext(r.Context())
```

Pass the **request context** into every `Record*` call so child entries line up
under the parent `Request` entry in the dashboard.

## N+1 detection

`RecordQuery` normalises each statement — literals and `?` / `$n` placeholders
are collapsed, whitespace squeezed, case folded — so two queries that differ
only in their bound values compare equal. When the same normalised statement is
recorded more than once **under the same request id**, the duplicates are
flagged:

- the `"n+1"` tag is added to the entry, and
- the payload gains `"n_plus_one": true` and a `"repeat_count"` running count.

This surfaces the classic ORM lazy-loading problem without any ORM hook — the
heuristic lives entirely in `RecordQuery`.

```go
// Two queries that normalise to the same statement under one request:
rec.RecordQuery(ctx, telescope.QueryEntry{SQL: "select * from posts where id = 1"})
rec.RecordQuery(ctx, telescope.QueryEntry{SQL: "select * from posts where id = 2"})

q := rec.Filter(telescope.TypeQuery, 0)[0] // newest first
flagged, _ := q.Payload["n_plus_one"].(bool) // true
count := q.Payload["repeat_count"]           // 2
```

The per-request N+1 accumulator is dropped when the matching `Request` entry is
recorded, so it cannot leak one stale map per request id over the life of the
process.

## The dashboard

`Handler` returns an `http.Handler` serving an HTML dashboard plus a JSON API.
Everything renders through `html/template`, so recorded payloads are
auto-escaped — the dashboard is safe to point at untrusted recorded data.

```go
type HandlerOptions struct {
    Title        string // page title; defaults to "Telescope"
    DefaultLimit int    // cap on list/JSON entries without ?limit=; defaults to 200, <=0 means no cap
}
```

Routes, relative to the mount point:

| Route             | Description                                          |
|-------------------|------------------------------------------------------|
| `GET  /`          | HTML overview: counts per type + recent entries      |
| `GET  /{type}`    | HTML list of entries of one type                     |
| `GET  /entry/{id}`| HTML detail view with a pretty-printed payload        |
| `GET  /api/entries` | JSON entry list (honours `?type=` and `?limit=`)   |
| `POST /clear`     | clears every stored entry, then redirects to `/`     |

Query the JSON API directly when scripting:

```bash
curl 'http://localhost:8080/telescope/api/entries?type=query&limit=50'
```

### Reading entries from code

The recorder exposes the buffer for tests and tooling (all newest-first):

```go
all     := rec.Entries()                          // every stored entry
queries := rec.Filter(telescope.TypeQuery, 100)   // by type, capped (0 = no cap)
entry, ok := rec.Find("abc123")                   // by id
rec.Reset()                                         // drop everything
```

## Production notes

- **Memory is bounded.** The recorder is a fixed-size ring buffer — once
  `Capacity` (default `DefaultCapacity` = 500) is reached, the oldest entries
  are evicted. Memory does not grow unbounded, but pick a `Capacity` that fits
  the payload sizes you record; large query bindings or cached values multiply
  the per-entry cost.

- **The dashboard is unauthenticated by design — you must gate it.** `Handler`
  is deliberately auth-agnostic: mounted bare it serves SQL, bindings, log
  context, request IPs and stack traces to *anyone* who can reach the route.
  Never expose it publicly. Pick one of, in order of preference:

  1. **Don't mount it in production at all** — the safest posture. Mount it only
     when the environment is non-production:

     ```go
     if cfg.Env != "production" {
         mux.Handle("/telescope/", http.StripPrefix("/telescope",
             rec.Handler(telescope.HandlerOptions{})))
     }
     ```

  2. **Gate it with HTTP Basic auth** when it must be reachable on a network
     that isn't already private. `RequireBasicAuth` wraps the dashboard handler;
     it compares credentials in constant time and answers `401` +
     `WWW-Authenticate` on any mismatch. It panics at construction if both the
     username and password are empty, so a misconfigured deploy fails loudly
     instead of silently exposing the dashboard:

     ```go
     dash := rec.Handler(telescope.HandlerOptions{})
     mux.Handle("/telescope/", http.StripPrefix("/telescope",
         telescope.RequireBasicAuth("ops", os.Getenv("TELESCOPE_PASSWORD"), dash)))
     ```

     Source the password from config/secret store, never a literal. Pair it with
     TLS so the credentials aren't sent in the clear.

  3. **Mount it behind your existing app middleware** — session auth, an IP
     allowlist, a reverse-proxy `auth_request`, or a VPN-only internal route.
     Because `Handler` adds no auth of its own, whatever gate you wrap it in is
     the only gate; there is no built-in bypass.

  The `POST /clear` endpoint (destructive — wipes the whole buffer) lives under
  the same handler, so it is covered by whichever gate you choose above.

- **Recorded data is rendered safely.** Everything reaches the browser through
  `html/template`, so adversarial values — a `<script>` payload smuggled in via
  a request path, query string, exception message, log context, or a
  user-controlled `X-Forwarded-For` / `X-Request-ID` header — is HTML-escaped,
  not executed. This is a stored-XSS defense and is covered by regression tests
  (`security_test.go`). Gating per above is still required: escaping prevents
  XSS, it does not stop an unauthenticated viewer from *reading* the leaked SQL,
  IPs and stack traces.

- **Internal bookkeeping is bounded.** Besides the ring buffer, the N+1
  accumulator (`nplus`) is normally drained per request by `RecordRequest`. As a
  defense against callers that record queries under request ids that never
  terminate (background contexts reusing `ContextWithRequestID`, aborted
  requests), the accumulator is also hard-capped — once it reaches an internal
  ceiling the oldest tracked id is evicted, so it can never grow without bound.

- **Recording is always-on but cheap.** `Record*` takes a single mutex and
  writes one slot in the ring buffer; the N+1 heuristic is an O(1) map bump per
  query. Still, in production you may want to skip recording entirely (don't
  install the middleware / don't call `Record*`) rather than record and hide,
  so you pay nothing.

- **`POST /clear` is destructive.** It wipes the whole buffer. Keep it behind
  the same gate as the rest of the dashboard.

- **Request id propagation.** The middleware honours an inbound `X-Request-ID`
  header and mirrors it onto the response, so the same id can flow across
  services and tie distributed traces together.

## See also

- **[GETTING_STARTED.md](GETTING_STARTED.md)** — project scaffolding and `main.go`.
- **[ORM.md](ORM.md)** — the query builder whose statements you feed into `RecordQuery`.
- **[WEB.md](WEB.md)** — the `web` framework and its middleware stack.
