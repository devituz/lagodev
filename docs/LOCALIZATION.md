# Localization

Two small packages cover everything an app needs to speak more than one
language and to reason about wall-clock time:

- **`i18n`** — translations modelled on Laravel's `Lang` facade. JSON
  message files keyed by locale, `:name` placeholders, and count-aware
  pluralisation.
- **`carbon`** — an ergonomic `time.Time` wrapper modelled on Laravel's
  Carbon. Multi-layout parsing, boundary helpers, chainable arithmetic,
  and human-readable diffs.

Neither package depends on the rest of the framework, so both drop into
any Go program. The [integration](#integration-locale-from-the-request)
section shows how to wire the active locale to an incoming HTTP request.

---

## i18n

### Message files

Messages live in one flat JSON file per locale, named by locale code:

```
resources/lang/
├── en.json
├── uz.json
└── ru.json
```

```json
// resources/lang/en.json
{
    "welcome": "Hello :name",
    "auth": {
        "failed": "These credentials do not match our records."
    },
    "apples": {
        "zero": "No apples",
        "one": "One apple",
        "other": ":count apples"
    }
}
```

Nested objects are flattened with `.`, so `auth.failed` above is looked
up as `t.Get("auth.failed")`. An object whose keys are *only* plural
forms (`zero`, `one`, `two`, `few`, `many`, `other`) is kept intact for
`Choice` instead of being flattened.

### Loading translations

```go
import "github.com/devituz/lagodev/i18n"

tr, err := i18n.Load("resources/lang", "en")
if err != nil {
    log.Fatal(err)
}
```

`Load` reads every `*.json` file in the directory, using the basename as
the locale code, and sets both the active locale and the fallback locale
to `defaultLocale`. If the default locale file is missing it still
returns a usable `*Translator` together with an error wrapping
`i18n.ErrLocaleMissing` — useful when you intend to supply messages later
via `Add`.

For embedded files, use `LoadFS` with any `io/fs.FS`:

```go
import "embed"

//go:embed resources/lang/*.json
var langFS embed.FS

tr, err := i18n.LoadFS(langFS, "resources/lang", "en")
```

Register a locale programmatically (handy in tests):

```go
tr.Add("en", map[string]any{
    "welcome": "Hello :name",
    "apples": map[string]any{
        "one":   "One apple",
        "other": ":count apples",
    },
})
```

### Locale selection

A `*Translator` carries an *active* locale and a *fallback* locale. The
default of both is the `defaultLocale` passed to `Load`.

```go
tr.Default()             // "en" — the locale set at Load time
tr.Current()             // "en" — the active locale right now
tr.SetFallback("en")     // missing keys fall back to en
```

`WithLocale` returns a shallow clone with a different active locale. The
underlying (read-only) message map is shared, so cloning per request is
cheap and goroutine-safe:

```go
uz := tr.WithLocale("uz")
uz.Get("welcome", i18n.M{"name": "Ada"}) // "Salom Ada"
tr.Current()                              // still "en" — original untouched
```

### Translating with parameters

`Get` resolves a key in the active locale, falling back to the fallback
locale, and substitutes `:placeholder` tokens. `i18n.M` is just
`map[string]string`:

```go
tr.Get("welcome", i18n.M{"name": "Ada"})   // "Hello Ada"
tr.Get("auth.failed")                       // no params needed
tr.Has("welcome")                           // true
```

If a key exists in neither the active nor the fallback locale, `Get`
returns the **key itself** — a deliberate signal that surfaces missing
translations during development rather than crashing.

When `Get` lands on a plural map (a key with no count), it returns the
`other` form. It does **not** inject `:count` automatically — use
`Choice` for count-aware messages.

### Pluralization

`Choice` picks the plural form by count:

- `count == 0` → `zero` if defined, else `other`
- `count == 1` → `one` if defined, else `other`
- everything else → `other`

The count is also exposed as `:count` automatically, so the common case
needs no extra replacement map:

```go
tr.Choice("apples", 0)   // "No apples"
tr.Choice("apples", 1)   // "One apple"
tr.Choice("apples", 5)   // "5 apples"   (:count filled in for you)
```

Extra placeholders merge on top of the injected `:count`:

```go
// "other": ":count apples for :name"
tr.Choice("apples", 3, i18n.M{"name": "Ada"}) // "3 apples for Ada"
```

---

## carbon

`carbon.Carbon` wraps `time.Time` with the helpers stdlib makes you
write by hand. Because it carries a `time.Time`, call `.Time()` to hand
it to any API that expects the standard type.

### Parsing

```go
import "github.com/devituz/lagodev/carbon"

t, err := carbon.Parse("2026-06-25 14:30:00")
if err != nil {
    // none of the known layouts matched
}

// Panics on error — only for hard-coded literals:
t = carbon.MustParse("2026-06-25")

// Wrap an existing time.Time:
t = carbon.From(someStdlibTime)
```

`Parse` tries each layout in `carbon.ParseLayouts` in order and returns
the first that decodes. The default set covers the shapes lagodev apps
see in practice:

```go
var ParseLayouts = []string{
    time.RFC3339Nano,
    time.RFC3339,
    "2006-01-02 15:04:05", // SQL datetime
    "2006-01-02T15:04:05",
    "2006-01-02",          // ISO date
    "02/01/2006",          // DD/MM/YYYY
    "01/02/2006",          // MM/DD/YYYY
}
```

`ParseLayouts` is an exported package variable; append your own layouts
at startup if you need to.

### Constructors and arithmetic

```go
now   := carbon.Now()                 // local timezone
utc   := carbon.NowIn(time.UTC)       // a specific location

later := now.AddDays(7).AddHours(3)   // chainable
prev  := now.SubMonths(1)
```

Available chainable helpers (each returns a new `Carbon`):

```go
.Add(d time.Duration)
.AddSeconds(n)  .AddMinutes(n)  .AddHours(n)
.AddDays(n)     .AddWeeks(n)    .AddMonths(n)  .AddYears(n)
.SubDays(n)     .SubMonths(n)   .SubYears(n)
```

Boundary helpers snap to the edges of a day, month, or year:

```go
now.StartOfDay()    // 00:00:00 today
now.EndOfDay()      // 23:59:59.999999999 today
now.StartOfMonth()  // first day, 00:00
now.EndOfMonth()    // last instant of the month
now.StartOfYear()   // Jan 1, 00:00
now.EndOfYear()     // last instant of the year
```

### Formatting

```go
t := carbon.MustParse("2026-06-25 14:30:00")

t.Date()     // "2026-06-25"
t.DateTime() // "2026-06-25 14:30:00"
t.ISO8601()  // "2026-06-25T14:30:00Z"  (Format(time.RFC3339))
t.Format("Mon, 02 Jan 2006")
t.Time()     // the underlying time.Time
```

### Comparisons, diffs, and humanize

```go
a := carbon.MustParse("2026-06-25")
b := carbon.MustParse("2026-06-20")

a.After(b)        // true
a.Before(b)       // false
a.Equal(b)        // false
a.Sub(b)          // time.Duration (a - b)
a.DiffInDays(b)   // 5  (calendar days, rounded toward zero)

a.IsPast()        // relative to Now()
a.IsFuture()
a.IsToday()
a.IsZero()        // zero-value guard
```

`DiffForHumans` renders a relative phrase of `(c - o)`:

```go
past   := carbon.Now().SubDays(2)
future := carbon.Now().AddHours(1)

past.DiffForHumans(carbon.Now())   // "2 days ago"
future.DiffForHumans(carbon.Now()) // "1 hour from now"
```

### Timezones

`Now` uses the local zone; `NowIn` takes an explicit `*time.Location`.
There is no dedicated conversion method — convert through stdlib and
re-wrap with `From`:

```go
loc, _ := time.LoadLocation("Asia/Tashkent")

t := carbon.Now()
tashkent := carbon.From(t.Time().In(loc)) // same instant, Tashkent wall clock

tashkent.DateTime() // formatted in +05:00
```

---

## Integration: locale from the request

The `web` layer does not bind a locale for you — wire it explicitly so
each request gets its own `*Translator` clone. Keep one shared
`*Translator` (loaded once at boot) and clone per request with
`WithLocale`; the clone is cheap and the shared message map is read-only.

```go
package routes

import (
    "strings"

    "github.com/devituz/lagodev/i18n"
    "github.com/devituz/lagodev/web"
)

// tr is loaded once at startup (e.g. in main) and shared.
func Register(app *web.App, tr *i18n.Translator) {
    app.Get("/greeting", func(c *web.Context) (any, error) {
        loc := localeFromRequest(c, tr)
        t := tr.WithLocale(loc)

        name := c.QueryDefault("name", "stranger")
        return map[string]string{
            "locale":  t.Current(),
            "message": t.Get("welcome", i18n.M{"name": name}),
        }, nil
    })
}

// localeFromRequest resolves the locale from an explicit ?lang= override,
// then the Accept-Language header, falling back to the translator default.
func localeFromRequest(c *web.Context, tr *i18n.Translator) string {
    if l := c.Query("lang"); l != "" && tr.WithLocale(l).Has("welcome") {
        return l
    }
    if al := c.Request.Header.Get("Accept-Language"); al != "" {
        // "uz-UZ,uz;q=0.9,en;q=0.8" → "uz"
        primary := strings.SplitN(strings.SplitN(al, ",", 2)[0], "-", 2)[0]
        if tr.WithLocale(primary).Has("welcome") {
            return primary
        }
    }
    return tr.Default()
}
```

`c.Request` is the underlying `*http.Request`, so any header is available
through `c.Request.Header`. For per-user timezone display, store the
user's zone and render times through `carbon.From(t.Time().In(loc))`.

---

## Production notes

- **Load once, clone per request.** `Load`/`LoadFS` read and parse files
  from disk — do it at boot, not per request. `WithLocale` is a cheap,
  goroutine-safe clone over a shared read-only message map.
- **Embed for single-binary deploys.** Use `LoadFS` with `embed.FS` so
  translations ship inside the binary; no `resources/lang` directory to
  copy alongside the executable.
- **Treat returned keys as missing translations.** `Get` returning the
  raw key (e.g. `"auth.failed"` verbatim) means the key is absent in both
  the active and fallback locale. Catch these in tests with `Has`.
- **Always set a sane fallback.** `SetFallback("en")` ensures partially
  translated locales degrade to a complete one instead of leaking keys to
  end users.
- **Store time in UTC, present in the user's zone.** Persist with
  `carbon.NowIn(time.UTC)` (or stdlib UTC) and convert for display only;
  this keeps diffs and comparisons unambiguous across DST boundaries.
- **`MustParse` is for literals only.** It panics on bad input — never
  feed it user-supplied or external data. Use `Parse` and handle the
  error at request boundaries.
- **Extend `ParseLayouts` at startup**, before any concurrent parsing, if
  your inputs use a layout outside the default set — it is a plain
  package-level slice with no synchronisation.

## See also

- **[GETTING_STARTED.md](GETTING_STARTED.md)** — project scaffolding and
  the `web` server.
- **[WEB.md](WEB.md)** — `web.Context`, middleware, and the request
  lifecycle.
