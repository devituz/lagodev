# Events & Notifications

Two small, independent packages modelled on Laravel's `Event` and
`Notification` facades:

- **`events`** — an in-process, type-safe event dispatcher. Fire a value,
  every registered listener for that type runs.
- **`notifications`** — multi-channel delivery (mail, sms, slack, …) of a
  message to a recipient, routed per channel.

Neither package touches the database or the HTTP layer; both are plain Go you
can wire into a service, a model hook, or `main.go`.

## Overview

```
event value ──► Dispatcher ──► listener fn(ctx, e) error   (events)
Notifiable  ──► Manager    ──► Channel.Send(ctx, addr, n)  (notifications)
```

Events are **synchronous and in-process**: listeners run in registration order
on the calling goroutine. Notifications fan a single message out to several
transports. To run either off the request goroutine, hand the work to the
[`queue`](#integration-with-the-queue) package — neither dispatches
asynchronously on its own.

---

# Events

```go
import "github.com/devituz/lagodev/events"
```

## The dispatcher

`events.New()` returns a `*Dispatcher`. Keep one per application (store it on
your service container or pass it around); it is safe for concurrent use.

```go
d := events.New()
```

By default every listener runs even if an earlier one errors, and the errors
are joined together. Flip to short-circuit with `StopOnError` — it returns the
dispatcher so you can chain:

```go
d := events.New().StopOnError(true) // stop at the first listener error
```

## Events are plain values

An event is any value, typically a struct:

```go
type UserRegistered struct {
    ID    uint64
    Email string
}

type OrderPlaced struct {
    OrderID uint64
}
```

The dispatcher routes by the event's concrete type — `UserRegistered` listeners
never see an `OrderPlaced`.

## Subscribing — `events.Listen`

`Listen` is a generic free function (not a method) so listeners receive the
typed event with no manual assertion:

```go
events.Listen(d, func(ctx context.Context, e UserRegistered) error {
    return sendWelcomeEmail(ctx, e.Email)
})
```

Register as many listeners for a type as you like; they fire in registration
order.

```go
events.Listen(d, func(ctx context.Context, e UserRegistered) error {
    log.Printf("user %d registered", e.ID)
    return nil
})
events.Listen(d, func(ctx context.Context, e UserRegistered) error {
    return analytics.Track(ctx, "signup", e.ID)
})
```

### Inspecting & removing listeners

```go
// True once at least one listener exists for the type.
if events.HasListeners[UserRegistered](d) { /* ... */ }

// Remove every listener for a type; returns true if anything was removed.
events.Forget[UserRegistered](d)
```

`HasListeners` and `Forget` are generic over the event type, just like
`Listen`.

## Dispatching — `Dispatch`

```go
err := d.Dispatch(ctx, UserRegistered{ID: 42, Email: "ada@example.com"})
```

`Dispatch` runs every listener registered for the event's type and returns the
**joined** listener errors (or `nil`). With no listeners it is a cheap no-op
returning `nil`.

A pointer is normalized to its element value, so `Dispatch(&UserRegistered{…})`
reaches a listener registered via `Listen[UserRegistered]`:

```go
ev := &UserRegistered{ID: 42, Email: "ada@example.com"}
_ = d.Dispatch(ctx, ev) // matches Listen[UserRegistered] listeners
```

### Error handling

Listener errors are collected with `errors.Join`, so `errors.Is` works against
each one:

```go
err := d.Dispatch(ctx, UserRegistered{ID: 42})
if errors.Is(err, ErrMailDown) {
    // at least one listener failed with ErrMailDown
}
```

A **panicking** listener does not abort the fan-out: the panic is recovered and
surfaced as an error (`events: listener panic: …`), and the remaining listeners
still run — unless `StopOnError(true)` is set, in which case the panic
short-circuits like any other error.

### Synchronous vs. queued

`Dispatch` is always synchronous — it blocks until every listener returns. For
fire-and-forget work, dispatch a queue job from inside the listener instead of
doing the work inline:

```go
events.Listen(d, func(ctx context.Context, e UserRegistered) error {
    // Don't send the email here on the request goroutine —
    // enqueue it and return immediately.
    return queue.Dispatch(ctx, q, SendWelcomeEmail{UserID: e.ID})
})
```

### Firing events from a model hook

The dispatcher pairs naturally with ORM lifecycle hooks (see
[`ORM.md`](ORM.md)):

```go
func (u *User) AfterCreate(hc *orm.HookContext) error {
    return app.Events().Dispatch(hc.Ctx, UserRegistered{ID: u.ID, Email: u.Email})
}
```

---

# Notifications

```go
import "github.com/devituz/lagodev/notifications"
```

Notifications answer a different question: "send *this message* to *this
recipient* across *these channels*." Where an event has many independent
listeners, a notification has one payload routed to several transports.

## The three interfaces

| Interface      | Implemented by    | Responsibility                               |
|----------------|-------------------|----------------------------------------------|
| `Notification` | the message       | which channels to use (`Channels() []string`)|
| `Notifiable`   | the recipient     | per-channel address (`RouteFor(ch) string`)  |
| `Channel`      | the transport     | `Name()` + `Send(ctx, addr, n)`              |

### `Notification` — the message

```go
type Notification interface {
    Channels() []string
}
```

Channel names are arbitrary strings (`"mail"`, `"sms"`, `"slack"`,
`"database"`, …). The `Manager` routes by name; only registered channels
receive a delivery attempt.

A notification destined for the mail channel must additionally implement
`MailNotification`, which builds the `mail.Message`:

```go
type MailNotification interface {
    ToMail(recipient string) mail.Message
}
```

```go
import "github.com/devituz/lagodev/mail"

type WelcomeNotification struct {
    Name string
}

func (WelcomeNotification) Channels() []string { return []string{"mail"} }

func (n WelcomeNotification) ToMail(to string) mail.Message {
    return mail.NewMessage().
        From("noreply@example.com").
        To(to).
        Subject("Welcome to the app").
        HTML("<h1>Hi " + n.Name + "!</h1>").
        Build()
}
```

### `Notifiable` — the recipient

The recipient knows its own address per channel. Returning `""` skips that
channel for this user.

```go
type Notifiable interface {
    RouteFor(channel string) string
}
```

```go
type User struct {
    Email string
    Phone string
}

func (u User) RouteFor(channel string) string {
    switch channel {
    case "mail":
        return u.Email
    case "sms":
        return u.Phone
    }
    return "" // unknown channel → skipped
}
```

### `Channel` — the transport

```go
type Channel interface {
    Name() string
    Send(ctx context.Context, recipient string, n Notification) error
}
```

## Channels in-box

Only one channel ships: **`MailChannel`**, which adapts a `mail.Mailer` (see
[`MAIL.md`](MAIL.md)). Everything else — SMS, Slack, push, database — is a
custom channel you write by implementing `Channel`.

```go
import "github.com/devituz/lagodev/mail"

// Mailer can be the SMTP mailer in production, or the LogMailer in tests.
mailer := mail.NewSMTPMailer(mail.SMTPConfig{
    Host: "smtp.mailgun.org", Port: 587,
    Username: "postmaster@example.com",
    Password: os.Getenv("MAIL_PASSWORD"),
    From:     "noreply@example.com",
})

mailCh := notifications.NewMailChannel(mailer)
```

`MailChannel.Send` type-asserts the notification to `MailNotification`; a
notification that lists `"mail"` but does not implement `ToMail` fails that
channel with a clear error.

## The Manager

`NewManager()` returns an empty `Manager`. Register channels by name:

```go
mgr := notifications.NewManager()
mgr.Register(notifications.NewMailChannel(mailer))

// Or several at once:
mgr.RegisterMany(
    notifications.NewMailChannel(mailer),
    NewSMSChannel(twilio),
)
```

`Register` keys by `Channel.Name()`; re-registering the same name **replaces**
the previous channel. Look one up (nil if missing) with `mgr.Channel("mail")`.

## Sending

```go
user := User{Email: "ada@example.com", Phone: "+15551234"}

err := mgr.Send(ctx, user, WelcomeNotification{Name: "Ada"})
```

`Send` walks `n.Channels()` and, for each one:

1. Skips it silently if no channel is registered under that name.
2. Asks `to.RouteFor(name)` for the address; skips silently if it is `""`.
3. Calls the channel's `Send`, wrapping any failure as
   `notifications: <channel>: <err>`.

Per-channel errors are collected with `errors.Join` and returned together — a
failing SMS gateway does **not** stop the email from going out:

```go
err := mgr.Send(ctx, user, AlertNotification{}) // Channels() == ["mail", "sms"]
if err != nil {
    // mail may have succeeded even though sms failed; inspect with errors.Is
}
```

`SendNow` is an alias for `Send`, kept for parity with Laravel terminology
(where `SendNow` bypasses the queue). From this package's perspective every
dispatch is already synchronous.

## A custom channel

Implement `Channel` to add a transport. Define a per-channel contract (mirroring
`MailNotification`) so the notification supplies the right payload:

```go
// SMSNotification is the contract a Notification implements to be SMS-routable.
type SMSNotification interface {
    ToSMS(recipient string) string
}

type SMSChannel struct {
    client *twilio.Client
}

func (c *SMSChannel) Name() string { return "sms" }

func (c *SMSChannel) Send(ctx context.Context, recipient string, n notifications.Notification) error {
    sn, ok := n.(SMSNotification)
    if !ok {
        return errors.New("notification does not implement SMSNotification")
    }
    return c.client.SendText(ctx, recipient, sn.ToSMS(recipient))
}
```

A **database** channel follows the same shape: define a `DatabaseNotification`
interface returning a row struct, then `orm.Save` it inside `Send`.

---

## Integration with the queue

Both packages are synchronous. To move delivery off the request goroutine, hand
the work to [`queue`](QUEUE.md). Define a job, register a handler that performs
the actual send, and dispatch the job instead of sending inline:

```go
import "github.com/devituz/lagodev/queue"

type SendWelcome struct {
    Email string
    Name  string
}

// Wire once at boot:
func registerHandlers(w *queue.Worker, mgr *notifications.Manager) {
    queue.Handle[SendWelcome](w, func(ctx context.Context, j SendWelcome) error {
        return mgr.Send(ctx, User{Email: j.Email}, WelcomeNotification{Name: j.Name})
    })
}

// At call site — returns immediately, worker delivers later:
_ = queue.Dispatch(ctx, q, SendWelcome{Email: u.Email, Name: u.Name})
```

The same pattern queues an event listener's work: the listener enqueues a job
and returns, keeping `Dispatch` fast. Use `queue.DispatchAfter` to delay
delivery (e.g. a "your trial ends soon" reminder).

---

## Testing

Both packages are trivial to test without external services.

Events need no doubles — register a listener that records what it saw:

```go
d := events.New()
var got UserRegistered
events.Listen(d, func(_ context.Context, e UserRegistered) error {
    got = e
    return nil
})
_ = d.Dispatch(context.Background(), UserRegistered{ID: 7, Email: "a@b.co"})
// assert got == UserRegistered{ID: 7, Email: "a@b.co"}
```

Notifications use `mail.NewLogMailer()`, which captures messages in memory
instead of sending them:

```go
log := mail.NewLogMailer()
mgr := notifications.NewManager()
mgr.Register(notifications.NewMailChannel(log))

_ = mgr.Send(context.Background(), User{Email: "a@x"}, WelcomeNotification{Name: "Ada"})

// log.Count() == 1
// log.Last().Subject == "Welcome to the app"
```

---

## Production notes

- **Keep listeners and `ToMail`/`ToSMS` cheap.** Anything slow (HTTP, SMTP, DB)
  should be queued, not run inline on the dispatching goroutine — `Dispatch`
  and `Send` block until every callback returns.
- **Errors are joined, not the first.** Always inspect the full result with
  `errors.Is`; with notifications a non-nil error can still mean *some* channels
  delivered. Use `events.StopOnError(true)` only when later listeners genuinely
  depend on earlier ones succeeding.
- **Unregistered channels are silent.** A notification declaring `"sms"` in an
  environment without an SMS channel is skipped without error — convenient for
  stripped-down test/staging setups, but verify your channels are actually
  registered in production so deliveries are not lost quietly.
- **One dispatcher / one manager per process.** Both are safe for concurrent
  use; register all listeners and channels at boot, then share the instance.
- **Panics in listeners are contained,** but a recovered panic still counts as a
  failed listener — monitor the joined error so a silently panicking listener
  does not go unnoticed.
