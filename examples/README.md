# lagodev examples

Pick the one that matches your stack — each runs standalone with
`go mod tidy && go run .`.

| Folder              | What it shows                                                  |
|---------------------|----------------------------------------------------------------|
| [`basic/`](basic)   | The 30-line tour: connection → migrations → ORM → query        |
| [`blog/`](blog)     | **Full showcase** — 3 models, FK, factories, seeders, service  |
| [`gin/`](gin)       | Gin v1 server wrapping a framework-agnostic service            |
| [`fiber/`](fiber)   | Fiber v2 — identical service, swapped HTTP layer               |
| [`echo/`](echo)     | Echo v4 — same                                                 |
| [`chi/`](chi)       | Chi v5 — Go-native routing on top of `net/http`                |
| [`microservice/`](microservice) | A queue-style worker with `LockForUpdate` + tx     |
| [`grpc/`](grpc)     | A bare-bones gRPC service that shares the ORM with HTTP        |

## The pattern

**Every** example follows the same shape:

```
1. Define models (embed orm.Model)
2. Register migrations in init()
3. Apply migrations at startup
4. Build a *Service struct that takes a *database.Connection
5. Wrap the service in framework-specific handlers
```

The `*Service` layer is the integration point — it returns plain Go
values and errors. That's why swapping Gin → Fiber → Echo means changing
only the HTTP wiring, never the business logic.

## See also

- [`../docs/GETTING_STARTED.md`](../docs/GETTING_STARTED.md) — first 10 minutes
- [`../docs/CLI.md`](../docs/CLI.md) — full `lago` / `artisan` reference
- [`../docs/ORM.md`](../docs/ORM.md) — ORM cookbook
- [`../docs/MIGRATIONS.md`](../docs/MIGRATIONS.md) — migration patterns
- [`../docs/FACTORIES.md`](../docs/FACTORIES.md) — factories + seeders
- [`../docs/CONFIGURATION.md`](../docs/CONFIGURATION.md) — `.env` and `lago.json`
- [`../docs/FRAMEWORK_INTEGRATION.md`](../docs/FRAMEWORK_INTEGRATION.md) — Gin/Fiber/Echo/Chi/gRPC patterns
- [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) — how it all fits together
