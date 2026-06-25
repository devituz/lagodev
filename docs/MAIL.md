# Mail — sending email

`lagodev/mail` is a small `Mailer` abstraction modelled on Laravel's `Mail`
facade. You compose a `Message` with a fluent builder, then hand it to a
`Mailer` for delivery. The transport is pluggable: the core package ships an
RFC-compliant **SMTP** driver plus an in-memory **log** driver for tests, and
two HTTP-API drivers live in sub-packages — `mail/sendgrid` and
`mail/mailgun`.

> The package is a **leaf**: a `Message` is plain data and `Mailer.Send` takes
> a `context.Context`, so it composes with the rest of the framework (queue
> workers, web handlers) without importing any of them. HTML bodies you build
> with `lagodev/view` drop straight into `Message.HTML`.

The whole surface of the core package:

```go
func NewMessage() *Builder
func (b *Builder) From(addr string) *Builder
func (b *Builder) To(addrs ...string) *Builder
func (b *Builder) Cc(addrs ...string) *Builder
func (b *Builder) Bcc(addrs ...string) *Builder
func (b *Builder) ReplyTo(addr string) *Builder
func (b *Builder) Subject(s string) *Builder
func (b *Builder) Text(s string) *Builder
func (b *Builder) HTML(s string) *Builder
func (b *Builder) Header(k, v string) *Builder
func (b *Builder) Attach(filename string, data []byte, contentType ...string) *Builder
func (b *Builder) Build() Message

type Mailer interface {
    Send(ctx context.Context, msg Message) error
}

func NewSMTPMailer(cfg SMTPConfig) *SMTPMailer
func NewLogMailer() *LogMailer

var ErrEmptyRecipients = errors.New("mail: at least one recipient required")
var ErrHeaderInjection = errors.New("mail: CR/LF in header field rejected")
```

## Quick start — compose & send

```go
package main

import (
    "context"
    "os"

    "github.com/devituz/lagodev/mail"
)

func main() {
    ctx := context.Background()

    m := mail.NewSMTPMailer(mail.SMTPConfig{
        Host:     "smtp.mailgun.org",
        Port:     587,
        Username: "postmaster@example.com",
        Password: os.Getenv("MAIL_PASSWORD"),
        From:     "noreply@example.com",
    })

    msg := mail.NewMessage().
        To("user@example.com").
        Subject("Welcome aboard").
        HTML("<h1>Hi!</h1><p>Thanks for signing up.</p>").
        Text("Hi! Thanks for signing up.").
        Build()

    if err := m.Send(ctx, msg); err != nil {
        panic(err)
    }
}
```

`NewMessage()` returns a `*Builder`; every setter returns the builder so calls
chain. `Build()` finalises the immutable `Message`. The `To`, `Cc`, and `Bcc`
setters are variadic and **accumulate** across calls — `To("a").To("b")` adds
both. If `Message.From` is empty when you send, the mailer falls back to the
`From` configured on the transport.

A `Message` with no `To`/`Cc`/`Bcc` recipient is rejected with
`ErrEmptyRecipients` at send time.

## The `Message` payload

```go
type Message struct {
    From        string
    To          []string
    Cc          []string
    Bcc         []string
    ReplyTo     string
    Subject     string
    Text        string
    HTML        string
    Headers     map[string]string
    Attachments []Attachment
}
```

`Message.Recipients()` returns the full envelope set (To + Cc + Bcc) — useful
when writing a custom driver. You normally never touch the struct directly:
build it with the fluent `Builder` and treat the result as read-only.

## Mailers / transports

Every transport implements the one-method `Mailer` interface, so swapping one
for another is a one-line change. Pick by environment, not by code:

| Transport                 | Constructor                  | Best for                                            |
|---------------------------|------------------------------|-----------------------------------------------------|
| `mail.SMTPMailer`         | `mail.NewSMTPMailer(cfg)`    | Any RFC SMTP server: Postfix, SES, Postmark, Mailtrap, MailHog |
| `mail.LogMailer`          | `mail.NewLogMailer()`        | Tests / local dev — captures messages in memory     |
| `sendgrid.Mailer`         | `sendgrid.New(cfg)`          | SendGrid v3 HTTP API                                 |
| `mailgun.Mailer`          | `mailgun.New(cfg)`           | Mailgun HTTP API (works where SMTP ports are blocked)|

### SMTP

```go
type SMTPConfig struct {
    Host        string // e.g. "smtp.mailgun.org"
    Port        int    // 25 / 465 / 587
    Username    string
    Password    string
    From        string // default From when Message.From is empty
    Insecure    bool   // disable STARTTLS — dev servers (MailHog) only
    TLSImplicit bool   // immediate TLS handshake (port 465 mode)
}
```

`NewSMTPMailer` uses `net/smtp` under the hood. When `Username` is non-empty it
authenticates with `PLAIN`. Leave `Insecure` and `TLSImplicit` at their
defaults for any real server; the driver uses STARTTLS when the server offers
it. Only set `Insecure: true` for an isolated local relay like MailHog.

```go
m := mail.NewSMTPMailer(mail.SMTPConfig{
    Host:     "localhost",
    Port:     1025,
    Insecure: true, // MailHog — no TLS
    From:     "dev@localhost",
})
```

### SendGrid (HTTP API)

```go
import "github.com/devituz/lagodev/mail/sendgrid"

m := sendgrid.New(sendgrid.Config{
    APIKey: os.Getenv("SENDGRID_API_KEY"),
    From:   "noreply@example.com",
})
err := m.Send(ctx, mail.NewMessage().
    To("user@example.com").
    Subject("Hi").
    HTML("<b>hello</b>").
    Build())
```

```go
type Config struct {
    APIKey     string
    From       string       // default From when Message.From is empty
    Endpoint   string       // override the API URL (default DefaultEndpoint)
    HTTPClient *http.Client  // default: 30s-timeout client
}
```

`sendgrid.DefaultEndpoint` is `https://api.sendgrid.com/v3/mail/send`. SendGrid
returns `202` on success; any other status surfaces the response body in the
returned error. An empty `APIKey` or `From` (with no default) is rejected
before the request is made.

### Mailgun (HTTP API)

```go
import "github.com/devituz/lagodev/mail/mailgun"

m := mailgun.New(mailgun.Config{
    Domain: "mg.example.com",
    APIKey: os.Getenv("MAILGUN_KEY"),
    From:   "noreply@example.com",
    Region: mailgun.RegionEU, // optional; defaults to RegionUS
})
err := m.Send(ctx, mail.NewMessage().
    To("user@example.com").
    Subject("Hi").
    HTML("<b>hello</b>").
    Build())
```

```go
type Config struct {
    Domain     string       // sending domain registered with Mailgun
    APIKey     string       // private API key
    From       string       // default From when Message.From is empty
    Region     Region       // RegionUS (default) or RegionEU
    HTTPClient *http.Client  // default: 30s-timeout client
}

const (
    RegionUS Region = "https://api.mailgun.net/v3"
    RegionEU Region = "https://api.eu.mailgun.net/v3"
)
```

The HTTP driver is the right choice on platforms that block outbound SMTP ports
25/465/587 (Heroku, App Engine, Cloud Run, locked-down corporate networks).
Non-2xx responses surface the Mailgun error body in the returned error.

Because every transport satisfies `mail.Mailer`, dependency-inject the
interface and choose the concrete type per environment:

```go
func newMailer() mail.Mailer {
    switch os.Getenv("MAIL_DRIVER") {
    case "sendgrid":
        return sendgrid.New(sendgrid.Config{APIKey: os.Getenv("SENDGRID_API_KEY"), From: "noreply@example.com"})
    case "mailgun":
        return mailgun.New(mailgun.Config{Domain: "mg.example.com", APIKey: os.Getenv("MAILGUN_KEY"), From: "noreply@example.com"})
    case "log":
        return mail.NewLogMailer()
    default:
        return mail.NewSMTPMailer(mail.SMTPConfig{Host: "localhost", Port: 1025, Insecure: true, From: "dev@localhost"})
    }
}
```

## HTML + text bodies

Set `HTML`, `Text`, or both. The SMTP driver picks the right MIME structure
automatically:

| Bodies set            | MIME structure produced              |
|-----------------------|--------------------------------------|
| `Text` only           | `text/plain`                         |
| `HTML` only           | `text/html`                          |
| both `Text` and `HTML`| `multipart/alternative`              |
| any attachment        | `multipart/mixed` wrapping the above |

Always send both when you can — `Text` is the fallback for clients that won't
render HTML, and including it improves deliverability.

```go
msg := mail.NewMessage().
    To("user@example.com").
    Subject("Receipt").
    Text("Your order #1234 shipped.").
    HTML("<p>Your order <b>#1234</b> shipped.</p>").
    Build()
```

### Rendering templates into the body

`mail` has no template engine of its own — render with `lagodev/view` (or the
stdlib) and assign the resulting string. `Engine.RenderToString` is built for
exactly this:

```go
import "github.com/devituz/lagodev/view"

eng, _ := view.New(tplFS, view.Options{Root: "templates"})

html, err := eng.RenderToString("mail/welcome", map[string]any{"Name": "Ada"})
if err != nil {
    return err
}

msg := mail.NewMessage().
    To("ada@example.com").
    Subject("Welcome, Ada").
    HTML(html).
    Build()
```

## Attachments, Cc / Bcc, custom headers

`Attach` appends a file. The content type is auto-detected from the filename
extension when you omit it (falling back to `application/octet-stream`):

```go
pdf, _ := os.ReadFile("invoice.pdf")
logo, _ := os.ReadFile("logo.png")

msg := mail.NewMessage().
    To("billing@example.com").
    Cc("manager@example.com").
    Bcc("audit@example.com").
    ReplyTo("support@example.com").
    Subject("Invoice #1234").
    HTML("<p>Invoice attached.</p>").
    Attach("invoice.pdf", pdf).                 // type auto-detected
    Attach("logo.png", logo, "image/png").      // explicit type
    Header("X-Campaign-ID", "spring-2026").
    Build()
```

`Cc` and `Bcc` go to every transport. Custom `Header` values map cleanly:
the SMTP driver writes them into the RFC 5322 header block, SendGrid sends
them in `headers`, and Mailgun prefixes each as an `h:`-field.

### Header-injection safety

The SMTP encoder validates every address and header value before emitting the
message. A CR/LF embedded in an address or header — the classic vector for
smuggling a hidden `Bcc` — is rejected with `ErrHeaderInjection`. Addresses are
parsed with `net/mail` and re-emitted in canonical form, so malformed input
fails loudly rather than producing a broken envelope.

## Queueing mail

Sending mail inline blocks the request on a network round-trip. In a web
handler, hand the work to `lagodev/queue` instead and return immediately.
Define a job that carries only what you need to rebuild the message, then send
from the handler:

```go
import (
    "context"
    "github.com/devituz/lagodev/mail"
    "github.com/devituz/lagodev/queue"
)

type SendWelcomeEmail struct {
    Email string
    Name  string
}

func registerMailHandlers(w *queue.Worker, m mail.Mailer) {
    queue.Handle[SendWelcomeEmail](w, func(ctx context.Context, j SendWelcomeEmail) error {
        return m.Send(ctx, mail.NewMessage().
            To(j.Email).
            Subject("Welcome, "+j.Name).
            HTML("<h1>Welcome, "+j.Name+"!</h1>").
            Build())
    })
}

// In the request handler — non-blocking:
_ = queue.Dispatch(ctx, q, SendWelcomeEmail{Email: "ada@example.com", Name: "Ada"})
```

The queue gives you at-least-once delivery and automatic retries on a transient
SMTP/API failure (the handler returns an error → the job is Nacked and
redelivered). Keep the handler **idempotent**: an email might be re-sent if the
process dies after delivery but before the Ack. See [QUEUE.md](QUEUE.md) for
delays, retry backoff, and durable drivers.

## Testing with `LogMailer`

`LogMailer` implements `Mailer` but keeps messages in memory instead of
sending them — ideal for assertions in tests and for local dev where you don't
want real mail going out:

```go
m := mail.NewLogMailer()
_ = m.Send(ctx, mail.NewMessage().To("a@b.com").Subject("Hi").Text("yo").Build())

m.Count()            // 1
m.Last().Subject     // "Hi"
m.Last().To          // []string{"a@b.com"}
m.Reset()            // clear captured history
```

It still enforces `ErrEmptyRecipients`, so a missing recipient is caught in
tests rather than in production.

## Production notes

- **Timeouts.** The HTTP drivers default to a 30s-timeout `http.Client`;
  override `Config.HTTPClient` to tune it. The SMTP driver honours
  `ctx` to return early on cancellation, **but** `net/smtp.SendMail` is not
  context-aware: the underlying delivery goroutine keeps running until the SMTP
  exchange finishes or its own network timeout fires. Bound it with a
  network-layer dial timeout, and always pass a `context.WithTimeout`.

- **Retries.** No transport retries internally. Run sends through
  `lagodev/queue` (above) so transient failures are retried with backoff, or
  wrap the `Mailer` with your own retry policy.

- **Credentials.** Read `APIKey` / `Password` from the environment, never hard-
  code them. `From` should be a verified/authenticated sender for the provider
  to avoid SPF/DKIM rejection.

- **Driver choice.** Prefer the HTTP drivers (SendGrid/Mailgun) on PaaS hosts
  that block outbound SMTP ports. SMTP is fine on a VPS where you control the
  network.

- **Deliverability.** Send both `Text` and `HTML`, set a real `From` on an
  authenticated domain (SPF + DKIM + DMARC), and use `ReplyTo` for the address
  you actually monitor.
