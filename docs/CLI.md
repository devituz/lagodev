# CLI reference

lagodev ships two interchangeable binaries — `lago` and `artisan` — built
from `cmd/lago` and `cmd/artisan` respectively. They share the entire
command tree.

```bash
go install github.com/devituz/lagodev/cmd/lago@latest      # → lago
# or
go install github.com/devituz/lagodev/cmd/artisan@latest   # → artisan
```

This guide uses `lago` throughout; substitute `artisan` if you prefer.

---

## Project setup

| Command                | Description                                        |
|------------------------|----------------------------------------------------|
| `lago init`            | Write `lago.json` with the default path layout     |
| `lago env:init`        | Write a documented starter `.env` file             |
| `lago env`             | Print the active env (sensitive values redacted)   |
| `lago env --show-secrets` | Show passwords/keys in clear text               |

## Generators

All `make:*` commands accept `--fields=name:type[:modifier]` (comma list),
`--force` (overwrite), and `--dir` (output directory override).

| Command                | Description                                                  |
|------------------------|--------------------------------------------------------------|
| `lago make:model`      | Generate a model struct (embed `orm.Model`)                  |
| `lago make:migration`  | Generate a timestamped migration with up/down                |
| `lago make:factory`    | Generate a faker-powered factory                             |
| `lago make:seeder`     | Generate a seeder registered in `init()`                     |
| `lago make:test`       | Generate a test scaffold using `lagotest.SQLite`             |
| `lago make:service`    | Generate a framework-agnostic CRUD service                   |
| `lago make:controller` | Generate a net/http REST controller wrapping a service       |
| `lago make:crud`       | One-shot: model + migration + factory + seeder + test        |

`make:model` flags compose:

```bash
lago make:model Post -m -f -s -t -c \
    --fields="title:string,body:text,published:bool:default(false)"
```

| Flag           | Generates                  |
|----------------|----------------------------|
| `-m, --migration`  | Migration                  |
| `-f, --factory`    | Factory                    |
| `-s, --seeder`     | Seeder                     |
| `-t, --test`       | Test                       |
| `-c, --controller` | Service + controller       |
| `-a, --all`        | All of the above           |

## Field spec syntax

```
name:type[:modifier1][:modifier2]...
```

**Types:** `string`, `text`, `longtext`, `char`, `int`, `bigint`, `smallint`,
`tinyint`, `uint`, `bool`, `float`, `double`, `decimal`, `json`, `jsonb`,
`uuid`, `date`, `datetime`, `timestamp`, `time`, `binary`.

**Modifiers:** `nullable`, `unique`, `index`, `primary`, `default(VALUE)`.

Examples:

```bash
--fields="email:string:unique"
--fields="age:int:default(18)"
--fields="bio:text:nullable"
--fields="published_at:datetime:nullable"
--fields="role:string:default('user'):index"
```

The same spec feeds three artifacts simultaneously:

| Artifact   | Result                                                          |
|------------|-----------------------------------------------------------------|
| Model      | `Email string \`column:"email"\``                                |
| Migration  | `t.String("email").Unique()`                                    |
| Factory    | `Email: f.Email()` (auto-picks an apt faker call by name+type)  |

## Migrations

```bash
lago migrate                                # apply all pending
lago migrate --pretend                      # print SQL, don't execute
lago migrate --path 2026_01_01_000001_create_users_table   # only that one
lago migrate:status                         # tabular: applied / pending
lago migrate:rollback                       # last batch
lago migrate:rollback --step=3              # roll back 3 most recent
lago migrate:reset                          # roll back EVERYTHING
lago migrate:refresh                        # reset + up
lago migrate:fresh                          # DROP all tables, then up
lago migrate:fresh --seed                   # fresh + run seeders
lago migrate:step --n=2                     # apply at most 2 pending
```

## Database

```bash
lago db:seed                                # all seeders, dependency-ordered
lago db:seed UserSeeder PostSeeder          # only these (+ their deps)
lago db:seed --class UserSeeder             # Laravel-compatible alias
lago db:wipe --force                        # DROP every user table
lago db:show                                # driver, host, tz, tables + row counts
lago db:table users                         # columns, types, nullability
lago db                                     # interactive SQL prompt (\q to exit)
```

## Custom binary

You can build your own CLI with the same surface plus your commands:

```go
package main

import (
    _ "github.com/devituz/lagodev/drivers/postgres"

    "github.com/devituz/lagodev/cli"
    _ "myapp/db/migrations"
    _ "myapp/db/seeders"
)

func main() {
    app := cli.New(cli.Options{ProjectName: "myapp"})
    app.AddCommand(myCustomCommand())
    app.Execute()
}
```

`cli.Options` lets you inject a custom `*database.Manager`, custom
migration/seeder registries, and a custom connector if you don't want the
default `.env`-driven one.
