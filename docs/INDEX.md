# Documentation index

A full-stack Go web framework — batteries included. Start with **Getting started**,
then dive into any subsystem below.

## Getting started

| Topic                              | File                                           | Audience                       |
|------------------------------------|------------------------------------------------|--------------------------------|
| 10-minute introduction             | [Getting started](GETTING_STARTED.md)          | First-time users               |
| Architecture deep-dive             | [Architecture](ARCHITECTURE.md)                | Contributors                   |
| Configuration: `.env` & `lago.json`| [Configuration](CONFIGURATION.md)              | Per-environment deployments    |
| `lago` / `artisan` CLI             | [CLI](CLI.md)                                  | Generators, migrate, db:*      |
| vs Laravel / Django / NestJS / Express | [Comparison](COMPARISON.md)               | Evaluating the framework       |
| Benchmarks & how to run them       | [Benchmarks](BENCHMARKS.md)                    | Performance-minded             |

## HTTP & presentation

| Topic                              | File                                           |
|------------------------------------|------------------------------------------------|
| Web framework (routing, middleware, requests) | [Web](WEB.md)                       |
| Templating & views                 | [Views](VIEWS.md)                              |
| API resources & serializers, collections | [API resources](API_RESOURCES.md)       |
| OpenAPI 3.1 spec + typed client codegen | [OpenAPI](OPENAPI.md)                     |
| GraphQL schema & execution         | [GraphQL](GRAPHQL.md)                          |
| HTTP client                        | [HTTP client](HTTP_CLIENT.md)                  |
| Real-time WebSocket gateway & broadcasting | [Realtime](REALTIME.md)                |

## Data

| Topic                              | File                                           |
|------------------------------------|------------------------------------------------|
| ORM — querying, hooks, casts       | [ORM](ORM.md)                                  |
| Connections, query builder, relations, seeders | [Database](DATABASE.md)            |
| Migrations & schema DSL            | [Migrations](MIGRATIONS.md)                    |
| Factories & seeders                | [Factories](FACTORIES.md)                      |
| Full-text search                   | [Search](SEARCH.md)                            |
| Caching                            | [Cache](CACHE.md)                              |

## Security & identity

| Topic                              | File                                           |
|------------------------------------|------------------------------------------------|
| Authentication (guard/session/JWT/OAuth/account) | [Authentication](AUTHENTICATION.md) |
| Authorization (gates & policies)   | [Authorization](AUTHORIZATION.md)              |
| Sessions                           | [Session](SESSION.md)                          |
| Encryption, hashing, signing       | [Encryption](ENCRYPTION.md)                    |

## Background work & messaging

| Topic                              | File                                           |
|------------------------------------|------------------------------------------------|
| Queues, jobs & dashboard           | [Queue](QUEUE.md)                              |
| Task scheduling & processes        | [Scheduling](SCHEDULING.md)                    |
| Events & notifications             | [Events](EVENTS.md)                            |
| Mail (SMTP, SendGrid, Mailgun)     | [Mail](MAIL.md)                                |

## Architecture, ops & cross-cutting

| Topic                              | File                                           |
|------------------------------------|------------------------------------------------|
| Dependency injection & app lifecycle | [Container](CONTAINER.md)                    |
| Validation                         | [Validation](VALIDATION.md)                    |
| Resilience (breaker, retry, timeout, bulkhead, rate limit) | [Resilience](RESILIENCE.md) |
| Observability (OpenTelemetry tracing/metrics) | [Observability](OBSERVABILITY.md)   |
| Telescope debug dashboard          | [Telescope](TELESCOPE.md)                      |
| Admin panel                        | [Admin](ADMIN.md)                              |
| Localization (i18n) & dates (carbon) | [Localization](LOCALIZATION.md)              |
| Integrating with Gin / Fiber / Echo | [Framework integration](FRAMEWORK_INTEGRATION.md) |

## Running examples

```bash
# Full showcase (3 models, FK, seeders, services, controllers)
cd examples/blog && go run .

# Framework comparisons (each has its own go.mod)
cd examples/gin    && go run .
cd examples/fiber  && go run .
cd examples/echo   && go run .
cd examples/chi    && go run .
```

## Quick links

- **Source**: <https://github.com/devituz/lagodev>
- **API reference**: <https://pkg.go.dev/github.com/devituz/lagodev>
- **Latest release**: see [GitHub Releases](https://github.com/devituz/lagodev/releases)
- **CHANGELOG**: [CHANGELOG.md](../CHANGELOG.md)
