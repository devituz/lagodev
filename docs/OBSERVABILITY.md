# Observability

Package `observability` is a vendor-neutral, **dependency-free** tracing /
metrics / log-correlation layer. It pulls in nothing but the Go standard
library — no OpenTelemetry SDK, no Prometheus client. The API is built from
small interfaces (`Tracer`, `Span`, `Meter`, `Counter`, `Gauge`,
`Histogram`) so a real backend can be slotted in behind an adapter later
without touching call sites.

What you get out of the box:

- **Tracing** — spans recorded into an in-process ring buffer, with optional
  emission through `log/slog`. W3C `traceparent` propagation for `net/http`.
- **Metrics** — an in-memory registry of counters, gauges and histograms.
- **Log correlation** — `trace_id` / `span_id` live on the request context,
  ready to be pulled into your structured logs.

```go
import "github.com/devituz/lagodev/observability"
```

> The package never edits `web`, `orm` or anything else. Every helper is
> returned for **you** to apply — wrap a handler, time a query, inject a
> header. Nothing is installed globally on your behalf.

## Quick start

A `Tracer` and a `Registry` (the default `Meter`) are the two roots. Create
them once at boot and pass them where you need them.

```go
package main

import (
    "log/slog"
    "os"

    "github.com/devituz/lagodev/observability"
)

var (
    // Emit each finished span as a structured log line.
    tracer = observability.NewTracer(
        observability.WithSpanLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil))),
        observability.WithRingCapacity(1024),
    )
    metrics = observability.NewRegistry()
)
```

`NewTracer()` with no options still records spans into a 256-entry ring
buffer; it just won't log them. `NewRegistry()` is always empty to start.

## Tracing — spans

`Tracer.Start` opens a span and returns a **derived context**. Pass that
context down the call stack: any `Start` made from it becomes a child span,
sharing the same `TraceID`.

```go
func handleRequest(ctx context.Context) error {
    ctx, span := tracer.Start(ctx, "http.request")
    span.SetAttr("http.method", "GET")
    span.SetAttr("http.route", "/users/{id}")
    defer span.End()

    // Child span — derived from ctx, so it links to the parent automatically.
    if err := queryDB(ctx); err != nil {
        span.RecordError(err) // marks the span StatusError
        return err
    }
    span.SetStatus(observability.StatusOK, "")
    return nil
}

func queryDB(ctx context.Context) error {
    _, span := tracer.Start(ctx, "db.query")
    span.SetAttr("db.system", "postgres")
    defer span.End()
    // ... run the query ...
    return nil
}
```

The `Span` interface:

| Method                          | Effect                                            |
|---------------------------------|---------------------------------------------------|
| `SetAttr(key, value) Span`      | Attach a typed key/value pair                     |
| `RecordError(err) Span`         | Store the error; sets `StatusError` if unset      |
| `SetStatus(status, desc) Span`  | Override the status (`StatusOK` / `StatusError`)  |
| `End()`                         | Finalize and record duration (idempotent)         |
| `Context() SpanContext`         | Return the span's W3C identifiers                 |

Mutators return the same `Span`, so they chain:

```go
span.SetAttr("user_id", 42).SetStatus(observability.StatusOK, "")
```

`End()` is safe to call twice — the second call is a no-op, so a
`defer span.End()` next to an early manual `End()` won't double-record.

### Span status

```go
const (
    observability.StatusUnset // no explicit status
    observability.StatusOK    // completed successfully
    observability.StatusError // failed
)
```

`SpanStatus.String()` renders `"unset"` / `"ok"` / `"error"`.

### Span context

`Span.Context()` returns the W3C identifiers:

```go
type SpanContext struct {
    TraceID  string // 32 lower-hex chars
    SpanID   string // 16 lower-hex chars
    ParentID string // parent span id; empty for a root span
    Sampled  bool
}

func (sc SpanContext) Valid() bool
```

You rarely build one by hand — propagation and `Start` produce them. To read
it anywhere downstream:

```go
if sc, ok := observability.SpanContextFromContext(ctx); ok {
    log.Printf("trace=%s span=%s", sc.TraceID, sc.SpanID)
}
```

`ContextWithSpanContext(ctx, sc)` seeds a `context.Context` with a span
context — used when continuing a remote trace (see
[Propagation](#propagation--w3c-traceparent)).

### Reading recorded spans

The default tracer keeps the last N finished spans in a ring buffer. Reach
them through the `Recorder` interface (the default `Tracer` implements it):

```go
if rec, ok := tracer.(observability.Recorder); ok {
    for _, s := range rec.Spans() { // oldest first
        fmt.Printf("%s %s %v %s\n", s.Name, s.Context.TraceID, s.Duration, s.Status)
    }
}
```

Each entry is an immutable `SpanRecord`:

```go
type SpanRecord struct {
    Name      string
    Context   SpanContext
    Start     time.Time
    End       time.Time
    Duration  time.Duration
    Status    SpanStatus
    StatusMsg string
    Err       string
    Attrs     map[string]any
}
```

## Metrics

`NewRegistry()` returns a `*Registry`, which satisfies the `Meter`
interface. Instruments are deduplicated by name **and** label set, so calling
`Counter("x", "route", "/a")` twice returns the same object.

```go
type Meter interface {
    Counter(name string, labels ...string) Counter
    Gauge(name string, labels ...string) Gauge
    Histogram(name string, buckets []float64, labels ...string) Histogram
}
```

Labels are a **flat** `name, value, name, value …` slice.

### Counter

Monotonically increasing. `Add` ignores negative values.

```go
reqs := metrics.Counter("http_requests_total", "route", "/users", "method", "GET")
reqs.Inc()      // +1
reqs.Add(3)     // +3
_ = reqs.Value()
```

### Gauge

Goes up and down.

```go
inflight := metrics.Gauge("http_inflight_requests")
inflight.Inc()
defer inflight.Dec()
inflight.Set(0)
```

### Histogram

Records a distribution into fixed buckets (upper bounds, ascending). Pass
`nil` to use `DefaultBuckets` — latency-oriented bounds in seconds:

```go
var DefaultBuckets = []float64{
    0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}
```

```go
lat := metrics.Histogram("http_request_seconds", nil, "route", "/users")
lat.Observe(0.012)
lat.Observe(0.4)

snap := lat.Snapshot()
fmt.Println(snap.Count, snap.Sum) // total observations, sum of values
```

`Snapshot()` returns an immutable view with **cumulative** bucket counts:

```go
type HistogramSnapshot struct {
    Buckets []float64 // upper bounds
    Counts  []uint64  // cumulative counts aligned with Buckets
    Count   uint64    // total observations
    Sum     float64
}
```

Bucket `i` holds the count of observations `<= Buckets[i]`, and
`Count - Counts[last]` is the `+Inf` overflow.

## Structured logs (trace correlation)

There are two complementary ways to get `trace_id` into your logs.

**1. Emit spans as logs.** `WithSpanLogger` makes the tracer write one record
per finished span, already carrying `trace_id`, `span_id`, `duration`,
`status`, any `parent_span_id`, `error`, and each attribute as `attr.<key>`:

```go
tracer := observability.NewTracer(
    observability.WithSpanLogger(slog.Default()),
)
// {"msg":"span","span_name":"db.query","trace_id":"…","span_id":"…","duration":1200000,"status":"ok"}
```

Errored spans are logged at `slog.LevelError`, everything else at
`LevelInfo`.

**2. Correlate your own log lines.** Pull the active span context off the
request context inside an `slog.Handler` so every record you emit is tagged:

```go
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
    if sc, ok := observability.SpanContextFromContext(ctx); ok {
        r.AddAttrs(
            slog.String("trace_id", sc.TraceID),
            slog.String("span_id", sc.SpanID),
        )
    }
    return h.Handler.Handle(ctx, r)
}

logger := slog.New(traceHandler{slog.NewJSONHandler(os.Stdout, nil)})

// Inside a request handler — pass the span context's ctx:
logger.InfoContext(ctx, "user fetched", "id", id)
// → adds trace_id/span_id automatically
```

This handler depends only on `SpanContextFromContext`; it works with any
`slog.Handler` you wrap.

## Propagation — W3C `traceparent`

Cross-process tracing rides on the standard `traceparent` header. Four
helpers cover ingress and egress:

```go
func ExtractHTTP(h http.Header) (SpanContext, bool)
func InjectHTTP(h http.Header, sc SpanContext)
func ParseTraceparent(value string) (SpanContext, bool)
func FormatTraceparent(sc SpanContext) string
```

`ExtractHTTP` reads and validates the inbound header; `InjectHTTP` writes it
(skipping invalid contexts). `Parse`/`Format` are the raw string forms,
e.g. `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`. Only W3C
version `00` is emitted, and the invalid `ff` version is rejected on parse.

### Inbound: continue a remote trace

```go
func startServerSpan(tracer observability.Tracer, r *http.Request) (context.Context, observability.Span) {
    ctx := r.Context()
    if remote, ok := observability.ExtractHTTP(r.Header); ok {
        ctx = observability.ContextWithSpanContext(ctx, remote)
    }
    return tracer.Start(ctx, "http.server")
}
```

Seeding the remote context before `Start` makes the new span inherit the
caller's `TraceID` and become its child.

### Outbound: forward the trace

The lower-level building blocks (`Start`, `Clone`, `InjectHTTP`) are still
available if you need a bespoke transport, but the package now ships a ready
`http.RoundTripper` — see [Outbound HTTP](#outbound-http--roundtripper) below.

## Integration layer (`net/http`)

The primitives above are enough to wire anything by hand, but the package also
ships a small, batteries-included integration layer on top of them — a server
middleware, a client `RoundTripper`, and a `/metrics` handler — so the common
case is one call. It stays `net/http`-native (no dependency on `web`), and any
half can be disabled by passing `nil`.

### Inbound HTTP — `Middleware`

```go
func Middleware(tracer Tracer, reg *Registry, opts ...HTTPOption) func(http.Handler) http.Handler
```

`Middleware` wraps an `http.Handler` and, per request:

- extracts an inbound W3C `traceparent` and **continues that trace** (the
  server span becomes a child of the remote span);
- starts a span (default `"http.server"`) with `http.method` / `http.target`
  attributes, and hands the span-carrying context down via the request;
- holds an **in-flight gauge** up for the request duration;
- records a **request counter** and a **latency histogram** labeled by
  `method` and `status` on completion;
- sets the span status from the response code (`5xx` → `StatusError`);
- echoes the active trace id into a response header (default `Trace-Id`).

The response status is captured by a thin `ResponseWriter` wrapper that
exposes `Unwrap()`, so `http.ResponseController` (and therefore `Flusher`,
`Hijacker`, `Pusher`, `ReadFrom`, …) keeps working through it.

```go
package main

import (
    "net/http"

    "github.com/devituz/lagodev/observability"
)

func main() {
    tracer := observability.NewTracer()
    reg := observability.NewRegistry()

    mux := http.NewServeMux()
    mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
        // r.Context() already carries the active server span.
        _, _ = w.Write([]byte("hi"))
    })
    mux.Handle("/metrics", observability.MetricsHandler(reg))

    srv := observability.Middleware(tracer, reg)(mux)
    _ = http.ListenAndServe(":8080", srv)
}
```

Default metric names: `http_server_requests_total`,
`http_server_request_duration_seconds`, `http_server_in_flight_requests`.
Override anything through options:

```go
mw := observability.Middleware(tracer, reg,
    observability.WithSpanName("api.request"),
    observability.WithMetricNames("reqs_total", "req_seconds", "inflight"),
    observability.WithBuckets([]float64{0.01, 0.05, 0.25, 1, 5}),
    observability.WithTraceResponseHeader("X-Trace-Id"), // "" disables it
)
```

### Outbound HTTP — `RoundTripper`

```go
func NewRoundTripper(base http.RoundTripper, tracer Tracer, reg *Registry, opts ...HTTPOption) http.RoundTripper
```

`NewRoundTripper` wraps an `http.RoundTripper` (nil → `http.DefaultTransport`)
so every outbound call gets a client span (default `"http.client"`), injects
the active span as a `traceparent` header into the outgoing request, and
records an outbound counter + latency histogram labeled by `method` and
`status`. It clones the request before injecting, so the caller's request is
never mutated (per the `RoundTripper` contract).

```go
client := &http.Client{
    Transport: observability.NewRoundTripper(nil, tracer, reg),
}

// If req's context carries a server span, the client span is linked to it
// and the downstream service continues the same trace.
resp, err := client.Do(req.WithContext(ctx))
```

Default metric names: `http_client_requests_total`,
`http_client_request_duration_seconds`. On transport error the span is marked
`StatusError` and the `status` label is `"error"`.

### Exposing metrics — `MetricsHandler`

```go
func MetricsHandler(reg *Registry) http.Handler
```

`MetricsHandler` renders the whole registry in the Prometheus text exposition
format (v0.0.4) — `# TYPE` lines for counters/gauges/histograms, cumulative
`_bucket` series with a `le="+Inf"` bucket, plus `_sum` and `_count`. Output
is deterministic (families and series in insertion order) and uses **stdlib
only**; no Prometheus client is pulled in.

```go
mux.Handle("/metrics", observability.MetricsHandler(reg))
```

Sample output:

```text
# TYPE http_server_requests_total counter
http_server_requests_total{method="GET",status="200"} 5
# TYPE http_server_request_duration_seconds histogram
http_server_request_duration_seconds_bucket{method="GET",status="200",le="0.005"} 1
http_server_request_duration_seconds_bucket{method="GET",status="200",le="+Inf"} 5
http_server_request_duration_seconds_sum{method="GET",status="200"} 0.83
http_server_request_duration_seconds_count{method="GET",status="200"} 5
```

### One-call wiring — `Provider`

`Provider` bundles a `Tracer` and a `Registry` and exposes the two middlewares
plus the metrics handler as methods, so the whole stack wires from one object:

```go
func NewProvider(tracer Tracer, reg *Registry) *Provider // nil args get defaults

func (p *Provider) Tracer() Tracer
func (p *Provider) Registry() *Registry
func (p *Provider) Middleware(opts ...HTTPOption) func(http.Handler) http.Handler
func (p *Provider) RoundTripper(base http.RoundTripper, opts ...HTTPOption) http.RoundTripper
func (p *Provider) MetricsHandler() http.Handler
```

```go
p := observability.NewProvider(nil, nil) // default Tracer + Registry

mux := http.NewServeMux()
mux.Handle("/metrics", p.MetricsHandler())

srv := p.Middleware()(mux)
client := &http.Client{Transport: p.RoundTripper(nil)}

_ = srv
_ = client
```

### Using it with `web`

The integration layer is `net/http`-native, so it slots under any router that
exposes its `http.Handler`. If you prefer a `web.Middleware` adapter (to keep
the span context on `c.Request`), wrap the primitives directly — the pattern is
unchanged from the [propagation](#inbound-continue-a-remote-trace) examples:
extract → seed context → `Start` → reassign `c.Request` → record on the way
out.

## Production notes

**Overhead.** Span creation is two `crypto/rand` reads (trace + span id) plus
a mutex-guarded ring write on `End()`; metric instruments use atomic CAS
loops (counters/gauges) and a single mutex per histogram. All operations are
allocation-light and safe for concurrent use, but the hot path is not free —
don't open a span around a tight inner loop.

**Sampling.** There is no sampler in the default tracer: every `Start`
records. The `Sampled` flag on `SpanContext` is propagated faithfully (it
rides the `traceparent` flags byte), so an upstream sampling decision is
honored when you continue a remote trace — but locally everything is kept.
If you need head sampling, gate the `tracer.Start` call behind your own
decision, or swap in a backend adapter that samples.

**Ring buffer.** The in-process buffer (`WithRingCapacity`, default 256) is a
*recent-history* window, not durable storage — it overwrites oldest-first
and is lost on restart. Treat `Recorder.Spans()` as a debug/introspection
feed, and rely on `WithSpanLogger` plus your log pipeline for retention.

**Cardinality.** Each unique label-value combination allocates a permanent
series in the registry; series are never evicted, and registry memory grows
with cardinality. Never label with unbounded values (user ids, raw paths with
ids, request ids) — use route templates (`/users/{id}`) and bounded enums.

## See also

- **[GETTING_STARTED.md](GETTING_STARTED.md)** — project bootstrap.
- **[WEB.md](WEB.md)** — middleware, `Context`, request lifecycle.
- **[ORM.md](ORM.md)** — wrap child spans around DB calls.
