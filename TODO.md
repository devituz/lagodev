# TODO — Laravel-style hardening (laravel-style branch)

Snapshot: 2026-05-30. Existing framework already provides Laravel-grade DX
(`web.App`, `web.Router`, `web.Context`, ORM, migrations, factories,
seeders, Artisan CLI). This TODO lists the **real gaps** to close.

## P0 — Security middleware (secure-by-default)

- [ ] `web/security.go`: `SecurityHeaders()` — CSP, X-Frame-Options,
      X-Content-Type-Options, Referrer-Policy, HSTS, Permissions-Policy
- [ ] `web/security.go`: `CSRF()` — double-submit cookie, constant-time
      compare, safe-method skip (GET/HEAD/OPTIONS)
- [ ] `web/security.go`: `RateLimit(n, window)` — token bucket per IP,
      `429 Too Many Requests` with `Retry-After`
- [ ] `web/security.go`: `BodyLimit(n bytes)` — wrap `r.Body` with
      `http.MaxBytesReader` to prevent DoS via huge payloads
- [ ] `web/security.go`: `RequestID()` — generate/forward `X-Request-ID`
      for tracing & log correlation

## P0 — Validation (native, no Gin)

- [ ] `web/validate.go`: port `ValidationError` + tag-based `Validate()`
      from `adapters/gin/validate.go`
- [ ] `web/context.go`: `c.BindAndValidate(dst)` — wires Bind+Validate,
      auto-422 with `{"errors": {field: msg}}`

## P0 — Server hardening

- [ ] `web/app.go`: set `ReadTimeout`, `WriteTimeout`, `IdleTimeout`
      (not just `ReadHeaderTimeout`)
- [ ] `web/context.go`: `Bind()` should use `MaxBytesReader` (default 1 MiB)
- [ ] `web/context.go`: production-safe `InternalError` — never leak raw
      `err.Error()` when `APP_ENV=production`

## P1 — Laravel parity

- [ ] `web/cookies.go`: `c.Cookie(name)` / `c.SetCookie(...)` with
      Secure / HttpOnly / SameSite=Lax default-on
- [ ] `web/middleware.go`: `Throttle(...)` alias for `RateLimit` (Laravel name)
- [ ] `web/router.go`: route names + `Route(name) (Route, ok)` lookup
- [ ] `web/router.go`: `Middleware(...)` chain helper for per-route mw
- [ ] `web/middleware.go`: `CORS` — credentials, `Access-Control-Max-Age`,
      echo requested headers
- [ ] CORS denied-origin returns 403 instead of silently dropping headers

## P1 — CORS hardening

- [ ] Tighten default CORS to a strict allow-list (already supported);
      reject wildcard + credentials combo (browser anyway rejects, but
      framework should refuse to send the unsafe combo)

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
