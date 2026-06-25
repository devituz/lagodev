# Scheduling & processes

Two small, dependency-free packages:

- **`scheduling`** — a Laravel-style task scheduler. Jobs run on a
  wall-clock cadence (every X minutes, hourly, daily at a fixed time, …)
  dispatched by a single in-process `Runner`.
- **`process`** — a fluent wrapper over `os/exec` for running external
  commands with context cancellation, timeouts, and a typed result.

No external cron daemon and no cron-string parser are involved: the
common Laravel cadences are expressed as small `Schedule` values that
the `Runner` ticks against once per second.

## Quick start — define a scheduled task

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/devituz/lagodev/scheduling"
)

func main() {
    ctx := context.Background()

    s := scheduling.New()

    s.Job("cleanup-old-sessions", scheduling.Hourly(), func(ctx context.Context) error {
        return purgeStaleSessions(ctx)
    })
    s.Job("nightly-billing", scheduling.DailyAt("02:00"), runBilling)
    s.Job("metrics-flush", scheduling.Every(5*time.Minute), flushMetrics)

    if err := s.Run(ctx); err != nil {
        log.Fatal(err)
    }
}
```

`Run` blocks. In a real service you start it in its own goroutine
alongside the HTTP server (see [Running the scheduler](#running-the-scheduler-graceful-shutdown)).

A job is three things:

| Argument   | Type                                | Purpose                                |
|------------|-------------------------------------|----------------------------------------|
| `name`     | `string`                            | Unique label, used in logs and `Tasks()` |
| `schedule` | `scheduling.Schedule`               | When to fire                           |
| `fn`       | `func(ctx context.Context) error`   | The work (`scheduling.TaskFn`)         |

Names must be unique — registering a duplicate **panics** to catch typos
at startup.

## Cron / interval expressions

Every cadence is a `Schedule` constructor. There is no cron-string
syntax; you pick a constructor.

```go
type Schedule interface {
    Due(prev, now time.Time) bool
    String() string
}
```

| Constructor                  | Fires                                          |
|------------------------------|------------------------------------------------|
| `Every(d time.Duration)`     | every `d` since the previous run (`d` must be > 0) |
| `EveryMinute()`              | alias for `Every(time.Minute)`                 |
| `EveryFiveMinutes()`         | alias for `Every(5 * time.Minute)`             |
| `Hourly()`                   | at the top of every hour                       |
| `Daily()`                    | every day at `00:00` local                     |
| `DailyAt("HH:MM")`           | every day at the given time, local             |
| `Weekly()`                   | Mondays at `00:00` local                       |
| `Monthly()`                  | the 1st of every month at `00:00` local        |

Notes that matter in production:

- **All fixed-time schedules are *local* time** — they read `now.Hour()`,
  `now.Day()`, etc. on the server clock. Run the host in the timezone you
  expect, or in UTC and adjust your `DailyAt` values.
- **`Every(d)` is interval-based, not aligned.** It fires when
  `now - lastRun >= d`. `lastRun` is seeded at registration, so
  `Every(5*time.Minute)` first fires ~5 minutes after `Run` starts, not
  immediately.
- **`DailyAt` panics on a malformed string.** Valid input is `"HH:MM"`
  with `00 <= HH <= 23` and `00 <= MM <= 59`. Pass it a literal, not user
  input.
- `Schedule.String()` returns a human label (`"daily at 02:00"`,
  `"every 5m0s"`, `"hourly"`) — handy for status pages.

### Custom schedules

`Schedule` is a plain interface, so you can supply your own cadence —
e.g. "every weekday at 09:00":

```go
type weekdayAt struct{ h, m int }

func (w weekdayAt) String() string { return "weekdays" }
func (w weekdayAt) Due(prev, now time.Time) bool {
    switch now.Weekday() {
    case time.Saturday, time.Sunday:
        return false
    }
    if now.Hour() != w.h || now.Minute() != w.m {
        return false
    }
    return prev.Day() != now.Day()
}

s.Job("daily-report", weekdayAt{9, 0}, sendReport)
```

`Due` receives the previous fire time (`prev`) and the current tick
(`now`); the `Runner` owns the `prev` bookkeeping, so implementations
stay stateless.

## Overlap prevention

The `Runner` will **never run the same job concurrently with itself**.
While a job's `fn` is still executing, its schedule is skipped — the next
due tick is ignored until the previous run returns. Different jobs run in
parallel; only same-name overlap is suppressed.

This is automatic and needs no opt-in. The trade-off: a long-running
nightly job that overruns into the next day's slot will skip that slot
rather than stack up. If you need a per-job log of skips, install a
logger (below) and compare `Tasks()` snapshots.

> Note: overlap prevention is **in-process only**. Across multiple
> replicas of your service each `Runner` is independent, so a job
> registered in N pods runs N times per tick. For multi-replica
> deployments either run the scheduler in a single dedicated replica, or
> guard the job body with a distributed lock (advisory lock / Redis
> `SET NX`).

## Inspecting & logging

```go
// Override the 1s poll interval (mainly for tests).
s.Tick(100 * time.Millisecond)

// Structured logger callback — called when a job's fn returns an error.
s.Logger(func(format string, args ...any) {
    log.Printf(format, args...)
})

// Snapshot of every registered task, for a /status endpoint.
for _, t := range s.Tasks() {
    fmt.Printf("%-22s %-16s last=%s running=%v\n",
        t.Name, t.Schedule, t.LastRun.Format(time.RFC3339), t.Running)
}
```

`Tick`, `Logger` return the `*Runner` so they chain off `New()`. A
`TaskInfo` is a snapshot:

```go
type TaskInfo struct {
    Name     string
    Schedule string    // from Schedule.String()
    LastRun  time.Time
    Running  bool
}
```

A job's `fn` error is **logged, not fatal** — the scheduler keeps
running. Handle/escalate inside the job or via the logger callback.

## Running the scheduler (graceful shutdown)

`Run(ctx)` blocks until either `ctx` is cancelled or `Stop()` is called.
Before returning it **drains** — it waits for any jobs that have already
fired to finish. The two stop paths differ in how they treat in-flight
work:

- **`Stop()`** — signals `Run` to exit but lets in-flight jobs run to
  completion (their context is *not* cancelled). Graceful.
- **cancelling `ctx`** — cancels the context handed to in-flight jobs,
  asking them to abort, then drains.

`Run` is restartable: calling `Run` again after `Stop` starts a fresh
cycle.

Typical wiring alongside an HTTP server with signal-based shutdown:

```go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(),
        syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    s := scheduling.New()
    s.Job("nightly-billing", scheduling.DailyAt("02:00"), runBilling)
    s.Logger(func(f string, a ...any) { log.Printf(f, a...) })

    // Run the scheduler in the background.
    done := make(chan error, 1)
    go func() { done <- s.Run(ctx) }()

    // ... start your web.App here ...

    <-ctx.Done()      // first SIGINT/SIGTERM
    s.Stop()          // let in-flight jobs finish
    if err := <-done; err != nil && err != context.Canceled {
        log.Printf("scheduler stopped: %v", err)
    }
}
```

`Run` returns `ctx.Err()` (e.g. `context.Canceled`) when the caller's
context ends, and `nil` when stopped via `Stop()`.

## Running external commands — `process`

`process.Command` builds a command; `Run(ctx)` executes it and returns a
typed `Result`. It **never starts a shell** — the first argument is the
program, the rest are exact `argv` entries. No string-splitting, no shell
injection from interpolated values.

```go
import "github.com/devituz/lagodev/process"

r, err := process.Command("git", "rev-parse", "HEAD").Run(ctx)
if err != nil {
    return err
}
fmt.Println(r.Stdout) // commit SHA
```

The builder is **immutable** — every setter returns a clone, so a base
command can be reused safely:

```go
base := process.Command("git").Dir("/srv/repo")

status := base.Args("status", "--short")     // base is untouched
log10  := base.Args("log", "-n", "10")       // independent command
```

### Builder methods

| Method                      | Effect                                            |
|-----------------------------|---------------------------------------------------|
| `Command(name, args...)`    | Start a command (`name` = program, then argv)     |
| `.Args(args...)`            | Replace the argument list                         |
| `.AppendArgs(args...)`      | Append to the argument list                       |
| `.Dir(d)`                   | Working directory                                 |
| `.Env(k, v)`                | Add/override an environment variable              |
| `.Stdin(r io.Reader)`       | Feed stdin from a reader                          |
| `.Timeout(d)`               | Kill the process after `d` (→ `ErrTimeout`)       |
| `.Run(ctx)`                 | Execute, returning `(Result, error)`              |

### Result and error handling

```go
type Result struct {
    ExitCode int
    Stdout   string
    Stderr   string
    Duration time.Duration
}

func (r Result) Successful() bool // ExitCode == 0
```

`Run` returns an error for any non-success outcome, but the `Result` is
always populated so you can inspect output/exit code regardless:

```go
res, err := process.Command("npm", "test").
    Dir("/srv/app").
    Env("CI", "1").
    Timeout(2 * time.Minute).
    Run(ctx)

switch {
case errors.Is(err, process.ErrTimeout):
    log.Printf("npm test timed out after %s", res.Duration)

case err != nil:
    // Non-zero exit: inspect the exit code and captured stderr.
    var exit *process.ExitError
    if errors.As(err, &exit) {
        log.Printf("exit %d: %s", exit.Result.ExitCode, exit.Result.Stderr)
    }
    return err

default:
    fmt.Println(res.Stdout)
}
```

- A **timeout** (or a `ctx` whose deadline is exceeded) returns
  `process.ErrTimeout`; match it with `errors.Is`.
- A **non-zero exit** returns `*process.ExitError`; reach it with
  `errors.As`. `ExitError.Result` holds `ExitCode`, `Stdout`, `Stderr`,
  `Duration`; `ExitError.Unwrap()` exposes the underlying `os/exec`
  error.
- On the happy path `err == nil` and `res.Successful()` is `true`.

### Combining the two

A scheduled job that shells out is the common case — `TaskFn` already
receives a `context.Context`, so pass it straight through:

```go
s.Job("db-backup", scheduling.DailyAt("03:30"), func(ctx context.Context) error {
    res, err := process.Command("pg_dump", "-Fc", "-f", "/backups/app.dump", "app").
        Env("PGPASSWORD", os.Getenv("DB_PASSWORD")).
        Timeout(30 * time.Minute).
        Run(ctx)
    if err != nil {
        return fmt.Errorf("pg_dump: %w (stderr: %s)", err, res.Stderr)
    }
    return nil
})
```

When the scheduler shuts down via context cancellation, the `pg_dump`
process is signalled to stop too, because it shares that context.

## Production notes

- **Single scheduler replica.** Overlap prevention is in-process. Running
  the same `Runner` in multiple pods fires every job once per pod. Pin the
  scheduler to one replica, or wrap jobs in a distributed lock.
- **Set the host timezone explicitly.** `Daily`, `DailyAt`, `Weekly`,
  `Monthly` use local time. Prefer running in UTC so `DailyAt` values are
  unambiguous across deploys.
- **Validate `DailyAt` strings at build/config time** — they panic on bad
  input. Never pass untrusted/dynamic values.
- **Keep jobs idempotent.** Because a slot is *skipped* (not queued)
  while a previous run is still in flight, and replicas can double-fire,
  jobs should be safe to skip and safe to repeat.
- **Drain on shutdown.** Always call `Stop()` (or cancel `ctx`) and wait
  for `Run` to return before exiting the process, so in-flight jobs
  finish cleanly.
- **`process` never invokes a shell.** If you genuinely need shell
  features (globbing, pipes), call the shell explicitly:
  `process.Command("sh", "-c", script)` — and remember that reintroduces
  injection risk if `script` contains untrusted data.
- **Always read `Result` on error.** `Stderr` and `ExitCode` are
  populated even when `Run` returns `ErrTimeout` or `*ExitError`; they are
  your primary debugging signal.
