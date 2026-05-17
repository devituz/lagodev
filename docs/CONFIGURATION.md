# Configuration

lagodev reads two distinct configuration sources:

| Source       | What it controls                                          |
|--------------|-----------------------------------------------------------|
| `.env` files | Runtime database connection, logging, app environment    |
| `lago.json`  | Directory layout the CLI uses for generators             |

## `.env`

Generate a starter file:

```bash
lago env:init       # writes ./.env
```

```bash
# .env
APP_ENV=local
APP_DEBUG=true
APP_NAME=app

# Driver: postgres | mysql | sqlite
DB_CONNECTION=postgres
DB_HOST=127.0.0.1
DB_PORT=5432
DB_USERNAME=postgres
DB_PASSWORD=secret
DB_DATABASE=app
DB_SCHEMA=public
DB_SSL_MODE=disable
DB_TIMEZONE=Asia/Tashkent

# Pool tuning (optional)
DB_MAX_OPEN=25
DB_MAX_IDLE=5
DB_CONN_MAX_LIFETIME=1h
DB_CONN_MAX_IDLE_TIME=10m

# Logging (optional)
DB_LOG_QUERIES=true
DB_SLOW_QUERY=200ms
```

### Load order

The CLI loads `.env` files in this order — later files overlay earlier ones:

1. `.env`               — shared / committed
2. `.env.local`         — developer overrides (gitignored)
3. `.env.$APP_ENV`      — environment-specific (`.env.production`, `.env.testing`, …)

`os.Getenv` always wins over `.env` files (the process environment is the
final source of truth).

### Reading from your own code

```go
import "github.com/devituz/lagodev/config"

func init() { _ = config.LoadEnv() }

cfg := config.FromEnv()                 // builds a database.Config
debug := config.Bool("APP_DEBUG", false)
host  := config.String("REDIS_HOST", "localhost")
ttl   := config.Duration("CACHE_TTL", 5*time.Minute)
```

### Inspecting the active environment

```bash
lago env                       # password/secret/key/token redacted
lago env --show-secrets        # full output
```

### Reference

| Variable                | Type      | Default     | Notes |
|-------------------------|-----------|-------------|-------|
| `APP_ENV`               | string    | `""`        | Selects the third `.env.$APP_ENV` overlay |
| `APP_DEBUG`             | bool      | `false`     | Free for your app to read |
| `APP_NAME`              | string    | `""`        | Free for your app to read |
| `DB_CONNECTION`         | string    | `sqlite`    | `postgres`, `mysql`, `sqlite` |
| `DB_DSN`                | string    | `""`        | Overrides the structured fields below |
| `DB_HOST` / `DB_PORT`   | string/int| (driver default) | |
| `DB_USERNAME` / `DB_PASSWORD` | string | `""` |  |
| `DB_DATABASE` / `DB_SCHEMA`   | string | `""` |  |
| `DB_SSL_MODE`           | string    | `""`        | Postgres: `disable`, `require`, `verify-ca`, … |
| `DB_TIMEZONE`           | string    | `UTC`       | IANA name (`Asia/Tashkent`); also accepts `Local` |
| `DB_MAX_OPEN`           | int       | (driver default) | Pool tuning |
| `DB_MAX_IDLE`           | int       | (driver default) |  |
| `DB_CONN_MAX_LIFETIME`  | duration  | `0`         | `1h`, `30m`, etc. |
| `DB_CONN_MAX_IDLE_TIME` | duration  | `0`         |  |
| `DB_LOG_QUERIES`        | bool      | `false`     | Log every SQL call |
| `DB_SLOW_QUERY`         | duration  | `0`         | Log queries slower than this |
| `LAGO_CONFIG`           | path      | `lago.json` | Override the per-project config path |

## `lago.json`

Each project decides where models, migrations, factories, seeders, and
tests live. Generate the file:

```bash
lago init       # writes ./lago.json with the default layout
```

```json
{
  "paths": {
    "models":     "models",
    "migrations": "migrations",
    "factories":  "factories",
    "seeders":    "seeders",
    "tests":      "tests"
  }
}
```

Want everything under `internal/`?

```json
{
  "paths": {
    "models":     "internal/domain",
    "migrations": "internal/db/migrations",
    "factories":  "internal/testdata/factories",
    "seeders":    "internal/testdata/seeders",
    "tests":      "internal/db/tests"
  }
}
```

The `make:*` commands honor these paths automatically; pass `--dir` to
override on a per-command basis.

### Resolution order

1. The path in `$LAGO_CONFIG` (if set)
2. `lago.json` in the current working directory
3. Built-in defaults (table above)

A malformed `lago.json` falls back to defaults with a warning on stderr.

## Time zones

Set `DB_TIMEZONE` (or `Config.TimeZone`) to an IANA name and lagodev:

- adds `_loc=...` to SQLite DSN so timestamps round-trip in your zone,
- adds `loc=...` to MySQL DSN,
- adds `timezone=...` to the Postgres DSN session,
- uses `time.Now().In(loc)` for `CreatedAt`/`UpdatedAt`/`DeletedAt`.

```go
conn.Location()    // *time.Location
conn.Now()         // time.Now().In(conn.Location())
```

The default is `UTC`. Most production deployments should stay on UTC and
display in the user's zone at the edge — but lagodev gives you the lever
when you need it.
