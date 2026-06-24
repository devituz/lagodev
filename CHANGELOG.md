# Changelog

All notable changes are recorded here. Versions follow [SemVer](https://semver.org/).
Pre-`v1.0.0` releases may include breaking changes between minor versions.

## v0.23.0 — 2026-06-24

API-first (Roadmap milestone v0.23.0). Additive.

### Added

- **`openapi` — framework-wide OpenAPI 3.1.** Reflects Go structs into JSON
  Schema (honoring `json` + `validate` tags: required, formats, bounds, enum),
  a `Spec` builder for operations/params/bodies/responses with `$ref`
  components, route harvesting from `web.App`, and `Handler()`/Swagger-UI
  handlers (`/openapi.json` + docs page).
- **`collection` — generic data pipeline.** `Collection[T]` with chainable
  same-type methods (Filter/Reject/Map/Sort/Chunk/Partition/Take/Skip/…) plus
  type-changing free functions (`Map[T,U]`/`Reduce`/`GroupBy`/`KeyBy`/`Pluck`),
  numeric `Sum`/`Avg`/`Min`/`Max`, and an `iter.Seq` bridge. Immutable by
  default.

## v0.22.0 — 2026-06-24

Application core — the architecture layer that turns a pile of packages into a
cohesive app (Roadmap milestone v0.22.0). All additive.

### Added

- **`container` — DI service container.** Generic, type-safe:
  `Bind`/`Singleton`/`Instance` (+ named variants), `Make`/`MustMake`,
  child `Scope()`s with per-scope singleton lifetimes (per-request DI),
  cyclic-dependency detection, and opt-in struct autowiring (`Build`).
  Goroutine-safe; resolve-once verified under `-race`.
- **`app` — application bootstrap + modules.** `app.New().Register(modules…).Run()`
  wires `container` + `web` + `config` + `migrations`. Two-phase `Provider`
  (`Register` then `Boot`), `Module` grouping routes/migrations/commands,
  per-request container scope, graceful `Run`/`Test`.
- **`resource` — API resources / serializers.** Map models to clean JSON
  without hand-mapping: `Resource[Model]`/`Func`, a `Fields` builder
  (`Only`/`Except`/`Rename`/`When`/`OmitEmpty`), `Embed`/`EmbedMany` for nested
  relations, and `Item`/`Collection`/`Paginated` renderers that derive
  `meta`+`links` from `orm.Paginator`. Leaf package, cycle-free.
- **ORM eager loading — `With("posts.comments")`.** `(*Builder[T]).With`/
  `WithWhere` batch-load relations declared on the model
  (`WithRelations`/`RelationDef`) with one query per level (no N+1), honoring
  soft-delete scope and `Tabler`. Verified by exact SELECT-count tests.

## v0.21.0 — 2026-06-24

Framework feature expansion: validation, ORM soft deletes / pagination /
upsert idioms, and atomic cache primitives. All additive and tested.

### Added

- **`validation` package** — dependency-free, Laravel-FormRequest-grade
  request validation. Struct-tag driven (`validate:"required,email,max=255"`)
  plus a `Map`/fluent `Builder` API for validating decoded JSON. Built-in
  rules: required, email, url, min/max/len, gte/lte/gt/lt, numeric, integer,
  alpha, alphanumeric, uuid, boolean, in/notin, regex, datetime, cross-field
  eqfield/nefield/confirmed, and `dive` for nested structs/slices. Returns a
  structured `ValidationErrors` with a `Response()` helper for a 422 JSON body
  (`{"message","errors"}`).
- **ORM soft deletes** — embed `orm.SoftDeletes` (adds `deleted_at`).
  `orm.Delete` then soft-deletes; queries exclude trashed rows by default.
  New: `(*Builder[T]).WithTrashed()`/`.OnlyTrashed()`, `orm.Restore`,
  `orm.ForceDelete`, and `t.SoftDeletes()` in the schema blueprint.
- **ORM pagination** — `(*Builder[T]).Paginate(ctx, page, perPage)` returns
  `Paginator[T]{Data, Total, Page, PerPage, LastPage, HasMore}` (COUNT +
  LIMIT/OFFSET, soft-delete aware); `(*Builder[T]).Chunk(ctx, size, fn)`
  keyset-iterates large sets.
- **ORM write idioms** — `orm.FirstOrCreate`, `orm.UpdateOrCreate`, and a
  reusable `(*Builder[T]).Scope(fn)` for shared query constraints.
- **Cache atomic + batch ops** — `Memory.Add` (put-if-absent lock primitive),
  `Increment`/`Decrement` (race-safe counters), `Forever`, `Many`/`PutMany`,
  exposed as free functions via optional interfaces so custom stores degrade
  gracefully (`ErrUnsupported`).

## v0.20.2 — 2026-06-24

### Security / Dependencies

- `go get -u ./...` + `go mod tidy`: raised the dependency floor so
  consumers inherit patched versions. Notably **`golang.org/x/crypto`
  v0.17.0 → v0.53.0** (closes the old advisories Dependabot flagged),
  plus `jackc/pgx/v5` 5.6.0 → 5.10.0, `go-sql-driver/mysql` 1.8.1 →
  1.10.0, `mattn/go-sqlite3` 1.14.22 → 1.14.47, `spf13/cobra` 1.8.1 →
  1.10.2, `golang-jwt/jwt/v5` unchanged (already current), and the
  `golang.org/x/{sync,sys,text}` set. `govulncheck ./...` reports **no
  vulnerabilities** (reachable or otherwise). All tests pass on go1.26.4.

## v0.20.1 — 2026-06-24

### Security

- Pin the build toolchain to **go1.26.4** (`toolchain` directive in
  `go.mod`). `govulncheck` flagged 14 reachable vulnerabilities, all in the
  Go standard library shipped with go1.26.0–1.26.3 (`net/mail`,
  `net/textproto`, `crypto/x509`, `crypto/tls`, `net/http`, `net/url`,
  `net`, `os`), reached through the mail/web/db code paths. 1.26.4 fixes
  all of them; `govulncheck ./...` is now clean (0 reachable). The
  remaining advisories Dependabot reports are in modules the code does not
  call (not reachable).

## v0.20.0 — 2026-06-24

Framework-grade hardening pass. A full audit of every package (exercised
against real SQLite + httptest, not just snapshot strings) surfaced ~35
confirmed bugs across the DDL, ORM, HTTP, auth, queue, and IO layers.
All are fixed with a dedicated regression test each; the suite is green
under `go test -race ./...`.

### Security

- **Email header injection (critical)** — `mail.Message.Encode` wrote
  `From`/`To`/`Cc`/`Reply-To` and custom headers raw; a CRLF in any
  recipient or header value smuggled arbitrary headers (e.g. a hidden
  `Bcc`). Addresses are now validated via `net/mail` and re-emitted
  canonically; any CR/LF is rejected with the new `mail.ErrHeaderInjection`.
- **Filesystem symlink traversal** — `filesystem` Local disk only did
  lexical `..` checks; a symlink inside the root pointing outside let
  `Get`/`Put` escape. Paths are now resolved with `EvalSymlinks` and
  re-verified under the root (`ErrPathTraversal`).
- **S3 key traversal** — the S3 driver kept leading `..` in object keys
  (tenant-prefix escape); it now rejects `..`/absolute paths like Local.
- **SQL injection via `ORDER BY` direction** — `query.Builder.OrderBy`
  concatenated the direction raw; it is now whitelisted to `ASC`/`DESC`.
- **Session not actually destroyed on logout** — under
  `session.Manager.Middleware`, the deferred save re-wrote a destroyed
  session and re-issued the live cookie. `Destroy` now hard-stops both.
- **JWT hardening** — `Parse` now verifies `iss`/`aud` when configured,
  applies clock-skew leeway, and `ParseAccess`/`ParseRefresh` enforce the
  token type so a refresh token can't be replayed on an access endpoint.
  `auth.New` rejects secrets shorter than 32 bytes.
- **authz Gate panics → DoS** — a mis-typed policy method (wrong
  resource/user type or non-bool return) panicked the request goroutine
  via reflection; signatures are now validated and skipped. Fixed a data
  race on the policy registry.
- **httpclient** unbounded `io.ReadAll` capped (`MaxResponseBytes`,
  default 10 MiB) to prevent OOM on hostile responses.

### Fixed (correctness)

- **schema**: Postgres `enum` compiled to invalid SQL (`CHECK (VALUE IN …)`);
  enum/set now emit a column-named `CHECK` (and are enforced on SQLite too).
  Single-column `.Index()` was silently dropped; `ADD COLUMN … UNIQUE`
  failed on SQLite; `DROP INDEX`/`DROP FOREIGN KEY` used wrong per-dialect
  syntax; duplicate generated index names collided. `Index.Name()` /
  `Foreign.Name()` setters added.
- **orm/query/relations**: `UPDATE` no longer clobbers `created_at`/PK;
  `belongsTo` eager-loading no longer collapses parents that share a
  foreign key (N+1 fix was broken); `NULL` in a non-pointer field no
  longer errors the whole scan; `Count()` is correct with `GROUP BY`;
  query builders no longer leak state across calls (`Clone`).
- **queue/events/scheduling**: a panicking job/listener no longer kills
  the worker/dispatch; `sqlqueue` no longer double-delivers orphaned jobs
  or errors with `SQLITE_LOCKED` under concurrent workers (`BEGIN
  IMMEDIATE` + busy-retry); `Worker.Stop` no longer deadlocks when `Run`
  never started; the scheduler drains in-flight tasks and is restartable.
- **casts/inflect/reflectutil**: nil embedded-pointer base models no
  longer panic on insert; int/bool/date casts accept the types real
  drivers return; `Singularize`/`Pluralize` handle `-ses`/`-ves`/`-sis`/
  `-z`/`-o` cases.
- **database/cache/session**: closed-connection guards; negative cache
  TTL is now an immediate miss (was "forever").
- **web**: SSE/streaming now flushes through the default Logger
  middleware; `c.Status(n)` + a returned value keeps the right
  `Content-Type`; access logs record the real status/size.

### Added

- `auth`: `ParseTyped`/`ParseAccess`/`ParseRefresh`, `Config.Audience`,
  `Config.Leeway`, `Config.AllowWeakSecret`.
- `queue.Worker.OnFailed` dead-letter callback; workers/scheduler restartable.
- `casts`: `float`/`datetime` casts; `factory.WithSeed`/`WithFaker` for
  reproducible test data.
- `httpclient.MaxResponseBytes`; `query.Builder.Clone`/`FirstOrFail`;
  `web.WithoutDefaultMiddleware`.

### Breaking

- `auth.New` now errors on secrets < 32 bytes (set `Config.AllowWeakSecret`
  to opt out).
- `mail.Message.Encode` / mailers return `mail.ErrHeaderInjection` for
  malformed addresses or CRLF instead of emitting them.
- `web.SecurityHeadersConfig.NoSniff` → `DisableNoSniff` (inverted; the
  zero value now keeps `X-Content-Type-Options: nosniff` on).
- `query.Builder.OrderBy` panics on a direction other than `ASC`/`DESC`.
- `filesystem` Local/S3 operations return `ErrPathTraversal` for `..` or
  symlink escapes.

## v0.19.0 — 2026-06-04

Framework polish + interactive deploy stack. Two themes:
(1) the `web` package now ships the helpers a production app actually
needs out of the box, and (2) `lago new` generates a fully-Dockerised,
Kubernetes-ready project from a guided picker.

### Added

- **`web` streaming + helper methods**:
  - `c.Redirect(code, url)` — 30x with `Location` header. Clamps
    non-30x codes to 302.
  - `c.File(path)` / `c.Attachment(path, filename)` — serves a file
    with `inline` or `attachment` `Content-Disposition`.
  - `c.Stream(code, contentType, r)` — writes through an `io.Reader`
    with periodic flush via `http.Flusher`; ideal for CSV export
    style endpoints.
  - `c.SSE(event, data)` — Server-Sent Events frame (multi-line
    `data:` lines spec-compliant). First call sets the SSE
    headers; subsequent calls just append events.
- **`web.App.Health()` + `web.App.Ready(checks…)`** — production
  liveness / readiness endpoints. `Ready` runs every supplied
  `HealthCheck` in parallel with a per-probe timeout (default 2s),
  returns 200 on all-clear or 503 + the first failing check's name.
- **`web.App.Test(req)`** — drives a request through the full
  middleware chain + route table without binding a port. Returns
  `*httptest.ResponseRecorder`. Eliminates the
  `httptest.NewServer` boilerplate every handler test was carrying.
- **`lago new` is now an interactive scaffolder**.
  - Promptui-driven menu (Arrow keys) for:
    - Primary database: `postgres / mysql / sqlite / none`
    - Cache / queue: `redis / none`
    - Object storage: `minio / none`
  - Non-interactive mode via `--yes` + per-axis flags (`--db`,
    `--cache`, `--storage`) — composes with the existing
    `--framework` / `--module`.
  - Preflight checks for `go` / `git` / `docker` / `docker-compose`
    / `kubectl` and prints a friendly hint for any that's missing
    (never blocks the scaffold).
  - Each project now ships with **Dockerfile + .dockerignore +
    docker-compose.yml + k8s/{deployment,service,configmap,secret,
    kustomization}.yaml**, all conditional on the picks above.
  - `.env` only contains the variables the picked services actually
    need.
  - `main.go` template wires `web.RequestID()`, `web.SecurityHeaders()`,
    `app.Health()` and `app.Ready(db ping)` automatically.
- **`lago make:scheme <Name>`** — generates a DTO under `schemes/`
  with `validate:"…"` tags so controllers can plug it straight into
  `c.BindAndValidate(&dst)`. Pascal-cases the struct, snake-cases
  the file.
- **Deploy artefacts** (all rendered via `text/template` from
  `cli/cmd/stubs_deploy.go`):
  - **Dockerfile**: multi-stage, statically-linked binary built on
    `golang:1.25-alpine`, runtime on `gcr.io/distroless/static-
    debian12:nonroot`. `-trimpath` + `-s -w`, BuildKit-cached
    module + build caches, no shell in the final layer.
  - **docker-compose.yml**: only the picked services appear,
    each with its own `healthcheck`, a named bridge network, and
    persistent volumes. `app` waits for `service_healthy` before
    starting.
  - **k8s manifests**: production-shaped — `runAsNonRoot`,
    `readOnlyRootFilesystem`, `drop: ["ALL"]` caps,
    `RollingUpdate` with `maxUnavailable: 0`, CPU/mem requests +
    limits, `readinessProbe` on `/readyz` and `livenessProbe` on
    `/healthz`. Includes a `kustomization.yaml` so
    `kubectl apply -k k8s/` Just Works.

### Changed

- **`Logger` middleware output**. Format now: `METHOD PATH STATUS
  DURATION ip=IP size=BYTES req=ID`. Captures response size via
  the wrapping writer, surfaces the `RequestID` middleware's id
  for log correlation, and includes the client IP.
- **`Recovery` middleware no longer leaks the recovered value to
  the client.** Previously the response carried
  `internal server error: <recovered value>`, which could expose
  secrets if a handler panicked with one. The client now sees a
  generic `"internal server error"`; the recovered value goes only
  to the configured logger alongside the method/path and stack.
- **Startup / shutdown log lines are now English** (`listening on
  …`, `signal received …`, `registered routes:`). Previously
  Uzbek, which looked odd in mixed-language teams' log aggregators.

### Notes

- Main module added one dependency: `github.com/manifoldco/promptui`
  for the interactive `lago new` prompts. It is only pulled in by
  the CLI binary, not by application code that imports `web`/`orm`/
  etc.
- 12+ new tests across `web` (streaming helpers + Recovery
  info-leak guard + health/ready/Test) and `cli/cmd`
  (template-rendering matrix across infra combinations).
- `go test ./...` and `go vet ./...` clean across all 35 packages.

## v0.18.1 — 2026-06-03

Re-release of v0.18.0. The `v0.18.0` tag was pushed against the wrong
commit (it points at the docs-onboarding state, missing the four new
packages). The tag stays in place to preserve module-proxy
immutability; `v0.18.1` is the **first usable release of wave 5**.
Always pin `v0.18.1` (or newer).

Wave 5 of Laravel-equivalent infrastructure. Transactional email via
HTTP APIs, Redis-backed pub/sub and queue, plus a gRPC adapter that
mirrors the `web` package's auth/recovery/logging middleware.

### Added

- **`mail/mailgun` sub-package** — Mailgun v3 HTTP API mailer.
  - `mailgun.New(mailgun.Config{Domain, APIKey, From, Region, …})`
    returns a `mail.Mailer`. Use as a drop-in replacement for
    `SMTPMailer` when port 25/465/587 is blocked (Heroku, App
    Engine, Cloud Run, corporate firewalls).
  - Streams multipart/form-data so attachments don't double-buffer
    in memory.
  - Honours per-message `Headers` via Mailgun's `h:`-prefix
    convention; passes `Reply-To` correctly.
  - `RegionUS` / `RegionEU` constants for the two Mailgun
    endpoints.
- **`mail/sendgrid` sub-package** — SendGrid v3 HTTP API mailer.
  - `sendgrid.New(sendgrid.Config{APIKey, From, …})`.
  - Builds a typed JSON payload (`personalizations`, `content`,
    `attachments`) matching the SendGrid spec exactly.
  - Base64-encodes attachments as the v3 API requires.
- **`drivers/redis` module** (separate Go module — opt-in) — Redis-
  backed implementations of `broadcasting.Broadcaster` and
  `queue.Queue` in a single module so apps only depend on the
  redis client once.
  - `redis.NewBroadcaster(rdb, …)` — Redis Pub/Sub fan-out with
    optional `WithPrefix("app-a")` namespacing and a saturation
    metric (`Dropped()`).
  - `redis.NewQueue(rdb, name, …)` — list-backed queue with
    delayed delivery via a `delayed` sorted set, at-least-once
    semantics via a `reserved` sorted set, automatic orphan
    recovery on visibility-timeout expiry.
  - Both ship in-box tests against `miniredis` so CI stays
    hermetic.
  - Install: `go get github.com/devituz/lagodev/drivers/redis@latest`.
- **`adapters/grpc` module** (separate Go module — opt-in) — gRPC
  server with the same Recovery + Auth + Logging middleware idea as
  `web`.
  - `lagogrpc.New(lagogrpc.Options{…})` returns a `Server` with a
    `GRPC() *grpc.Server` accessor for `RegisterFooServiceServer`.
  - `AuthFromManager(authMgr)` reuses the lagodev/auth `Manager`
    you already configured for HTTP — the same JWT lights up gRPC
    auth, surfacing `UserID(ctx)` / `Role(ctx)` / `Claims(ctx)` on
    the handler context.
  - Graceful shutdown on SIGINT/SIGTERM with
    `WithShutdownTimeout`-style cap.
  - Built-in unary + stream interceptors for Recovery (panic →
    `codes.Internal`) and Logging (`method status duration` per
    RPC).
  - Install:
    `go get github.com/devituz/lagodev/adapters/grpc@latest`.

### Tests

- 50+ new tests across the four new packages. The grpc adapter
  uses `bufconn` so no real port is bound; the mail adapters use
  `httptest`; the Redis driver uses miniredis. `go test ./...` and
  `go vet ./...` clean across all 35 packages in the main module
  plus the four adapter modules.

### Notes

- Main module **still added zero new external dependencies** in
  this release. The four new opt-in adapter modules pull
  `github.com/redis/go-redis/v9`, `google.golang.org/grpc`, and
  their transitive deps only when imported.

## v0.17.0 — 2026-06-03

Wave 4 of Laravel-equivalent infrastructure. Real-time + testing +
durable queues.

### Added

- **`broadcasting` package** — pub/sub abstraction.
  - `Broadcaster` interface (`Publish`/`Subscribe`/`Close`) so
    Redis/NATS/Kafka drivers can plug in later without touching call
    sites.
  - `NewMemory(...)` in-process driver with per-subscription bounded
    outbox and background goroutine. Bursts beyond the buffer are
    dropped and surfaced via `Dropped()` as a saturation metric.
- **`mock` package** — small testing helpers.
  - `Clock` — controllable time source (`Advance`, `Set`).
  - `Calls[T]` — generic call recorder with `Record`/`Count`/`At`/
    `Last`/`All`/`AssertCount`/`AssertCalled`/`AssertNotCalled`.
  - `HTTPServer` — programmable `httptest.Server` with route-by-
    `(method, path)` registration, captured `RecordedRequest`
    history, and `OnJSON`/`ReplyJSON` shortcuts.
- **`queue/sqlqueue` package** — DB-backed Queue driver that survives
  restarts and works across replicas.
  - Uses lagodev's own `database.Connection` — works with Postgres,
    MySQL, and SQLite out of the box (dialect-aware schema).
  - `Setup(ctx)` creates the `jobs` table (and an index on
    `available_at`).
  - Reservation via `BEGIN; SELECT ... FOR UPDATE SKIP LOCKED;
    UPDATE reserved_at = now(); COMMIT;` on Postgres/MySQL,
    plain transactional select on SQLite.
  - Visibility-timeout-based orphan recovery — if a worker dies
    mid-job the row becomes eligible again automatically.
- **`adapters/websocket` module** (separate module — opt-in).
  - `Hub` orchestrates connections; `Connection.Join("room")` /
    `Leave("room")` and `Hub.Broadcast(ctx, channel, payload)` model
    Laravel Echo channels.
  - `Hub.Handler(onMessage)` returns a stdlib `http.Handler` so the
    adapter drops into Gin, Fiber, Chi, Echo, plain net/http, and
    lagodev's `web` package without changes.
  - Per-connection bounded outbox + ping keep-alive; `Dropped()`
    counter for back-pressure visibility.
  - Built on `github.com/coder/websocket` (formerly nhooyr) —
    stdlib-friendly, minimal transitive deps.
  - Install: `go get github.com/devituz/lagodev/adapters/websocket@latest`.

### Tests

- 40+ new tests (broadcasting, mock, sqlqueue, websocket end-to-end
  with real client/server handshake + frame I/O).
- `go test ./...` and `go vet ./...` clean across all 33 packages.

### Notes

- Main module still added zero new external dependencies in this
  release. The WebSocket adapter brings `coder/websocket` but lives in
  its own module.

## v0.16.0 — 2026-06-03

Wave 3 of Laravel-equivalent infrastructure. Five new core packages
covering email, background jobs, scheduling, internationalisation, and
multi-channel notifications. Main module took no new external deps.

### Added

- **`mail` package** — Laravel-style Mailer.
  - `NewMessage()` fluent Builder: `From/To/Cc/Bcc/ReplyTo/Subject/
    Text/HTML/Header/Attach`.
  - `SMTPMailer` talks net/smtp (STARTTLS handled when the server
    offers it; honours `ctx` cancellation).
  - `LogMailer` captures sent messages in memory — drop into tests
    and dev to skip real delivery.
  - Encode produces RFC 5322 output: multipart/alternative (text+html),
    multipart/mixed (attachments), base64-wrapped attachments, MIME
    `Q`-encoded Subject for non-ASCII, quoted-printable bodies.
- **`queue` package** — typed job queue.
  - `Job{ID, Name, Payload, Attempts, AvailableAt}` wire format.
  - `Queue` interface (`Push`/`Pop`/`Ack`/`Nack`/`Len`).
  - `MemoryQueue` ships in-box (FIFO with delayed delivery,
    at-least-once via Ack/Nack, sync.Mutex-backed).
  - `Dispatch[T]` / `DispatchAfter[T]` JSON-encode a typed payload
    and push it with the job name derived from the Go type.
  - `Worker` consumes jobs, dispatches to handlers registered via
    `Handle[T]`, with configurable `MaxRetry`, exponential `Backoff`,
    and `Poll` interval. Jobs without a handler are dropped (Acked).
- **`scheduling` package** — wall-clock task scheduler.
  - Schedules: `Every(d)`, `EveryMinute`, `EveryFiveMinutes`,
    `Hourly`, `Daily`, `DailyAt("HH:MM")`, `Weekly`, `Monthly`.
  - `Runner` ticks once per second by default; overlapping runs of
    the same task are prevented automatically.
  - `Tasks()` returns a snapshot for status pages / management
    endpoints.
- **`i18n` package** — Laravel `Lang::get` parity.
  - `Load(dir, defaultLocale)` reads `*.json` files (basename = locale).
    `LoadFS(fsys, …)` is the embedded-FS variant.
  - Flat keys plus nested-JSON flattening (`{"auth":{"failed":"…"}}`
    becomes `auth.failed`).
  - `:placeholder` substitution and `:count`-aware `Choice` for
    plural forms (`zero` / `one` / `other`).
  - Missing-key fallback to a configurable locale; ultimate fallback
    echoes the key for easy debugging.
- **`notifications` package** — multi-channel dispatch.
  - `Notification` describes channels + payload; `Notifiable` knows
    its per-channel routing; `Channel` is the transport interface.
  - `MailChannel` adapts `mail.Mailer` so any notification
    implementing `ToMail(recipient) mail.Message` delivers through
    email.
  - `Manager.Send` collects per-channel errors via `errors.Join` —
    one failing channel does not abort the rest. Missing channels and
    empty routes are silently skipped.

### Tests

- 70+ new tests across the five packages. `go test ./...` and `go vet
  ./...` clean across all 30 packages now in the module.

## v0.15.0 — 2026-06-03

Wave 2 of Laravel-equivalent infrastructure. Five new core packages
plus an optional S3/MinIO driver (separate module).

### Added

- **`filesystem` package** — `Disk` abstraction + `Local` driver.
  - `Disk` interface: `Put`, `PutStream`, `Get`, `Reader`, `Exists`,
    `Delete`, `Copy`, `Move`, `Files`, `Stat`.
  - `NewLocal(root)` writes atomically (temp + rename), creates
    intermediate directories, and rejects any `..` segment up-front so
    `../etc/passwd` returns `ErrPathTraversal` instead of being silently
    rewritten.
- **`filesystem/s3` package** (separate Go module — opt-in) — S3-
  compatible driver works with **AWS S3, MinIO**, DigitalOcean Spaces,
  Cloudflare R2, Wasabi, Backblaze B2, Ceph RGW. Same `Disk` interface,
  so call sites do not change between Local and S3.
  - `s3.New(s3.Config{Endpoint, AccessKey, SecretKey, Bucket, Region,
    UseSSL, Prefix})` — MinIO: `UseSSL: false` and `Endpoint:
    "minio.local:9000"`.
  - Install: `go get github.com/devituz/lagodev/filesystem/s3@latest`.
  - Integration tests run only when `S3_ENDPOINT` is set so CI stays
    hermetic.
- **`process` package** — `os/exec` wrapper.
  - `Command(name, args...).Dir().Env().Timeout().Stdin().Run(ctx)`.
  - `Result{ExitCode, Stdout, Stderr, Duration}`; `ErrTimeout` returned
    when wall-clock cap or context deadline is hit. Non-zero exits
    surface via `*ExitError` (works with `errors.As`).
  - No shell invocation — argv is passed verbatim, killing the obvious
    "shell injection from interpolated string" class of bug.
- **`authz` package** — `Gate[U]` + `Policy[R]` authorisation.
  - `Define[U, R](g, ability, fn)` registers a typed closure.
  - `Policy[R](g, policyStruct)` routes ability names to methods on
    a struct (case-insensitive). Methods may return `bool` or
    `(bool, error)`.
  - `Before[U](g, fn)` short-circuits to allow when, e.g., the user is
    a superadmin.
  - `Allows`, `Denies`, `Authorize` (returns `ErrDenied`), `Check`
    (returns a renderable `Decision`).
- **`carbon` package** — ergonomic `time.Time` helper.
  - `Now`, `Parse` (tries RFC3339, SQL datetime, ISO date, …),
    `MustParse`.
  - Add* / Sub* helpers (`AddDays`, `AddMonths`, …) and boundaries
    (`StartOfDay`, `EndOfMonth`, `EndOfYear`).
  - `DiffForHumans(other)` renders "5 minutes ago", "2 weeks from now",
    pluralised correctly.
- **`session` package** — cookie-backed sessions with a pluggable
  store.
  - `MemoryStore` ships in-box (sync.RWMutex, lazy expiry, background
    sweeper). Replace with Redis/DB by implementing the `Store`
    interface.
  - `Session.Put`, `Get`, `GetString`, `Forget`, `Flush`, `Regenerate`
    (mitigates session fixation), `Destroy`, `Save`.
  - **`Manager.Middleware()` returns a framework-agnostic
    `func(http.Handler) http.Handler`** so the same middleware drops
    into Gin, Fiber, Chi, Echo, or plain `net/http` — not tied to
    lagodev's `web` package.
  - `session.FromRequest(r)` / `FromContext(ctx)` for handler access.
  - `Options.Insecure` (inverted default) — cookies are Secure +
    HttpOnly + SameSite=Lax out of the box.

### Tests

- 60+ new tests across the five core packages plus the s3 module
  (unit + skipped-by-default integration). `go test ./...` and
  `go vet ./...` clean.

### Notes

- Main module took **no new external dependencies**. The S3 driver
  brings in `github.com/minio/minio-go/v7` but lives in its own
  module so the core stays minimal.

## v0.14.0 — 2026-05-30

First wave of Laravel-equivalent infrastructure packages. None of these
required new external dependencies.

### Added

- **`crypt` package** — symmetric encryption + signing.
  - `crypt.GenerateKey()` / `GenerateKeyString()` — 32-byte AES-256 keys
    (the second returns the `base64:` form used in `APP_KEY`).
  - `crypt.Encrypt` / `Decrypt` — authenticated AES-256-GCM with a
    fresh random nonce per call; tampering or wrong-key attempts return
    `ErrCiphertextMalformed`.
  - `crypt.Sign` / `Verify` — HMAC-SHA256 with constant-time compare for
    stateless tokens and signed URLs.
- **`cache` package** — `Store` interface + in-memory driver.
  - `cache.NewMemory()` — sync-safe map with lazy expiry plus a
    background sweeper. `Put` stores a copy so callers cannot mutate
    cached values.
  - Helpers: `Remember(ctx, store, key, ttl, fn)` (cache-aside pattern)
    and `Pull` (read-and-remove).
- **`events` package** — type-safe in-process event dispatcher.
  - `events.Listen[E](d, fn)` registers a typed listener.
  - `events.Dispatcher.Dispatch(ctx, event)` fans out synchronously,
    joining listener errors via `errors.Join`. Optional
    `StopOnError(true)` short-circuits on the first failure.
  - `HasListeners[E]` / `Forget[E]` for introspection and cleanup.
- **`httpclient` package** — fluent HTTP client.
  - Chainable builder: `BaseURL`, `Header`, `Headers`, `BearerToken`,
    `BasicAuth`, `Query`, `Timeout`, `Retry`, `Backoff`, `Transport`.
  - Methods: `Get`, `Delete`, `PostJSON`, `PutJSON`, `PatchJSON`.
  - Automatic retry with exponential backoff on transport errors,
    `429`, and 5xx responses.
  - `Response` exposes `Status` / `OK` / `Header` / `Body` / `String` /
    `JSON(dst)`; body is fully read and the underlying `*http.Response`
    is closed before the helper returns.
- **`lago key:generate`** Artisan command (and `artisan key:generate`).
  - Generates a fresh `base64:<32-byte>` key and writes it to `APP_KEY`
    in `.env` while preserving other lines.
  - Flags: `--env <path>`, `--force`, `--show`, `--print-only`.
  - Refuses to overwrite a non-empty `APP_KEY` unless `--force` is
    passed.

### Tests

- 31 new tests across `crypt`, `cache`, `events`, `httpclient`, and the
  `key:generate` command. `go test ./...` and `go vet ./...` clean.

## v0.13.0 — 2026-05-30

### Added

- **Secure-by-default middleware** in the native `web` package:
  - `web.SecurityHeaders()` — CSP, X-Frame-Options,
    X-Content-Type-Options, Referrer-Policy, Permissions-Policy, optional
    HSTS. Configurable via `SecurityHeadersConfig`.
  - `web.CSRF()` — double-submit cookie with constant-time compare
    (`crypto/subtle`). Skips safe methods. Configurable cookie name,
    header name, form field, `Secure`/`SameSite`/`MaxAge`.
  - `web.RateLimit(n, window)` and `web.Throttle(...)` — fixed-window
    per-IP limiter with `Retry-After`. Background GC of stale buckets.
  - `web.BodyLimit(n bytes)` — wraps `r.Body` with `http.MaxBytesReader`
    to prevent payload-DoS.
  - `web.RequestID()` — generates or echoes `X-Request-ID` for tracing
    and log correlation.
- **Native validation** (`web/validate.go`) — no Gin dependency:
  - `web.Validate(dst)` — struct-tag rule engine.
  - `c.BindAndValidate(&dst)` — wires `Bind` + `Validate` + auto-422
    response with `{"error": "validation failed", "errors": {...}}`.
  - Rules: `required`, `min=N`, `max=N`, `gt=N`, `lt=N`, `email`, `url`,
    `oneof=a b c`, `alpha`, `alphanumeric`, `uuid`, `numeric`, `integer`,
    `ip`.
  - `c.UnprocessableEntity(*ValidationError)` helper.
- **Cookies** (`web/cookies.go`) — `c.SetCookie`, `c.Cookie`,
  `c.ClearCookie` with `HttpOnly` / `Secure` / `SameSite=Lax` defaults.
  Opt-outs via `CookieInsecure()`, `CookieReadable()`, `CookieSameSite()`,
  `CookieMaxAge()`, `CookiePath()`, `CookieDomain()`, `CookieExpires()`.
- **Hardened CORS** — `web.CORSWithConfig(CORSConfig{...})` supports
  `AllowCredentials`, `AllowedMethods`, `AllowedHeaders`, `ExposedHeaders`,
  `MaxAgeSeconds`. Refuses to start with wildcard origin +
  `AllowCredentials: true` (panics at init).
- **`examples/secure`** — runnable demo of the full stack
  (`SecurityHeaders` + `BodyLimit` + `RateLimit` + `CORS` + validation).
- **`SECURITY.md`** — defenses catalogued by layer, recommended
  middleware stack, vulnerability-reporting policy.

### Changed

- `web.App.Run()` now sets `ReadTimeout` (30s), `WriteTimeout` (30s),
  `IdleTimeout` (120s), and `MaxHeaderBytes` (1 MiB) — not just
  `ReadHeaderTimeout`. Protects against slow-write / resource-hold
  attacks.
- `c.Bind()` always applies `http.MaxBytesReader` with `DefaultBodyLimit`
  (1 MiB) when no `BodyLimit` middleware is active, and decodes with
  `DisallowUnknownFields()` to block mass-assignment surprises.
- `c.InternalError(err)` respects `APP_ENV=production` and replaces the
  raw error message with a generic `"internal server error"`. Dev mode
  unchanged.
- `c.Error(err)` recognises `*ValidationError` and maps it to HTTP 422.

### Fixed

- Double-body write: `c.Bind()` wrote a 400 then `respond()` wrote a
  second 500 over it because there was no body-written tracking. New
  `bodyWritten` flag on `Context` short-circuits both paths.
- `c.Created()` flushed `WriteHeader(201)` eagerly, which made
  `Content-Type` unsettable; 201 responses landed as `text/plain`.
  Replaced with `pendingStatus` that defers `WriteHeader` until the body
  is written by `JSON/String/respond`.
- README quick tour referenced the removed `t.SoftDeletes()` schema
  builder.

## v0.10.0 — 2026-05-22

### Added

- **`adapters/gin` — official Gin adapter (separate module).** Brings
  the Laravel-style DX from the `web` package to Gin users without
  forcing `gin` as a dependency of the main module.
  - `lagogin.H` — wraps `func(*Ctx) (any, error)` into a
    `gin.HandlerFunc` with automatic status mapping
    (`orm.ErrNotFound` → 404, `*ValidationError` → 422, other errors
    → 500, `nil` → 204, value → 200).
  - `lagogin.Resource(r, "posts", ctrl)` — one-liner that registers
    the 5 canonical RESTful routes and records them in a global
    registry for OpenAPI introspection.
  - `lagogin.AuthJWT(manager)`, `Auth()`, `CORS()`, `RequestTimeout()`
    — middleware bundle. `Ctx.UserID()` / `Ctx.Role()` accessors for
    JWT claims.
  - `lagogin.Paginate[T]` — Laravel-style `Page{Data,Total,Page,
    PerPage,LastPage,From,To}` envelope driven by `?page=&per_page=`.
  - `lagogin.Validate` + `Ctx.BindAndValidate(&dto)` — struct-tag
    validator (`required`, `min`, `max`, `email`, `url`, `oneof`,
    `alpha`, `alphanumeric`, `uuid`) that maps failures to 422 +
    `{"errors": {...}}`.
  - `lagogin.QueryLog(conn)` + `Instrument(conn)` — per-request SQL
    counter surfaced as `X-DB-Query-Count` header, with N+1 warning
    above a configurable threshold.
  - `lagogin.OpenAPI(info)` + `ServeOpenAPI(r, info)` — generates a
    3.0 spec from the Resource registry and mounts `/openapi.json`
    plus a Swagger UI at `/docs`.
  - Install: `go get github.com/devituz/lagodev/adapters/gin@v0.10.0`.
- **`lago new <name> --framework=web|gin`** — full project scaffolder
  that emits `main.go`, `go.mod`, `.env`, `lago.json`, `config/`,
  `routes/`, and stub package directories. The Gin variant wires up
  `lagogin.CORS`, `QueryLog`, and `Resource()` out of the box;
  generated `main.go` loads `.env`, `.env.local`, `.env.$APP_ENV`,
  and blank-imports `migrations/` and `seeders/` so generated init()
  hooks run automatically.
- **`lago make:controller --framework=gin`** — emits a controller
  bound to the lagogin adapter, including `Paginate[T]` on Index and
  `BindAndValidate` on Store.
- **`lago make:model --framework=gin`** — propagates the flag down
  so `-c` (or `-a`) generates a Gin-flavored controller.

### Changed

- **Model stub is now minimal.** Generated models no longer carry
  `column:"…"` tags; the reflection cache derives column names from
  field names automatically (`Email` → `email`, `PasswordHash` →
  `password_hash`). Existing models keep working — explicit tags
  still override the auto-derivation. Example:

  ```go
  // before (v0.9.0):
  type User struct {
      orm.Model
      Email string `column:"email"`
      Name  string `column:"name"`
  }

  // after (v0.10.0 stub):
  type User struct {
      orm.Model
      Email string
      Name  string
  }
  ```

- **`examples/gin/main.go` rewritten.** 179 → ~130 lines; every
  `if err != nil { c.JSON(...) }` block removed in favor of
  `lagogin.H` and `Resource()`. Demonstrates Paginate, AuthJWT,
  ServeOpenAPI, and QueryLog end-to-end.
- `inflect.Pascal` preserves existing case when the input has no
  underscores and contains at least one uppercase letter.
  `Pascal("TagService")` now returns `"TagService"` (previously
  `"Tagservice"`), so make:service/factory/controller stop emitting
  duplicated suffixes like `tagservice_service.go`.
- `--fields=foo:string:default('bar')` now compiles as valid Go.
  The migration generator quotes the default through a small
  helper (`'bar'` → `"bar"`, numeric/bool literals pass through).

### Fixed

- Scaffold project's `main.go` now loads the `.env` chain so DB
  configuration from environment files actually reaches
  `database.Open`. Previously the scaffolded binary fell back to
  driver defaults and ignored `DB_DATABASE`, `DB_HOST`, etc.

### Dependencies

- New optional dependency: `github.com/gin-gonic/gin v1.10.0` (only
  required when importing `adapters/gin`; the main module is
  unchanged).

## v0.9.0 — 2026-05-18

### Added

- `auth` package — framework-agnostic JWT signing/parsing (HS256) and
  bcrypt password helpers. `auth.Manager` issues short-lived access and
  long-lived refresh tokens with typed `Claims{UserID, Role, Type}`.
- `web.AuthJWT(manager)` — middleware that verifies JWTs from the
  `Authorization: Bearer ...` header and populates `auth_user_id`,
  `auth_role`, and `auth_claims` on the request context.
- New dependency: `github.com/golang-jwt/jwt/v5` (HMAC-SHA256 signing).

## v0.8.0 — 2026-05-15

### Breaking

- `web.Handler` is now `func(c *Context) (any, error)`. Controllers
  generated by `lago make:controller` use the new signature; the
  framework auto-converts return values into JSON responses.

### Added

- `Context.respond(value, err)` — automatic response mapping:
  - `orm.ErrNotFound` → 404 with `{"error": "..."}`
  - other errors → 500 with `{"error": "..."}`
  - `(nil, nil)` → 204 No Content
  - `(value, nil)` → 200 + JSON (or whatever `Status()` was called with)
- `Context.Created(v)` — sugar for the 201 Created flow.

### Migration

```go
// before (v0.7.x)
func (c *PostController) Show(ctx *web.Context) {
    p, err := c.Service.Get(ctx.Ctx(), ctx.ParamUint("id"))
    if ctx.Error(err) { return }
    ctx.JSON(200, p)
}

// after (v0.8.0)
func (c *PostController) Show(ctx *web.Context) (any, error) {
    return c.Service.Get(ctx.Ctx(), ctx.ParamUint("id"))
}
```

Regenerate your controllers with `lago make:controller --force`, or
edit them manually.

## v0.7.0 — 2026-05-15

### Added

- `web/` package — Laravel-style HTTP framework (no Gin/Fiber/Echo
  dependency). `web.App`, `web.Router`, `web.Context`, built-in
  Logger/Recovery/CORS/Auth middleware.
- `lago init` now scaffolds `config/`, `routes/`, and `.env` alongside
  `lago.json`.
- Generated controllers use `*web.Context` instead of `net/http`.

## v0.6.0 — 2026-05-15

### Added

- `router/` package — framework-agnostic Laravel-style router (later
  superseded by `web/` in v0.7.0, kept as a low-level option).
- `router.Resource(name, ctrl)` registers the five canonical REST
  routes in one call.

### Fixed

- Migration stub now includes `t.SoftDeletes()` to match the
  `DeletedAt` column on `orm.Model`.

## v0.5.0 — 2026-05-15

### Added

- Examples for Gin, Fiber, Echo, Chi (each in its own `go.mod`).
- `examples/blog/` — full showcase with 3 models, FK relations,
  factories, seeders, services, and controllers.
- New documentation files: `CLI.md`, `ORM.md`, `MIGRATIONS.md`,
  `FACTORIES.md`, `CONFIGURATION.md`, `FRAMEWORK_INTEGRATION.md`.
- Root `go.work` file for local development across examples.

## v0.4.0 — 2026-05-15

### Added

- Framework-agnostic service layer scaffolded by `lago make:service`.
- `lago make:controller` now generates a controller that wraps a
  service; cross-package import paths computed automatically.
- `lago make:model -c` produces service + controller together.
- `.env` chain loading: `.env`, `.env.local`, `.env.$APP_ENV`.
- `lago env` and `lago env:init` commands.

## v0.3.0 — 2026-05-15

### Added

- `lago make:controller`, `db:show`, `db:table`, interactive `db` SQL
  prompt.
- `migrate:fresh --seed`, `migrate --path`, `db:seed --class` for
  Laravel parity.
- Second binary `cmd/lago` (alongside `cmd/artisan`).

## v0.2.0 — 2026-05-15

### Added

- Timezone support: `Config.TimeZone` propagates to the SQLite/MySQL/
  Postgres DSN; `conn.Location()` / `conn.Now()` give the ORM a
  timezone-aware "now".
- `--fields=name:type[:modifier]` flag on `make:model` / `make:migration`
  / `make:factory` — same spec generates matching model, migration,
  and factory definitions in lockstep.
- `lago make:crud` — one-shot model + migration + factory + seeder +
  test.
- Per-project `lago.json` config with custom directory layout.
- Generated `.go` files are `gofmt`'d before writing.

## v0.1.x — 2026-05-15

- v0.1.0: initial public release (modules + ORM + migrations + CLI).
- v0.1.1: fixed stub import paths after module rename.
- v0.1.2: factory generator imports the model package automatically.
- v0.1.3: Laravel-style migration timestamps (`YYYY_MM_DD_HHMMSS`).
