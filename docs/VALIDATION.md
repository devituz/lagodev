# Validation

`lagodev/validation` is a dependency-free, Laravel-FormRequest-grade
validator for structs and `map[string]any`. It pulls in nothing outside
the standard library, and it never imports `web` — so it stays a leaf
package you can use anywhere, framework or not.

> Available since **v0.21.0**. Two entry points cover most needs:
> `validation.Validate(&v)` for tagged structs, `validation.Map(data,
> rules)` for decoded JSON.

## Overview

There are two ways to declare rules, and they validate identically:

- **Struct tags** — `validate:"required,email,max=255"` on each field.
  Read by `Validate` / `ValidateWith`.
- **Programmatic `Rules`** — a `map[string][]string` (or the fluent
  `Builder`). Read by `Map` / `MapWith`.

Either way you get back a `ValidationErrors` (a `map[string][]string`
that implements `error`). Recover it with `errors.As`, and turn it into
a 422 JSON body with `validation.Response`.

Field names in errors use the `json` tag when present, otherwise the Go
field name.

## Quick start — validate a struct

```go
package main

import (
    "errors"
    "fmt"

    "github.com/devituz/lagodev/validation"
)

type RegisterRequest struct {
    Name                 string `json:"name" validate:"required,max=255"`
    Email                string `json:"email" validate:"required,email"`
    Age                  int    `json:"age" validate:"gte=18,lte=120"`
    Password             string `json:"password" validate:"required,min=8,confirmed"`
    PasswordConfirmation string `json:"password_confirmation"`
    Role                 string `json:"role" validate:"in=admin|user|guest"`
}

func main() {
    req := RegisterRequest{Email: "bad", Age: 5, Password: "short"}

    if err := validation.Validate(&req); err != nil {
        var ve validation.ValidationErrors
        if errors.As(err, &ve) {
            for _, field := range ve.Fields() {
                fmt.Printf("%s: %s\n", field, ve.First(field))
            }
        }
    }
}
```

`Validate` accepts a struct or a pointer to one. It returns `nil` when
every field passes, otherwise a `ValidationErrors`.

## Built-in rules

The complete rule set lives in `validation/rules.go`. Empty strings pass
size/format rules — use `required` to forbid an empty value first.

| Rule              | Argument            | Checks                                                            |
|-------------------|---------------------|-------------------------------------------------------------------|
| `required`        | —                   | Non-empty: trimmed string, non-zero len, non-nil ptr, non-zero    |
| `email`           | —                   | Parses as an RFC 5322 address (`net/mail`)                        |
| `url`             | —                   | Absolute URL with scheme + host (`url.ParseRequestURI`)          |
| `min=N`           | number              | String/slice/map length **or** numeric value `>= N`              |
| `max=N`           | number              | String/slice/map length **or** numeric value `<= N`              |
| `len=N`           | number              | Length / numeric value `== N`                                    |
| `gte=N`           | number              | Numeric `>= N` (numeric strings parsed by value, not length)     |
| `lte=N`           | number              | Numeric `<= N`                                                    |
| `gt=N`            | number              | Numeric `> N`                                                    |
| `lt=N`            | number              | Numeric `< N`                                                    |
| `numeric`         | —                   | String parses as a float                                         |
| `integer`         | —                   | String parses as a base-10 int                                   |
| `alpha`           | —                   | `^[A-Za-z]+$`                                                    |
| `alphanumeric`    | —                   | `^[A-Za-z0-9]+$`                                                 |
| `uuid`            | —                   | Canonical 8-4-4-4-12 UUID                                        |
| `boolean`         | —                   | Bool kind, or string `true`/`false`/`1`/`0`                      |
| `in=a\|b\|c`      | pipe list           | Value is one of the listed tokens                               |
| `notin=a\|b`      | pipe list           | Value is **not** one of the listed tokens                       |
| `regex=...`       | pattern             | Matches the pattern (must be the **last** rule in a tag)         |
| `datetime=LAYOUT` | Go time layout      | Parses with the layout; empty arg defaults to `time.RFC3339`     |
| `eqfield=Other`   | sibling name        | Equals the value of another field (by reported name)            |
| `nefield=Other`   | sibling name        | Differs from another field                                       |
| `confirmed`       | —                   | Matches `<field>_confirmation` (or `<Field>Confirmation`)        |
| `dive`            | —                   | Recurse into a nested struct or slice/array of structs          |

### Numeric vs length semantics

`min`/`max`/`len` are size rules: on a string/slice/map they compare the
**length**, on a number they compare the **value**. `gte`/`lte`/`gt`/`lt`
are always numeric — a numeric string like `"20"` compares by its parsed
value, not its length. Choose accordingly:

```go
Password string `json:"password" validate:"min=8"`   // >= 8 chars
Age      int    `json:"age"      validate:"gte=18"`   // value >= 18
Tags     []string `json:"tags"   validate:"max=5"`    // <= 5 elements
```

### `regex` is greedy

Because a pattern may contain commas, the `regex=` token swallows the
rest of the tag verbatim. It must be the final rule:

```go
Slug string `json:"slug" validate:"required,regex=^[a-z0-9-]{3,40}$"`
```

### Nested structs and slices — `dive`

```go
type order struct {
    Address address     `json:"address" validate:"dive"`
    Items   []orderItem `json:"items"   validate:"dive"`
}
```

Nested failures are reported with dotted/indexed paths, e.g.
`address.city` or `items.0.sku`.

## Struct-tag vs programmatic rules

When you already have a decoded `map[string]any` (raw JSON, form values)
and no struct to bind to, validate the map directly with `Map`:

```go
data := map[string]any{"email": "bad", "age": 5}

err := validation.Map(data, validation.Rules{
    "email": {"required", "email"},
    "age":   {"required", "integer", "gte=18"},
})
```

Cross-field rules (`eqfield`, `nefield`, `confirmed`) resolve against
other keys in the same map. Absent keys are treated as empty, so
`required` catches them.

### Fluent `Builder`

`validation.New()` is sugar over the `Rules` map — handy when you build
rules conditionally:

```go
rules := validation.New().
    Field("email", "required", "email").
    Field("age", "integer", "gte=18").
    Rules()

err := validation.Map(data, rules)

// Or validate in one call:
err = validation.New().
    Field("email", "required", "email").
    Validate(data)
```

Calling `Field` twice for the same name appends rules.

## Custom rules

There is **no public rule-registration API** — the rule registry in
`rules.go` is internal and read-only after `init()`. For project-specific
checks, run your own logic and add to the same `ValidationErrors` so the
result merges cleanly with the built-in rules:

```go
func validateRegister(req *RegisterRequest) error {
    ve := validation.ValidationErrors{}

    if err := validation.Validate(req); err != nil {
        var built validation.ValidationErrors
        if errors.As(err, &built) {
            for f, msgs := range built {
                for _, m := range msgs {
                    ve.Add(f, m)
                }
            }
        }
    }

    // Custom business rule the tag set can't express:
    if reservedUsername(req.Name) {
        ve.Add("name", "is reserved")
    }

    if len(ve) > 0 {
        return ve
    }
    return nil
}
```

`ValidationErrors` exposes `Add`, `Has`, `First`, `Fields`, and `ToMap`
for inspecting and building results by hand.

## Error messages and i18n

Override the default message per rule, or per field+rule, with a
`Messages` table. The most specific key wins: `email.required` beats
`required`.

```go
msgs := validation.Messages{
    "email.required": "We need your email address",
    "required":       "is mandatory",
    "min":            "is too short",
}

err := validation.ValidateWith(&req, msgs)   // struct form
err = validation.MapWith(data, rules, msgs)  // map form
```

For localisation, build the `Messages` table per request from your i18n
catalogue keyed by `<field>.<rule>` / `<rule>`, and pass it to
`ValidateWith` / `MapWith`. There is no global locale state — message
selection is explicit and per-call, which keeps it concurrency-safe.

## Integration with web request binding

`validation` deliberately does not import `web`, so it exposes only the
response *shape* (`ErrorResponse`) and you wire it in your handler.
`validation.Response(ve)` produces a Laravel-style 422 body:

```json
{
  "message": "The given data was invalid.",
  "errors": {
    "email": ["must be a valid email address"],
    "age":   ["must be >= 18"]
  }
}
```

Typical handler — bind, validate, emit 422:

```go
func (h *Handler) Store(c *web.Context) (any, error) {
    var req RegisterRequest
    if err := c.Bind(&req); err != nil {
        return nil, err // 400 already written by Bind on malformed JSON
    }

    if err := validation.Validate(&req); err != nil {
        var ve validation.ValidationErrors
        if errors.As(err, &ve) {
            c.JSON(422, validation.Response(ve))
            return nil, nil
        }
        return nil, err
    }

    return h.Service.Create(c.Ctx(), req)
}
```

The `return nil, nil` after `c.JSON(422, ...)` tells the framework you
already wrote the response (see [WEB.md](WEB.md) — the `(any, error)`
contract). A non-`ValidationErrors` error falls through to the framework
and becomes a 500.

You can factor the bind+validate+422 dance into a small middleware or
helper of your own, since both `Bind` and `Validate` are ordinary
function calls with no hidden state.

## Production notes

- **No reflection cache.** `Validate` reflects over the struct on every
  call. For hot paths validating millions of structs/sec, prefer the
  `Map` form against an already-decoded payload, or validate once at the
  edge rather than per layer.
- **Compiled regexes are memoised.** Each distinct `regex=` pattern is
  compiled once and cached for the process lifetime. Keep the set of
  patterns bounded (they come from your tags, so this is automatic) —
  never feed user input as a `regex=` argument.
- **Empty passes by design.** Format/size rules skip empty values so
  optional fields validate only when present. Always pair them with
  `required` when the field is mandatory.
- **Deterministic output.** `ValidationErrors.Error()` and `Fields()`
  sort field names, so log lines and test assertions are stable.
- **`ToMap` returns a copy.** Safe to mutate or serialise without
  touching the underlying error.
- **Cross-field by reported name.** `eqfield`/`nefield` reference the
  *reported* name (the `json` tag if present), not the Go field name.
  `confirmed` looks for `<field>_confirmation` first, then the CamelCase
  `<Field>Confirmation` fallback.

## See also

- [WEB.md](WEB.md) — the `(any, error)` handler contract and `c.Bind`.
- [GETTING_STARTED.md](GETTING_STARTED.md) — scaffolding a request flow.
- `validation/rules.go` — the authoritative rule list and messages.
