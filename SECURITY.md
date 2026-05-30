# Security

lagodev is **secure-by-default**: every guard listed below is either
enabled out of the box or shipped as a one-line middleware so the
application has no excuse to ship without it.

## Reporting a vulnerability

Email the maintainer privately. Do **not** open a public issue with PoC
exploit details. The repository follows responsible-disclosure.

---

## Defenses by layer

### Input

| Concern | Defense | Where |
|---|---|---|
| Oversized JSON body (DoS) | `http.MaxBytesReader` wraps `r.Body` | `web/context.go` `Bind`, default 1 MiB |
| Oversized form / file upload | `BodyLimit(n)` middleware | `web/security.go` |
| Slowloris-style header attack | `Server.ReadHeaderTimeout = 10s` | `web/app.go` |
| Slow body / write attacks | `ReadTimeout` / `WriteTimeout` / `IdleTimeout` | `web/app.go` |
| Header smuggling / abuse | Stdlib `net/http` validates header CRLF | upstream |
| Unbounded path / query | go 1.22 `mux` pattern routing | `web/app.go` |
| Untrusted JSON shape | Struct-tag validator `validate:"…"` | `web/validate.go` |
| Mass assignment | `c.Bind` decodes into a typed struct — extra keys ignored by default | `encoding/json` |

### SQL / data layer

| Concern | Defense | Where |
|---|---|---|
| SQL injection | All builder paths use placeholders (`g.Placeholder(n)`) — column names quoted via grammar | `query/builder.go` |
| Identifier injection in raw expressions | `WhereRaw` requires bound `args ...any` | `query/builder.go` |
| Path traversal | Framework does not serve untrusted file paths; applications must use `filepath.Clean` | n/a |
| Bulk delete without WHERE | `Truncate` is explicit; ORM `Delete` requires the model's PK | `query/builder.go`, `orm/query.go` |

### Authentication / authorization

| Concern | Defense | Where |
|---|---|---|
| Password storage | bcrypt with configurable cost (default `bcrypt.DefaultCost`) | `auth/auth.go` |
| Timing attacks on hash compare | `bcrypt.CompareHashAndPassword` is constant-time | `auth/auth.go` |
| JWT "alg: none" attack | `Parse` rejects non-HMAC signing methods explicitly | `auth/auth.go` `Parse` |
| Bad JWT signature / expiry | Distinct sentinels `ErrInvalidToken`, `ErrExpiredToken` | `auth/auth.go` |
| Brute force / credential stuffing | `RateLimit(...)` / `Throttle(...)` middleware | `web/security.go` |
| Replay across logout | Short `AccessTTL` (default 15 min); applications add a revocation list when needed | `auth/auth.go` |

### Web / cross-origin

| Concern | Defense | Where |
|---|---|---|
| CSRF on cookie-auth endpoints | `CSRF()` middleware — double-submit cookie, constant-time compare | `web/security.go` |
| XSS via reflected JSON | Responses set `Content-Type: application/json; charset=utf-8`; framework never inlines HTML from input | `web/context.go` |
| Clickjacking | `X-Frame-Options: DENY` via `SecurityHeaders()` | `web/security.go` |
| MIME sniffing | `X-Content-Type-Options: nosniff` | `web/security.go` |
| TLS downgrade | `Strict-Transport-Security` (opt-in, requires HTTPS) | `web/security.go` |
| Loose CSP | Default `Content-Security-Policy: default-src 'self'` | `web/security.go` |
| Referrer leak | `Referrer-Policy: strict-origin-when-cross-origin` | `web/security.go` |
| Privileged-API surface | `Permissions-Policy` strips camera / geolocation / mic by default | `web/security.go` |
| CORS misconfig | Strict allow-list, wildcard rejected when credentials are requested | `web/middleware.go` `CORS()` |

### Cookies

| Concern | Defense | Where |
|---|---|---|
| Cookie theft via JS | `HttpOnly` default-on in `c.SetCookie` | `web/cookies.go` |
| Cookie sent over HTTP | `Secure` default-on (configurable for dev) | `web/cookies.go` |
| CSRF via cross-site GET | `SameSite=Lax` default | `web/cookies.go` |

### Secrets & logging

- `.env` is gitignored; secrets are read via `config` package, never
  hardcoded.
- `Logger()` middleware does not log request/response bodies — only
  method, path, status, and duration.
- In production (`APP_ENV=production`) `InternalError` returns a generic
  `{"error":"internal server error"}` payload; the raw error stays in
  `Recovery()`'s log.

### Dependencies

- Run `govulncheck ./...` in CI (`.github/workflows`).
- Pin direct dependencies; review `go.mod` updates in PR.

---

## Recommended middleware stack (copy/paste)

```go
app := web.New(
    web.WithDatabase(conn),
    web.WithAddr(":8080"),
)

app.Use(
    web.RequestID(),
    web.SecurityHeaders(),
    web.BodyLimit(1<<20),          // 1 MiB
    web.RateLimit(60, time.Minute), // 60 req/min/IP
    web.CORS("https://app.example.com"),
)

api := app.Group("/api", func(g *web.Router) {
    g.Use(web.CSRF(), web.AuthJWT(authMgr))
    g.Resource("posts", &PostController{Conn: conn})
})
```
