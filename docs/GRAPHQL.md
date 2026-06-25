# GraphQL cookbook

`github.com/devituz/lagodev/graphql` is a dependency-free, struct-first
GraphQL execution engine. You describe a schema in plain Go — objects,
fields, arguments and resolver functions — and the package parses incoming
query documents, executes the requested selection set, and produces the
standard `{"data": ..., "errors": [...]}` JSON envelope. It ships a
hand-written lexer + parser and an `http.Handler`, and pulls in nothing
outside the standard library.

## What it covers (and what it doesn't)

This is a pragmatic core, not a full GraphQL-spec implementation. The
unsupported edges error or are ignored — never silently wrong.

Supported:

- Operations: `query` and `mutation`, anonymous or named.
- Selection sets: nested fields, aliases (`alias: field`), arguments.
- Arguments: `Int`, `Float`, `String`, `Boolean`, `ID`, enum values,
  lists, input objects, `null`, and variable references (`$var`).
- Variables declared in the operation, supplied via the request, coerced
  to the declared type.
- Fragments: inline (`... on Type { ... }`) and named definitions
  referenced via spreads (`...Name`).
- Types: `Object`, `Scalar`, `Enum`, `InputObject`, plus `NewList` and
  `NewNonNull` wrappers.
- Execution: depth-first resolution, per-field error collection with a
  response path, default field resolution from struct fields / maps.
- The `__typename` meta field on every object.

Not supported:

- Directives (`@skip`, `@include`, custom) — a directive in the document
  is a parse error.
- Subscriptions.
- Full `__schema` / `__type` introspection (only `__typename`).
- Custom scalar coercion hooks beyond the five built-ins.

## Quick start

Build a schema, then execute a query against it.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"

    "github.com/devituz/lagodev/graphql"
)

type user struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

var users = map[string]*user{
    "1": {ID: "1", Name: "Ann"},
    "2": {ID: "2", Name: "Bob"},
}

func main() {
    userType := &graphql.Object{
        Name: "User",
        Fields: graphql.Fields{
            "id":   &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
            "name": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
        },
    }

    queryType := &graphql.Object{
        Name: "Query",
        Fields: graphql.Fields{
            "user": &graphql.Field{
                Type: userType,
                Args: graphql.Args{"id": {Type: graphql.NewNonNull(graphql.ID)}},
                Resolve: func(_ context.Context, _ any, a graphql.ArgValues) (any, error) {
                    return users[a.String("id")], nil
                },
            },
        },
    }

    cs, err := graphql.NewSchema(graphql.Schema{Query: queryType})
    if err != nil {
        panic(err)
    }

    res := graphql.Execute(context.Background(), cs, graphql.Request{
        Query: `{ user(id: "1") { id name } }`,
    })

    out, _ := json.Marshal(res)
    fmt.Println(string(out))
    // {"data":{"user":{"id":"1","name":"Ann"}}}
}
```

`NewSchema` validates the schema once (rejecting nil field/arg types and a
missing `Query`) and returns a `*CompiledSchema` that is safe for
concurrent use. Reuse it across requests.

## Types & fields

Every GraphQL type implements the `Type` interface. You compose them as
plain struct literals.

| Type          | Purpose                                            |
|---------------|----------------------------------------------------|
| `Object`      | Output object — a named set of resolvable `Fields` |
| `Scalar`      | Leaf value (`Int`, `Float`, `String`, `Boolean`, `ID`) |
| `Enum`        | Fixed set of named values                          |
| `InputObject` | Structured argument type                           |
| `NewList(t)`  | `[t]` wrapper                                       |
| `NewNonNull(t)` | `t!` wrapper                                      |

The five built-in scalars are package-level variables: `graphql.Int`,
`graphql.Float`, `graphql.String`, `graphql.Boolean`, `graphql.ID`. Wrap
them to express nullability and lists, e.g. a non-null list of non-null
posts:

```go
"posts": &graphql.Field{Type: graphql.NewList(graphql.NewNonNull(postType))}
```

A `Field` carries its result `Type`, optional `Args`, an optional
`Description`, and an optional `Resolve` function:

```go
type Field struct {
    Type        Type
    Args        Args
    Description string
    Resolve     ResolveFunc
}
```

### Default resolution

When `Resolve` is nil, the executor reads the value straight off the
parent. For a `map[string]any` parent it looks up the field name; for a
struct it matches (in order) the exact field name, a `graphql:"..."` or
`json:"..."` tag, then a case-insensitive name match. This is why the
`id`/`name` fields above need no resolver — the `*user` struct supplies
them through its `json` tags.

### Enums

```go
statusEnum := &graphql.Enum{
    Name: "Status",
    Values: map[string]*graphql.EnumValue{
        "ACTIVE":  {Value: "active"},
        "BLOCKED": {Value: "blocked"},
    },
}
```

`EnumValue.Value` is the Go value the resolver sees on input and is
compared against on output. When it is nil the enum member's name is used.

### Input objects

```go
filterInput := &graphql.InputObject{
    Name: "Filter",
    Fields: graphql.Args{
        "status": {Type: statusEnum},
        "limit":  {Type: graphql.Int, Default: int64(20)},
    },
}
```

An `InputObject`'s `Fields` are an `Args` map — each entry has a `Type`
and an optional `Default`. Input objects may nest other input objects and
lists.

## Resolvers

A resolver has this signature:

```go
type ResolveFunc func(ctx context.Context, parent any, args ArgValues) (any, error)
```

- `parent` is the value returned by the enclosing field's resolver (or
  `Request.Root` for top-level fields).
- `args` are the coerced arguments.
- Returning an error surfaces it in the response `errors` array with the
  field's path; the field becomes `null` without aborting sibling fields.

`ArgValues` is the coerced argument map. Use the typed accessors rather
than asserting yourself — they coerce and default to a zero value:

```go
a.String("id")     // string
a.Int("limit")     // int64 (coerces float64)
a.Float("score")   // float64
a.Bool("active")   // bool
a.List("ids")      // []any
a.Input("filter")  // map[string]any (input object)
v, ok := a.Get("x") // raw value + presence
```

A nested resolver receives the parent it was resolved from. Here `posts`
loads from the `*user` returned one level up:

```go
"posts": &graphql.Field{
    Type: graphql.NewList(graphql.NewNonNull(postType)),
    Resolve: func(_ context.Context, parent any, _ graphql.ArgValues) (any, error) {
        u, _ := parent.(*user)
        if u == nil {
            return nil, nil
        }
        return loadPosts(u.ID), nil
    },
},
```

Always thread `ctx` into your data layer — the HTTP handler propagates the
request context, so cancellation and deadlines follow the connection.

## Queries & mutations

`Schema` has two roots — `Query` (required) and `Mutation` (optional):

```go
schema := graphql.Schema{
    Query:    queryType,
    Mutation: mutationType,
}
cs, err := graphql.NewSchema(schema)
```

A mutation field is just a `Field` on the `Mutation` object; it resolves
exactly like a query field. Executing a `mutation { ... }` operation when
no `Mutation` root is defined returns an error.

```go
mutationType := &graphql.Object{
    Name: "Mutation",
    Fields: graphql.Fields{
        "createUser": &graphql.Field{
            Type: userType,
            Args: graphql.Args{
                "name": {Type: graphql.NewNonNull(graphql.String)},
            },
            Resolve: func(ctx context.Context, _ any, a graphql.ArgValues) (any, error) {
                return createUser(ctx, a.String("name"))
            },
        },
    },
}
```

### Variables and named operations

When a document defines more than one operation, supply
`Request.OperationName`. Variables declared in the operation are coerced
to their declared types and read through `$name`:

```go
query := `
    query Feed($role: String) {
      users(role: $role) { name posts { id title } }
    }`

res := graphql.Execute(ctx, cs, graphql.Request{
    Query:     query,
    Variables: map[string]any{"role": "admin"},
})
```

`Request.Root`, when non-nil, is the parent value handed to top-level
resolvers — useful for injecting a request-scoped object.

## HTTP handler integration

`graphql.Handler(cs)` returns an `http.Handler` speaking the conventional
GraphQL-over-HTTP JSON protocol. Mount it on any router, including the
lagodev `web` app:

```go
import (
    "net/http"

    "github.com/devituz/lagodev/graphql"
    "github.com/devituz/lagodev/web"
)

func Register(app *web.App) {
    cs := buildSchema()
    h := graphql.Handler(cs)

    // Adapt the std http.Handler onto a web route.
    app.Post("/graphql", func(c *web.Context) (any, error) {
        h.ServeHTTP(c.Writer, c.Request)
        return nil, nil
    })
}
```

The handler accepts:

- **POST** with `Content-Type: application/json` and a body of
  `{"query": ..., "operationName": ..., "variables": {...}}`.
- **GET** with `?query=`, optional `?operationName=` and `?variables=`
  (a JSON-encoded object) — suitable for read-only requests.

Transport-level problems (wrong method, unreadable body, malformed JSON)
return a JSON error envelope with the matching status code. Resolver and
validation errors surface through the normal `errors` array with a `200`,
per GraphQL convention. The body is capped at 1 MiB.

A raw client round-trip:

```bash
curl -X POST http://localhost:8080/graphql \
    -H "Content-Type: application/json" \
    -d '{"query":"{ user(id:\"1\"){ name posts { title } } }"}'
# {"data":{"user":{"name":"Ann","posts":[{"title":"Hello"}]}}}
```

## Fragments & error handling

Both inline and named fragments are supported and flattened into the
parent selection during execution. A type condition matches against the
enclosing object's `Name`; an untyped inline fragment always applies.

```graphql
query {
  user(id: "1") {
    ...userFields
    ... on User { role }
  }
}

fragment userFields on User {
  id
  name
}
```

Fragment-spread cycles (`fragment F on T { ...F }`) are detected and
rejected before execution — they would otherwise drive unbounded
recursion. An unknown spread target produces a field-level error.

### Errors and null propagation

`Response` marshals to `{"data": ..., "errors": [...]}`, omitting an empty
errors array. Each `GqlError` carries a `Message` and, for field errors, a
`Path` (field names and list indices, root-to-leaf):

```json
{
  "data": { "user": null },
  "errors": [
    { "message": "graphql: user not found", "path": ["user", "name"] }
  ]
}
```

Null propagation follows the spec: a resolver error or a `null` returned
for a `NonNull` field nullifies that field; if the field itself is
non-null, the null bubbles up to the enclosing object, and through a
non-null list element it nullifies the whole list. Sibling fields are
unaffected — every reachable field is still resolved and its own errors
collected. Returning a `GqlError` from a resolver works directly: it
implements the `error` interface.

A parse error, a missing/ambiguous operation, a failed variable coercion,
or a missing `Mutation` root yields a `Response` with `Data` nil and a
single error.

## Production notes

- **Compile once, reuse.** `NewSchema` validates the whole type graph up
  front; `*CompiledSchema` is immutable and concurrency-safe. Build it at
  startup, not per request.
- **Push auth and rate limiting to the transport.** The engine has no
  directives and no query-depth/complexity limits. Wrap `Handler(cs)` in
  your own middleware (authn, depth caps, persisted-query allow-lists)
  before exposing it publicly. The 1 MiB body cap is the only built-in
  guard.
- **Thread `context`.** Cancellation propagates from the HTTP request into
  every resolver — honour it in your data layer to shed load on client
  disconnects.
- **Avoid N+1.** Nested resolvers run depth-first; a list field that
  resolves a sub-field per element will fan out queries. Batch or
  pre-load inside the parent resolver where it matters.
- **Errors are partial, not fatal.** A single failing field returns
  `data` for everything else plus a pathed error entry — design clients to
  read both `data` and `errors`.
- **Honest gaps.** No subscriptions, no `@skip`/`@include`, no full
  introspection. If a tool needs `__schema`, this engine won't serve it
  (only `__typename` is available).

## API reference

Entry points used throughout this guide:

```go
func NewSchema(s Schema) (*CompiledSchema, error)
func Execute(ctx context.Context, cs *CompiledSchema, req Request) Response
func Handler(cs *CompiledSchema) http.Handler
func NewList(t Type) *List
func NewNonNull(t Type) *NonNull
```

Run `go doc github.com/devituz/lagodev/graphql` for the full surface.
</content>
</invoke>
