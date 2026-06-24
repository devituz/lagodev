# lagodev Roadmap — toward (and beyond) Laravel / Django / NestJS

> Goal: make lagodev a batteries-included Go framework that reaches **feature
> parity** with Laravel, Django, and NestJS, then **pulls ahead** by leaning on
> what Go does better — compile-time safety, real concurrency, single-binary
> deploys, and native observability.

This document is the source of truth for what to build next. Each feature has a
**priority** (P0 = parity-critical, P1 = expected, P2 = differentiator/nice),
a **scope** (packages), and a **target version**. We work top-down.

---

## 1. Where lagodev already stands (strong foundation)

Already shipped and hardened (v0.20.x–v0.21.0):

| Capability | Package | Parity note |
|---|---|---|
| ActiveRecord ORM + generics | `orm`, `query`, `relations` | ~Eloquent / Django ORM |
| Soft deletes, pagination, scopes, upsert | `orm` | ✅ v0.21.0 |
| Schema builder + migrations (PG/MySQL/SQLite) | `schema`, `migrations`, `drivers/*` | ~Laravel migrations |
| Request validation (tags + map + 422 shape) | `validation` | ~FormRequest / DRF serializers (partial) |
| JWT auth + bcrypt | `auth` | ~Sanctum (token only) |
| RBAC gates + policies | `authz` | ~Laravel Gate/Policy |
| Cache (atomic + batch) | `cache` | ~Laravel Cache |
| Sessions | `session` | ~Laravel session |
| Queue + worker + DLQ + SQL driver | `queue`, `queue/sqlqueue` | ~Laravel Queue / Bull |
| Events / listeners | `events` | ~Laravel events / Nest CQRS-lite |
| Task scheduling | `scheduling` | ~Laravel scheduler |
| Broadcasting / pub-sub | `broadcasting` | ~Laravel Echo (server) |
| Mail (SMTP + Mailgun + SendGrid) | `mail` | ~Laravel Mail |
| Notifications (multi-channel) | `notifications` | ~Laravel Notifications |
| Filesystem (local + S3) | `filesystem`, `filesystem/s3` | ~Flysystem |
| HTTP client | `httpclient` | ~Guzzle / Http facade |
| Router + middleware (CORS, CSRF, RateLimit, RequestID, Recovery, SecurityHeaders, graceful shutdown, SSE/streaming) | `web`, `router` | ~Laravel/Nest middleware |
| Framework adapters (gin, grpc, websocket) | `adapters/*` | Nest transport-ish |
| i18n / localization | `i18n` | ~Laravel localization / Django i18n |
| Dates (Carbon-like) | `carbon` | ~Carbon |
| Attribute casts | `casts` | ~Eloquent casts |
| Model factories + seeders | `factory`, `seeder` | ~Laravel factories |
| Config (.env) | `config` | ~config |
| Crypto helpers | `crypt` | ~encryption |
| Logger, process, hooks, testing, mock | `logger`, … | tooling |
| Interactive scaffolder (`lago new`), Docker/k8s | `cli`, `cmd` | ~Artisan + Sail |

**Verdict:** the data/HTTP/async/infra layers are already competitive. The gaps
are in **application architecture** (DI, modules), **API presentation**
(serializers, OpenAPI, GraphQL), **auth breadth** (sessions, OAuth, account
flows), **developer surface** (admin, codegen, dashboards, templating), and
**observability**.

---

## 2. Competitive gap matrix

`✅ have` · `🟡 partial` · `❌ missing`

| Feature | Laravel | Django | NestJS | lagodev | Action |
|---|:--:|:--:|:--:|:--:|---|
| Service Container / DI | ✅ | 🟡 | ✅ (core) | ❌ | **P0** build `container` |
| Modules / providers / bootstrap | ✅ | ✅ apps | ✅ | 🟡 | **P0** `app`/`module` layer |
| API Resources / Serializers | ✅ | ✅ DRF | ✅ | ❌ | **P0** `resource` package |
| Framework-wide OpenAPI/Swagger | 🟡 | 🟡 | ✅ | 🟡 gin-only | **P0** `openapi` package |
| Eager loading `With("a.b")` | ✅ | ✅ | ✅ | 🟡 manual | **P0** ORM `With` |
| Auth: sessions/guards | ✅ | ✅ | ✅ | ❌ | **P1** session guard |
| Auth: password reset / email verify | ✅ | ✅ | 🟡 | ❌ | **P1** account flows |
| Auth: OAuth2 / social login | ✅ Socialite | 🟡 | 🟡 | ❌ | **P1** `oauth` |
| Auth: API token management | ✅ Sanctum | ✅ DRF | 🟡 | 🟡 JWT only | **P1** token store |
| Template / view engine | ✅ Blade | ✅ DTL | 🟡 | ❌ | **P1** `view` (html/template) |
| Admin / auto-CRUD panel | 🟡 Nova($) | ✅ killer | 🟡 | ❌ | **P1** `admin` (differentiator) |
| GraphQL | 🟡 | 🟡 | ✅ | ❌ | **P2** `graphql` |
| WebSocket high-level gateway | ✅ | 🟡 | ✅ | 🟡 adapter | **P2** `realtime` |
| Observability dashboard | ✅ Telescope/Horizon | 🟡 | 🟡 | ❌ | **P2** `telescope` |
| Native tracing/metrics (OTel) | 🟡 | 🟡 | 🟡 | ❌ | **P1** `observability` (differentiator) |
| Collections / data pipeline | ✅ | 🟡 | 🟡 | ❌ | **P2** `collection` (generics) |
| Rate limit / resilience | ✅ | 🟡 | ✅ | 🟡 | **P2** circuit breaker |
| Codegen CLI (model/ctrl/client) | ✅ Artisan | ✅ mgmt cmds | ✅ nest g | 🟡 | **P1** extend `lago gen` |
| Full-text / search abstraction | ✅ Scout | 🟡 | 🟡 | ❌ | **P2** `search` |
| Background job dashboard | ✅ Horizon | 🟡 | 🟡 Bull Board | ❌ | **P2** queue UI |

---

## 3. Prioritized feature plan

### P0 — Parity-critical (build first)

#### 3.1 Service Container / Dependency Injection — `container/`
Both Laravel and NestJS are built on a container. Go can do this type-safely.
- Generic-based registry: `container.Bind[T](c, factory)`, `container.Singleton[T]`,
  `container.Make[T](c)`. Constructor injection by resolving dependencies.
- Scoped containers (per-request) wired into `web` context.
- Auto-wiring via reflection for struct dependencies (opt-in).
- **Why:** unlocks modules, providers, testability, and decouples wiring.
- **Scope:** new `container/`; light hooks in `web`. **Target:** v0.22.0.

#### 3.2 Application / Module layer — `app/`
A bootstrap + module system (Nest modules / Django apps / Laravel providers).
- `app.New()` boots config → container → providers → router.
- `Provider` interface: `Register(c)` + `Boot(c)`. Ordered, with deps.
- Modules group providers, routes, migrations, and commands.
- **Why:** turns a pile of packages into a cohesive application skeleton.
- **Scope:** new `app/`, integrates `container`, `config`, `web`, `migrations`.
  **Target:** v0.22.0.

#### 3.3 API Resources / Serializers — `resource/`
DRF serializers / Laravel API Resources: transform models → JSON cleanly.
- `resource.Resource[Model, DTO]` with field selection, renaming, computed
  fields, relation embedding, conditional fields, and collection wrapping
  (`data`, `meta`, pagination links from `orm.Paginator`).
- Input deserialization paired with `validation`.
- **Why:** every JSON API needs a presentation layer; hand-mapping is the #1
  boilerplate pain.
- **Scope:** new `resource/`; integrates `orm`, `validation`, `web`.
  **Target:** v0.22.0.

#### 3.4 Framework-wide OpenAPI 3.1 — `openapi/`
Promote the gin-only OpenAPI into a first-class, adapter-agnostic generator.
- Derive specs from typed routes + `validation` rules + `resource` DTOs.
- Serve Swagger UI / Redoc; export `openapi.json`.
- Optional typed client generation (`lago gen client`).
- **Why:** Nest/DRF set the bar; API-first teams expect it.
- **Scope:** new `openapi/`; consumes `web`, `validation`, `resource`.
  **Target:** v0.23.0.

#### 3.5 ORM eager loading — `orm.With("posts.comments")`
- Nested `With` on `Builder[T]`, batch-loads relations (no N+1), reusing the
  fixed `belongsTo`/`hasMany` loaders. Constrained eager loads
  (`With("posts", func(q){...})`).
- **Why:** the last big Eloquent/ORM gap; relations exist but loading is manual.
- **Scope:** `orm`, `relations`. **Target:** v0.22.0.

### P1 — Expected (next)

#### 3.6 Auth breadth — `auth`, new `auth/oauth`, `auth/account`
- **Session guard** (cookie auth) alongside JWT; pluggable user provider.
- **Account flows:** password reset tokens, email verification, signed URLs,
  remember-me, throttled login.
- **API token store** (Sanctum-style): hashed tokens, abilities, revocation,
  last-used — pairs with the existing `jti`.
- **OAuth2 / social login** (`auth/oauth`): Google/GitHub/etc. + generic OIDC.
- **Why:** "auth" today is JWT primitives; real apps need the full lifecycle.
- **Scope:** `auth` + submodules. **Target:** v0.23.0–v0.24.0.

#### 3.7 View / template engine — `view/`
- Thin layer over `html/template`: layouts, partials, components, named views,
  CSRF/asset/route helpers, auto-escaping. Hot-reload in dev.
- **Why:** server-rendered apps (Blade/DTL) and email templating.
- **Scope:** new `view/`; integrates `web`, `mail`. **Target:** v0.24.0.

#### 3.8 Admin / auto-CRUD panel — `admin/`
Django's killer feature. Register a model → get a CRUD UI.
- Reflect schema → list/detail/forms; filters, search, pagination, soft-delete
  awareness, RBAC-gated actions; pluggable field widgets.
- **Why:** massive productivity win and a strong differentiator vs Laravel/Nest.
- **Scope:** new `admin/`; uses `orm`, `schema`, `authz`, `view`.
  **Target:** v0.25.0.

#### 3.9 Native observability — `observability/`
- First-class OpenTelemetry: traces/metrics/logs across HTTP, ORM (query
  spans), queue, cache, http client; exemplars; Prometheus endpoint.
- **Why:** Go's strength; ship what Laravel/Django bolt on. Differentiator.
- **Scope:** new `observability/`; thin hooks everywhere. **Target:** v0.24.0.

#### 3.10 Codegen CLI — extend `lago gen`
- `lago gen model|migration|resource|controller|policy|test|client`.
- Scaffolds wired to the new `app`/`module`/`resource` conventions.
- **Why:** Artisan/`nest g` velocity.
- **Scope:** `cli`, `cmd/lago`. **Target:** rolling, v0.22.0+.

### P2 — Differentiators / nice-to-have

- **GraphQL** (`graphql/`): schema-first or struct-first resolvers over the ORM. v0.26+.
- **Real-time gateway** (`realtime/`): high-level rooms/presence/auth channels on
  the websocket adapter + `broadcasting`. v0.26+.
- **Telescope-style dashboard** (`telescope/`): requests, queries (N+1 flagging),
  jobs, cache, mails, exceptions — fed by `observability`. v0.26+.
- **Queue dashboard** (Horizon/Bull-Board-style): throughput, failed-job retry UI. v0.26+.
- **Collections** (`collection/`): generic, lazy data-pipeline helpers
  (`Map/Filter/Reduce/GroupBy/Chunk`). v0.23+.
- **Search abstraction** (`search/`): driver interface + Meilisearch/Typesense/PG
  FTS. v0.27+.
- **Resilience** (`resilience/`): circuit breaker, retry/backoff, bulkhead,
  timeouts — reuse in `httpclient`/`queue`. v0.25+.

---

## 4. How lagodev pulls *ahead* (Go-native edge)

Parity is the floor. These make lagodev objectively better for its niche:

1. **Compile-time safety everywhere.** Typed query builder, typed routes, typed
   events, typed resources via generics — catch at build time what PHP/Python
   catch at runtime (or never). Push further: a `lago gen` that emits typed
   accessors so `user.Posts()` is checked by the compiler.
2. **Single static binary.** Embed migrations, templates, admin assets, and
   OpenAPI UI via `embed.FS`. One artifact, no runtime, trivial containers.
3. **Real concurrency, not workarounds.** Goroutine-native queues, schedulers,
   broadcasting, and parallel eager-loading — no separate worker runtime, no
   PHP-FPM/Gunicorn process model.
4. **Observability as a first-class citizen**, not a paid add-on (Telescope is
   PHP-only; Horizon needs Redis). OTel-native traces/metrics across every layer.
5. **Cloud-native by default.** Health/readiness, graceful shutdown, 12-factor
   config, k8s scaffolding already ship; add leader election, distributed
   scheduling locks, and zero-downtime migration gates.
6. **Security baked in.** govulncheck-clean releases, pinned patched toolchain,
   default-deny CSRF/headers, path-traversal/header-injection guards already
   landed — keep "secure by default" as a release gate.
7. **Performance budget per release.** `benchmarks/` becomes CI-gated: router,
   ORM hydration, JSON, and validation throughput tracked so we stay
   order-of-magnitude faster than Laravel/Django.

---

## 5. Milestones

| Version | Theme | Headline features |
|---|---|---|
| **v0.22.0** | Application core | `container` (DI), `app`/modules, `resource`, ORM `With`, `lago gen` start |
| **v0.23.0** | API-first | `openapi` (framework-wide), `collection`, typed client gen |
| **v0.24.0** | Auth + UX | auth breadth (sessions/account/OAuth), `view`, `observability` (OTel) |
| **v0.25.0** | Productivity | `admin` panel, `resilience` |
| **v0.26.0** | Real-time + insight | `graphql`, `realtime`, `telescope`, queue dashboard |
| **v0.27.0** | Scale | `search`, distributed scheduling/leader election |
| **v1.0.0** | Stability | API freeze, docs site, security audit, perf gates |

---

## 6. Cross-cutting release rules (non-negotiable)

- **Backward compatible & `go get`-able.** No `replace` directives in published
  modules; additive changes; breaking changes only on a major bump, loudly
  documented. Each submodule gets its own path-prefixed tag.
- **Tested.** Every feature ships with regression tests; `go test -race ./...`
  green; `go vet` + `gofmt` clean.
- **Secure.** `govulncheck ./...` clean; patched toolchain pinned.
- **Documented.** Godoc with runnable examples + a CHANGELOG entry per release.

> Next up: start at **v0.22.0 — Application core** (`container` → `app`/modules
> → `resource` → ORM `With`). Pick one and we build it.
