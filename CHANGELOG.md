# Changelog

All notable changes are recorded here. Versions follow [SemVer](https://semver.org/).
Pre-`v1.0.0` releases may include breaking changes between minor versions.

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
