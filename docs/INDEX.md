# Documentation index

| Topic                              | File                                           | Audience                       |
|------------------------------------|------------------------------------------------|--------------------------------|
| 10-minute introduction             | [Getting started](GETTING_STARTED.md)          | First-time users               |
| Web framework                      | [Web](WEB.md)                                  | Building HTTP APIs             |
| ORM — querying, hooks, casts       | [ORM](ORM.md)                                  | Day-to-day data access         |
| Migrations & schema DSL            | [Migrations](MIGRATIONS.md)                    | Database evolution             |
| Factories & seeders                | [Factories](FACTORIES.md)                      | Test data, dev fixtures        |
| `lago` / `artisan` CLI             | [CLI](CLI.md)                                  | Generators, migrate, db:* |
| Configuration: `.env` & `lago.json`| [Configuration](CONFIGURATION.md)              | Per-environment deployments    |
| Integrating with Gin / Fiber / Echo| [Framework integration](FRAMEWORK_INTEGRATION.md) | Adding lagodev to existing apps |
| Architecture deep-dive             | [Architecture](ARCHITECTURE.md)                | Contributors                   |

## Running examples

```bash
# Full showcase (3 models, FK, seeders, services, controllers)
cd examples/blog && go run .

# Framework comparisons (each has its own go.mod)
cd examples/gin    && go run .
cd examples/fiber  && go run .
cd examples/echo   && go run .
cd examples/chi    && go run .

# Native lagodev/web server
cd examples/basic  && go run .
```

## Quick links

- **Source**: <https://github.com/devituz/lagodev>
- **API reference**: <https://pkg.go.dev/github.com/devituz/lagodev>
- **Latest release**: see [GitHub Releases](https://github.com/devituz/lagodev/releases)
- **CHANGELOG**: [CHANGELOG.md](../CHANGELOG.md)
