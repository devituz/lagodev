# TODO — Laravel-style hardening (laravel-style branch)

Snapshot: 2026-05-30. Existing framework already provides Laravel-grade DX
(`web.App`, `web.Router`, `web.Context`, ORM, migrations, factories,
seeders, Artisan CLI). This TODO lists the **real gaps** to close.

## P0 — Security middleware (secure-by-default) ✅

- [x] `web/security.go`: `SecurityHeaders()` — CSP, X-Frame-Options,
      X-Content-Type-Options, Referrer-Policy, HSTS, Permissions-Policy
- [x] `web/security.go`: `CSRF()` — double-submit cookie, constant-time
      compare, safe-method skip (GET/HEAD/OPTIONS)
- [x] `web/security.go`: `RateLimit(n, window)` — fixed-window per IP,
      `429 Too Many Requests` with `Retry-After`
- [x] `web/security.go`: `BodyLimit(n bytes)` — wrap `r.Body` with
      `http.MaxBytesReader` to prevent DoS via huge payloads
- [x] `web/security.go`: `RequestID()` — generate/forward `X-Request-ID`
      for tracing & log correlation

## P0 — Validation (native, no Gin) ✅

- [x] `web/validate.go`: native `ValidationError` + tag-based `Validate()`
- [x] `web/context.go`: `c.BindAndValidate(dst)` — wires Bind+Validate,
      auto-422 with `{"errors": {field: msg}}`
- [x] Extra rules: `numeric`, `integer`, `ip`, `gt=N`, `lt=N`

## P0 — Server hardening ✅

- [x] `web/app.go`: set `ReadTimeout`, `WriteTimeout`, `IdleTimeout`,
      `MaxHeaderBytes`
- [x] `web/context.go`: `Bind()` applies `MaxBytesReader` (default 1 MiB)
      and `DisallowUnknownFields()`
- [x] `web/context.go`: production-safe `InternalError` —
      `APP_ENV=production` hides raw `err.Error()`

## P0 — Bug fixes uncovered while building secure example ✅

- [x] Double-body write: `Bind()` 400 + `respond()` 500 wrote payload
      twice. Tracked with `bodyWritten` flag.
- [x] `Created()`: status flushed before Content-Type could be set,
      leaving 201 responses as `text/plain`. Replaced with
      `pendingStatus` that defers `WriteHeader`.

## P1 — Laravel parity ✅ (partial)

- [x] `web/cookies.go`: `c.Cookie(name)` / `c.SetCookie(...)` with
      Secure / HttpOnly / SameSite=Lax default-on
- [x] `web/middleware.go`: `Throttle(...)` alias for `RateLimit`
- [ ] `web/router.go`: route names + `Route(name) (Route, ok)` lookup
- [ ] `web/router.go`: `Middleware(...)` chain helper for per-route mw
      (currently achievable via `Group` of one)
- [x] `web/middleware.go`: `CORSWithConfig` — credentials,
      `Access-Control-Max-Age`, echo requested headers
- [x] CORS rejects wildcard + credentials at init (panic)

## P2 — Docs

- [ ] `SECURITY.md`: catalogue all defenses + recommended config
- [ ] `README.md`: link new middleware
- [ ] `examples/secure`: a runnable example demonstrating every guard

## Out of scope for this branch (separate effort)

- Session cookies / signed cookies (need a kv abstraction first)
- WebSocket / SSE
- gRPC adapter
- Sanctum/Passport equivalent

## Push status

If `git push -u origin laravel-style` fails (no remote / auth), commits
stay local — manual push by the maintainer.
