# Benchmarks

Micro-benchmarks for lagodev's hot paths. They live in the `benchmarks/`
package and use only the stdlib `testing` harness — no third-party benchmark
dependencies.

## What these are (and are not)

These measure **framework overhead in isolation**: the cost lagodev adds on top
of the standard library for a given operation (route dispatch, struct
validation, resource serialization, DI resolution, query building, collection
ops). They are intentionally narrow.

They are **not**:

- End-to-end throughput numbers. There is no real network, no real database
  round-trip on most of them (the SQLite ones hit an in-memory DB).
- A comparison against Gin/Fiber/Echo or any other framework. Comparing
  micro-benchmarks across frameworks is misleading — different harnesses
  measure different boundaries.
- An absolute promise of performance on your hardware. Treat them as relative
  signals: which paths allocate, which are cheap, and how a change moves the
  needle.

If you want to know whether a change made something faster or slower, run the
suite before and after and compare — that is what these are for.

## Running

From the repo root:

```sh
cd benchmarks
go test -bench=. -benchmem -run=^$ ./...
```

- `-bench=.` runs all benchmarks.
- `-benchmem` reports `B/op` and `allocs/op` (every benchmark also calls
  `b.ReportAllocs()`).
- `-run=^$` skips unit tests so only benchmarks run.

Narrow to one path with a regex, e.g. just the router:

```sh
go test -bench=Router -benchmem -run=^$ ./benchmarks
```

For more stable numbers, increase the sample time and pin the count:

```sh
go test -bench=. -benchmem -run=^$ -benchtime=3s -count=5 ./benchmarks
```

Then feed the output to `benchstat` (from `golang.org/x/perf`) to compare two
runs:

```sh
go test -bench=. -benchmem -run=^$ -count=10 ./benchmarks > old.txt
# ...make a change...
go test -bench=. -benchmem -run=^$ -count=10 ./benchmarks > new.txt
benchstat old.txt new.txt
```

## Measured results

Numbers below were captured on the **dev machine**, not a controlled CI rig.
They are indicative, not absolute — your hardware, Go version, and machine load
will shift them. Reproduce locally before drawing conclusions.

- Machine: Apple M1 Pro (`darwin/arm64`)
- Go: `go1.26.4`
- Command: `go test -bench=. -benchmem -run=^$ ./...`

| Benchmark | What it exercises | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| `Router_MatchStatic` | match + dispatch + JSON encode, static route, full `web.App` pipeline¹ | 5359 | 6110 | 87 |
| `Router_MatchParam` | same pipeline with a path parameter (`/users/{id}`)¹ | 5229 | 6126 | 88 |
| `MiddlewareChain` | request through a 5-deep pass-through middleware stack¹ | 1084 | 1736 | 23 |
| `BindAndValidate` | JSON decode + struct validation, end-to-end through a POST¹ | 5149 | 9596 | 59 |
| `Validate_Struct` | struct `validate:"..."` tag validator, valid payload | 2042 | 912 | 29 |
| `Validate_Map` | map-based validator (decoded JSON, no struct) | 636 | 536 | 8 |
| `Builder_ToSQL` | simple SQL composition (3 wheres + order + limit), no I/O | 907 | 1072 | 29 |
| `Query_Complex` | heavier SQL build: join, IN, not-null, group, order, limit | 2882 | 3576 | 73 |
| `Resource_Item` | transform one model → `Fields` | 168 | 384 | 5 |
| `Resource_Collection` | transform a 50-row page through the resource layer | 8175 | 20096 | 251 |
| `Resource_CollectionJSON` | resource transform + `encoding/json` marshal (real response) | 38167 | 42730 | 753 |
| `Resource_Paginated` | render a 15-row `orm.Paginator` page incl. meta block | 2514 | 6000 | 76 |
| `Collection_FilterMapSort` | immutable chain (filter→sort→take) over 1k elements² | 156926 | 70208 | 8 |
| `Collection_MapReduce` | type-changing `Map` then `Reduce` over 1k elements | 1483 | 8192 | 1 |
| `Collection_GroupBy` | group 1k elements by key | 35033 | 89512 | 349 |
| `Container_MakeSingleton` | resolve a warm cached singleton (DI hot path) | 3600 | 96 | 2 |
| `Container_MakeTransient` | resolve a transient (factory runs each `Make`) | 9983 | 232 | 5 |

The SQLite-backed ORM benchmarks (`Insert`, `BatchInsert`, `SelectAll`) also
run; they include in-memory DB work and are not framework-overhead-only, so
they are omitted from the table above. Run the suite to see them.

### Reading notes

1. The `web.App` benchmarks dispatch through `App.Test()`, which materialises
   the route table on each call. That fixed setup cost is included in the
   per-op number, so these read higher in allocations than the raw matcher
   alone — they reflect the cost of a full request from the app boundary, which
   is the honest thing to report. They are still useful for tracking regressions
   in the dispatch + middleware + bind/validate path as a whole.
2. `Collection_FilterMapSort` is dominated by the immutability contract: each
   chained step copies the backing slice (documented behaviour). The high
   ns/op is the sort over 1k elements, not allocation pressure — note the low
   `allocs/op`. This is the cost of the ergonomic, copy-on-reshape API; if you
   need raw throughput on large slices, operate on `[]T` directly.

Lower is better for all three columns. `allocs/op` is usually the most
actionable: reducing allocations is what moves these paths under sustained
load.
