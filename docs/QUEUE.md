# Queue cookbook

Background jobs modelled on Laravel's Queue facade. A **Job** is a payload
(name + JSON bytes), a **Queue** is the transport (push/pop/ack/nack), and a
**Worker** is the long-running consumer that pulls Jobs off a Queue and runs
your registered handlers.

```go
import "github.com/devituz/lagodev/queue"
```

Three packages:

| Package                | Purpose                                            |
|------------------------|----------------------------------------------------|
| `queue`                | Job, Queue interface, Worker, in-memory driver     |
| `queue/sqlqueue`       | Database-backed driver (survives restarts)         |
| `queue/dashboard`      | Horizon-style stats + failed-job UI (stdlib only)  |

## Quick start

### 1. Define a job

A job is any struct that JSON-encodes. No interface, no base type — the Go
type name is the routing key.

```go
type SendWelcomeEmail struct {
    UserID uint64
}
```

### 2. Register a handler and dispatch

```go
import (
    "context"
    "github.com/devituz/lagodev/queue"
)

func main() {
    ctx := context.Background()

    q := queue.NewMemoryQueue()
    w := queue.NewWorker(q)

    // Handle[T] binds a typed handler. The Worker decodes the payload into T
    // and calls fn. The routing key is the type name "SendWelcomeEmail".
    queue.Handle[SendWelcomeEmail](w, func(ctx context.Context, j SendWelcomeEmail) error {
        return mailer.Send(ctx, buildEmail(j.UserID))
    })

    go w.Run(ctx) // blocks until ctx is cancelled or Stop() is called

    // Dispatch JSON-encodes the payload and pushes it.
    _ = queue.Dispatch(ctx, q, SendWelcomeEmail{UserID: 42})
}
```

`Dispatch[T]` and `Handle[T]` must agree on the type `T` — the job name is
`reflect`-derived from `T`, so a handler registered for `SendWelcomeEmail`
only receives jobs dispatched with that same struct.

### 3. Dispatch with a delay

```go
import "time"

// Deliver in 10 minutes instead of immediately.
_ = queue.DispatchAfter(ctx, q, SendWelcomeEmail{UserID: 42}, 10*time.Minute)
```

`DispatchAfter` sets `Job.AvailableAt`; drivers must not deliver the job
before that time.

## The Job wire format

`Dispatch` builds this; you rarely construct it by hand, but the dashboard
and custom drivers see it directly:

```go
type Job struct {
    ID          string    // unique per enqueue
    Name        string    // handler routing key (the type name)
    Payload     []byte    // JSON-encoded struct
    Attempts    int       // incremented on each delivery
    AvailableAt time.Time // earliest delivery time (zero = now)
}
```

## The Queue interface

Drivers implement this contract; the Worker only knows the interface:

```go
type Queue interface {
    Push(ctx context.Context, j Job) error
    Pop(ctx context.Context, wait time.Duration) (Job, error) // ErrEmpty after wait
    Ack(ctx context.Context, jobID string) error              // success → remove
    Nack(ctx context.Context, jobID string, retryAfter time.Duration) error
    Len() int                                                 // approximate pending
}
```

Semantics are **at-least-once**: `Pop` reserves a job, the Worker runs it,
then calls `Ack` (done) or `Nack` (return it, optionally after `retryAfter`).
If the process dies between Pop and Ack, the job is redelivered. Write
handlers to be **idempotent**. `Pop` returns `queue.ErrEmpty` when nothing is
available within `wait`.

## Drivers

### Memory driver

```go
q := queue.NewMemoryQueue()
```

In-process FIFO with delayed delivery and at-least-once semantics. Jobs live
only in RAM — a restart loses everything still pending. Use it for
single-process apps, tests, and local development.

### SQL driver

```go
import (
    "github.com/devituz/lagodev/database"
    "github.com/devituz/lagodev/queue"
    "github.com/devituz/lagodev/queue/sqlqueue"
)

q, err := sqlqueue.New(conn) // conn is a *database.Connection
if err != nil {
    return err
}
if err := q.Setup(ctx); err != nil { // creates the `jobs` table (dialect-aware)
    return err
}

_ = queue.Dispatch(ctx, q, SendWelcomeEmail{UserID: 42})

w := queue.NewWorker(q)
go w.Run(ctx)
```

The driver stores jobs in a single `jobs` table and survives process
restarts. `Pop` selects the next row by `available_at` and stamps it
`reserved_at = now()` inside a transaction, so concurrent workers — even on
separate replicas — never claim the same job.

Options:

```go
q, _ := sqlqueue.New(conn,
    sqlqueue.WithTable("background_jobs"),         // default "jobs"
    sqlqueue.WithVisibilityTimeout(5*time.Minute), // default 15m
)
```

- **`WithTable`** — override the table name.
- **`WithVisibilityTimeout`** — how long a reserved-but-unacked job stays
  invisible before another worker may reclaim it (orphan recovery). Pick a
  value comfortably larger than your longest handler runtime; too short and a
  slow-but-alive handler's job is re-delivered and runs twice.

> On SQLite the driver pins the pool to a single open connection and raises
> `busy_timeout`, so concurrent reservers serialise on the writer lock instead
> of erroring with `database is locked`.

## Retries & failed jobs

The Worker controls the retry loop. Configure it with the fluent setters
(each returns `*Worker`, so they chain):

```go
w := queue.NewWorker(q).
    MaxRetry(5).                       // attempts before giving up (then OnFailed)
    Backoff(30 * time.Second).         // wait between retries (Nack retryAfter)
    Poll(time.Second).                 // how long Pop blocks when idle
    Logger(log.Printf).                // structured-ish logging hook
    OnFailed(func(j queue.Job, err error) {
        log.Printf("dead-letter %s: %v", j.Name, err)
    })
```

| Setter            | Effect                                                          |
|-------------------|----------------------------------------------------------------|
| `MaxRetry(n)`     | Total attempts before the job is considered failed             |
| `Backoff(d)`      | Delay applied via `Nack(..., d)` on each retry                 |
| `Poll(d)`         | `wait` passed to `Pop` when the queue is idle                  |
| `Logger(fn)`      | `func(format string, args ...any)` for worker logs            |
| `OnFailed(fn)`    | Called once when a job exhausts `MaxRetry`                      |

A handler returning a non-nil error triggers a `Nack` (retry after
`Backoff`). When `Attempts` reaches `MaxRetry`, the Worker stops retrying and
invokes `OnFailed` — wire that callback to a dead-letter store (see below).

## The dashboard

`queue/dashboard` is a Horizon / Bull-Board-style operational view: live
per-queue counters, throughput, and a failed-job list with retry / forget
actions. It is stdlib-only (`net/http`, `html/template`) and decoupled from
any concrete driver via two seams:

- **`Stats`** — a concurrency-safe counter set the worker feeds.
- **`FailedJobs`** — a pluggable dead-letter store the UI lists, retries, and
  forgets. `MemoryFailedJobs` ships as the reference implementation.

### Wiring

```go
import (
    "context"
    "net/http"

    "github.com/devituz/lagodev/queue"
    "github.com/devituz/lagodev/queue/dashboard"
)

// 1. Counters + dead-letter store. The store's RequeueFunc re-enqueues on retry.
stats := dashboard.NewStats()
failed := dashboard.NewMemoryFailedJobs(func(ctx context.Context, j queue.Job) error {
    return q.Push(ctx, j) // put the job back on the queue
})

// 2. Feed the seams from worker wiring.
w := queue.NewWorker(q).OnFailed(func(j queue.Job, err error) {
    stats.OnFailed("default")
    failed.Record(j, "default", err) // dead-letter it
})
go w.Run(ctx)

// 3. Mount the handler. Routes are absolute under the mount point,
//    so strip the prefix you mount it on.
insp := dashboard.NewInspector(stats, failed)
http.Handle("/admin/queues/", http.StripPrefix("/admin",
    dashboard.Handler(insp)))
```

A "queue" here is any caller-chosen label (`"default"`, `"emails"`, …) —
`queue.Job` carries no queue name of its own, so a single `Stats` can track
several logical queues fed by distinct workers. Feed the rest of the counters
at the dispatch / handler boundaries:

```go
stats.OnPushed("default")    // at dispatch time
stats.OnProcessed("default") // after a successful Ack
stats.OnRetried("default")   // on each retry
```

### Routes

`Handler` mounts these (paths are absolute under the mount prefix):

```
GET   /queues/                    overview: per-queue stats + throughput (HTML)
GET   /queues/failed              failed-job list with retry/forget/flush (HTML)
POST  /queues/failed/{id}/retry   re-enqueue + drop  (CSRF-guarded)
POST  /queues/failed/{id}/forget  drop               (CSRF-guarded)
POST  /queues/failed/flush        drop all           (CSRF-guarded)
GET   /queues/api/stats           JSON stats snapshot
```

POST routes use double-submit-cookie CSRF protection issued by the rendered
pages. The handler is safe for concurrent use.

### Inspector

`Inspector` is the read/act seam the `Handler` renders. `NewInspector(stats,
failed)` adapts the in-memory pair; a `sqlqueue`-backed implementation can
satisfy the same interface to read directly from the database.

```go
type Inspector interface {
    Stats() (StatsView, error)              // per-queue counters + totals
    Pending(queue string, page int) ([]JobView, error)
    Failed(page int) ([]JobView, error)     // dead-lettered jobs (1-based pages)
    Retry(id string) error                  // re-enqueue + drop record
    Forget(id string) error                 // drop without re-enqueue
    FlushFailed() error                     // drop every failed job
}
```

Pagination is 1-based; `PageSize` (50) rows per page. Either argument to
`NewInspector` may be nil: a nil `Stats` yields empty counters, a nil store
yields an empty failed list and rejects the mutating actions.

Reading a JSON snapshot directly, without the HTML UI:

```go
sv, _ := insp.Stats()
for _, qs := range sv.Queues {
    fmt.Printf("%s: processed=%d failed=%d pending=%d throughput=%.1f/s\n",
        qs.Queue, qs.Processed, qs.Failed, qs.Pending, qs.Throughput)
}
fmt.Println("total failed:", sv.Totals.Failed)
```

### The failed-job store

```go
type FailedJob struct {
    ID       string    // store-local id (defaults to Job.ID)
    Queue    string    // logical queue label
    Job      queue.Job // original payload, replayed verbatim on retry
    Err      string    // last handler error message
    FailedAt time.Time // when it was dead-lettered
}
```

`MemoryFailedJobs` is the in-process reference store:

```go
failed := dashboard.NewMemoryFailedJobs(requeue) // requeue is a RequeueFunc
failed.Record(job, "emails", err) // dead-letter a job (call this from OnFailed)
failed.Len()                      // current count
list, _ := failed.List(ctx)       // newest-first
_ = failed.Retry(ctx, id)         // re-enqueue via RequeueFunc + drop
_ = failed.Forget(ctx, id)        // drop without re-enqueue
```

`Retry`/`Forget` return `dashboard.ErrNotFound` for an unknown id. A nil
`RequeueFunc` makes `Retry` forget-only (drops the record, enqueues nothing) —
useful for read-only dashboards. Optionally chain `.WithStats(stats)` so the
store bumps the retry counter for you.

To back failed jobs with the database instead of RAM, implement the
`FailedJobs` interface (`List` / `Retry` / `Forget`) over a `failed_jobs`
table and re-insert into the jobs table on `Retry`.

### Throughput sampling

`Sampler` derives a jobs/min rate per queue over a sliding window by polling
an `Inspector` in a background goroutine:

```go
s := dashboard.NewSampler(insp, 5*time.Second, time.Minute).Start()
defer s.Stop() // single goroutine — always stop it to avoid a leak

rate := s.Rate("default")        // jobs/min over the window
window := s.Window("default")    // []ThroughputSample for charting
```

`interval` and `window` default to 5s / 1m when ≤ 0.

## Production notes

### Graceful shutdown

`Worker.Run` blocks until its context is cancelled (or `Stop` is called) and
then drains in-flight work. Tie it to a signal handler so SIGTERM lets the
current job finish instead of killing it mid-flight:

```go
ctx, stop := signal.NotifyContext(context.Background(),
    syscall.SIGINT, syscall.SIGTERM)
defer stop()

w := queue.NewWorker(q)
queue.Handle[SendWelcomeEmail](w, handleWelcome)

errc := make(chan error, 1)
go func() { errc <- w.Run(ctx) }()

<-ctx.Done() // first signal cancels the context
w.Stop()     // ask the worker to finish the current job and return
<-errc
```

Because delivery is at-least-once, a job interrupted before `Ack` is
redelivered on the next start — idempotent handlers turn that into a no-op.

### Concurrency

`Worker.Run` processes one job at a time. To get parallelism, run several
workers against the **same** queue — the SQL driver's transactional `Pop`
guarantees each job is claimed by exactly one of them:

```go
const workers = 4
for i := 0; i < workers; i++ {
    w := queue.NewWorker(q)
    queue.Handle[SendWelcomeEmail](w, handleWelcome)
    go w.Run(ctx)
}
```

This scales across separate processes and replicas too, since reservation
happens in the database, not in memory.

### Driver choice

- **Memory** — single process only; fine for tests and local dev. Pending
  jobs vanish on restart.
- **SQL** — durable and multi-replica safe; the default for production. Tune
  `WithVisibilityTimeout` to your slowest handler and keep handlers
  idempotent. Run `Setup(ctx)` once at boot (it is idempotent) so the `jobs`
  table exists before the first dispatch.

## See also

- **[ORM.md](ORM.md)** — the `*database.Connection` the SQL driver consumes.
- **[GETTING_STARTED.md](GETTING_STARTED.md)** — wiring `database.NewManager`
  and opening a connection.
- **[WEB.md](WEB.md)** — mounting the dashboard handler inside the `web` app.
