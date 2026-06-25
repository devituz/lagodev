# Full-text search

The `search` package is a Scout-style full-text abstraction. Higher layers
program against one small `Engine` interface; you pick the backend at wiring
time and the rest of your code stays the same.

Two engines ship in the box:

- **Memory** — a dependency-free, in-memory inverted index. It tokenizes
  documents, folds case and diacritics, drops stop-words, supports term and
  prefix matching, TF-style ranking, equality filters, and pagination. This is
  the default; great for tests, small datasets, and getting started.
- **Postgres** — compiles `tsvector`/`tsquery` SQL through `database/sql`. The
  index lives in a real table; the engine builds parameterized `SELECT`s that
  rank rows with `ts_rank` and paginate with `LIMIT/OFFSET`.

Both engines tokenize queries through the same exported `Tokenize` primitive, so
the Memory and Postgres backends share a vocabulary and behave consistently.

## The `Engine` contract

```go
type Engine interface {
    Index(ctx context.Context, index string, docs ...Document) error
    Delete(ctx context.Context, index string, ids ...string) error
    Search(ctx context.Context, index, query string, opts Options) (Results, error)
}
```

| Type        | Shape                                                  |
|-------------|--------------------------------------------------------|
| `Document`  | `{ ID string; Fields map[string]any }`                 |
| `Hit`       | `{ ID string; Score float64; Fields map[string]any }`  |
| `Results`   | `{ Hits []Hit; Total int }` — `Total` is pre-pagination |
| `Options`   | `{ Page, PerPage int; Filters map[string]any; Prefix bool }` |

`Doc(id, fields)` is the convenience constructor for a `Document`. Field values
may be strings, numbers, or bools; non-string values are stringified for the
full-text index and kept verbatim for equality filters.

## Quick start (in-memory)

```go
import (
    "context"
    "fmt"

    "github.com/devituz/lagodev/search"
)

func main() {
    ctx := context.Background()
    eng := search.NewMemory()

    // Make some records searchable.
    _ = eng.Index(ctx, "posts",
        search.Doc("1", map[string]any{"title": "Hello World", "body": "first post"}),
        search.Doc("2", map[string]any{"title": "Goodbye World", "body": "last post"}),
        search.Doc("3", map[string]any{"title": "World World World", "body": "all about the world"}),
    )

    // Query it.
    res, _ := eng.Search(ctx, "posts", "world", search.Options{})

    fmt.Println("total:", res.Total) // total: 3
    for _, hit := range res.Hits {
        fmt.Printf("%s score=%.0f\n", hit.ID, hit.Score)
    }
    // 3 score=4
    // 1 score=1
    // 2 score=1
}
```

Ranking is term-frequency overlap: each query token contributes how many times
it appears across a document's fields. Ties break on document ID, so ordering is
deterministic. A blank query (no tokens after tokenizing) yields no hits.

## Indexing

`Index(ctx, index, docs...)` inserts or replaces documents in a named index.
Re-indexing a document with an existing ID **replaces it wholesale** — the
previous tokens and fields are discarded.

```go
// Add or update.
_ = eng.Index(ctx, "posts",
    search.Doc("42", map[string]any{
        "title":  "Distributed systems",
        "author": "ada",
        "tags":   "go databases",
    }),
)

// Remove. Unknown IDs (and unknown indexes) are silently ignored.
_ = eng.Delete(ctx, "posts", "42", "99")
```

The index name is just a string namespace. With the Memory engine it keys an
internal map; with the Postgres engine it is the physical table name.

### Tokenizing

`Tokenize` is the shared primitive both engines (and your own code) can use:

```go
search.Tokenize("Café del Mar, 2026!")  // → ["cafe", "del", "mar", "2026"]
search.IsStopWord("the")                // → true
```

Tokenizing folds case, strips common Latin diacritics (so `Café` and `cafe`
collide), splits on any non-letter/non-digit rune, and drops a small built-in
English stop-word set. The stop list is intentionally compact — if you need a
different vocabulary, build your own engine on top of `Tokenize`.

## Drivers

### Memory engine

```go
eng := search.NewMemory()
```

Safe for concurrent use, no external dependencies. Holds every indexed document
in memory, so it is bounded by RAM and not durable across restarts — re-index on
boot, or use it only for tests and ephemeral data. `Index`/`Delete` are
authoritative here: the engine owns the documents.

### Postgres engine (tsvector / tsquery)

The Postgres engine assumes the documents already live in a table you own. The
expected layout: a text `id` column, a `tsvector` column (generated or
trigger-maintained), and optionally a JSON/JSONB column scanned back into
`Hit.Fields`.

```sql
CREATE TABLE posts (
    id      text PRIMARY KEY,
    title   text NOT NULL,
    body    text NOT NULL,
    fields  jsonb,
    -- generated tsvector kept in lockstep with the row
    search  tsvector GENERATED ALWAYS AS (
        to_tsvector('english', coalesce(title,'') || ' ' || coalesce(body,''))
    ) STORED
);

CREATE INDEX posts_search_idx ON posts USING GIN (search);
```

Wire the engine over a standard `*sql.DB`:

```go
import (
    "database/sql"

    "github.com/devituz/lagodev/search"
)

func newSearch(db *sql.DB) search.Engine {
    return search.NewPostgres(search.WrapDB(db), search.PgConfig{
        IDColumn:     "id",       // default "id"
        VectorColumn: "search",   // default "search"
        FieldsColumn: "fields",   // default "" → Hit.Fields stays nil
        Config:       "english",  // default "english"
    })
}
```

`PgConfig` drives the generated SQL (its zero value is valid and matches the
table above):

| Field          | Default     | Purpose                                                            |
|----------------|-------------|--------------------------------------------------------------------|
| `IDColumn`     | `"id"`      | Primary key scanned into `Hit.ID`                                  |
| `VectorColumn` | `"search"`  | `tsvector` matched against the `tsquery` and fed to `ts_rank`      |
| `FieldsColumn` | `""`        | When set, scanned (JSON/JSONB) into `Hit.Fields`                  |
| `Config`       | `"english"` | Text-search config (`english`, `simple`, …)                      |
| `WebSearch`    | `false`     | Use `websearch_to_tsquery` instead of `plainto_tsquery`           |

`Search` compiles a parameterized statement of the form:

```sql
SELECT "id", ts_rank("search", plainto_tsquery('english', $1)) AS rank,
       "fields", count(*) OVER () AS total
FROM "posts"
WHERE "search" @@ plainto_tsquery('english', $1)
ORDER BY rank DESC, "id" ASC
LIMIT $2 OFFSET $3
```

The query text and every filter value are **bound parameters** — user input is
never interpolated into SQL. Only the configured table/column identifiers and
the trusted `Config` name reach the SQL text, and identifiers are double-quoted.
A `count(*) OVER ()` window function returns the pre-pagination `Total` in the
same round trip. A blank query short-circuits to an empty `Results` with no
round trip.

**`Index` is a no-op on the Postgres engine.** Documents are populated by your
application's normal writes; the `tsvector` column is expected to be generated
or trigger-maintained (as above). `Delete` does issue a real
`DELETE ... WHERE id IN (...)`. This lets a Postgres-backed app share the same
search-agnostic code as a Memory-backed one.

## Ranking and filters

`Options` controls every `Search` call; the zero value is valid (first page,
default size, term matching):

```go
res, _ := eng.Search(ctx, "posts", "distributed go", search.Options{
    Page:    2,
    PerPage: 20,
    Filters: map[string]any{"author": "ada"},
    Prefix:  true,
})
```

- **Pagination** — `Page` is 1-indexed (`< 1` → 1); `PerPage` falls back to
  `DefaultPerPage` (15) when unset. `Results.Total` is the match count across
  all pages, so you can compute page counts as `ceil(Total / PerPage)`.
- **Filters** — equality predicates, AND-ed together. On Memory they compare by
  stringified value (an `int` filter matches a numeric field regardless of Go
  type); a missing field never matches. On Postgres each becomes a bound
  `"col" = $n` predicate, ordered deterministically by key.
- **Prefix** — turns `"hel"` into a match for `"hello"`. On Memory it scans
  document tokens for the prefix; on Postgres it switches to `to_tsquery` and
  appends `:*` to each lexeme. Note: `Prefix` overrides `WebSearch`, since
  websearch syntax does not compose with the `:*` prefix operator.

`Hit.Score` is the relevance (higher is better): term-frequency on Memory,
`ts_rank` on Postgres. Hits arrive sorted by score descending.

## Keeping the index in sync with model changes

The Memory engine owns its documents, so you must mirror ORM writes into it.
The package gives you two ORM-agnostic pieces for this — `Searchable` and
`Indexer` — so models describe *what* to index and an adapter handles *how*,
without the search package ever importing `orm`.

### The `Searchable` interface

A model opts into indexing by describing its Document projection:

```go
type Searchable interface {
    SearchIndex() string        // index name, e.g. "posts"
    SearchDocument() Document    // stable ID + searchable fields
}
```

Implement it on your model. The `SearchDocument().ID` must be stable across
saves (the primary key is the natural choice), since it is what `Index` replaces
and `Delete` evicts.

```go
type Post struct {
    orm.Model
    Title string
    Body  string
}

func (p Post) SearchIndex() string { return "posts" }

func (p Post) SearchDocument() search.Document {
    return search.Doc(
        strconv.FormatUint(p.ID, 10),
        map[string]any{"title": p.Title, "body": p.Body},
    )
}
```

### The `Indexer` adapter

`Indexer` wraps an `Engine` and speaks in terms of `Searchable` models, so call
sites never reconstruct index names or Documents by hand:

```go
func NewIndexer(eng search.Engine) *search.Indexer

func (ix *Indexer) Index(ctx context.Context, m Searchable) error
func (ix *Indexer) Delete(ctx context.Context, index, id string) error
func (ix *Indexer) DeleteModel(ctx context.Context, m Searchable) error
func (ix *Indexer) Backfill(ctx context.Context, items ...Searchable) error
func (ix *Indexer) Engine() search.Engine
```

`Indexer` holds no state beyond its `Engine` and is safe for concurrent use
whenever the `Engine` is (both bundled engines are).

### Wiring into ORM hooks

The search package stays decoupled from `orm`: the hook methods live in your
application and delegate to the `Indexer`. `AfterSave` (re)indexes, `AfterDelete`
evicts (see [ORM.md](ORM.md#hooks) for the hook contract):

```go
// indexer is your app-wide *search.Indexer, built once at boot:
//   indexer := search.NewIndexer(searchEngine)
var indexer *search.Indexer

func (p *Post) AfterSave(ctx *orm.HookContext) error {
    return indexer.Index(ctx.Ctx, p)
}

func (p *Post) AfterDelete(ctx *orm.HookContext) error {
    return indexer.DeleteModel(ctx.Ctx, p)
}
```

Because `Index` replaces a document by ID, the same hook covers both inserts and
updates. With the **Postgres** engine these hooks are largely unnecessary: the
generated `tsvector` column tracks the row automatically on every write, and you
only need the `AfterDelete` hook if you delete the underlying row out-of-band
(`orm.Delete` already removes it).

### Backfilling an existing table (Memory)

Rebuild the in-memory index from the database on boot. `Backfill` groups
documents by index and issues one bulk `Index` call per index:

```go
func warmIndex(ctx context.Context, conn *database.Connection, ix *search.Indexer) error {
    var posts []Post
    if err := orm.Query[Post](conn).Get(ctx, &posts); err != nil {
        return fmt.Errorf("warm index: %w", err)
    }
    items := make([]search.Searchable, len(posts))
    for i := range posts {
        items[i] = posts[i]
    }
    return ix.Backfill(ctx, items...)
}
```

## Production notes

- **Pick the engine by scale.** Memory is perfect for tests and modest datasets
  but is RAM-bound and non-durable — re-index on boot. For anything that must
  survive restarts or grow past memory, use the Postgres engine.
- **Index the vector column.** Add a `GIN` index on the `tsvector` column or
  every `@@` match is a sequential scan. This is the single biggest Postgres
  performance lever.
- **Let Postgres maintain the vector.** A `GENERATED ALWAYS AS (...) STORED`
  column (or an `UPDATE` trigger) keeps the index correct without application
  code and survives writes that bypass the ORM.
- **Match the `Config` to your language.** `english` applies stemming and
  English stop-words; use `simple` for exact-token, language-agnostic matching.
  The Memory engine's folding/stop-words are fixed and English-leaning.
- **`Total` is exact.** The window-function count reflects all matches before
  pagination — safe to drive page navigation, but it does scan the full match
  set, so very broad queries cost more.
- **Program against `Engine`, not the concrete type.** Inject
  `search.Engine` so you can swap Memory (tests) for Postgres (production)
  without touching call sites.
```
