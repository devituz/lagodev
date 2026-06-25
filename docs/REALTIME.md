# Realtime gateway

`realtime` is a high-level WebSocket-style gateway modelled on Laravel Echo /
NestJS gateways: channels, presence rosters, and per-channel authorization on
top of a transport-agnostic connection. `broadcasting` is the companion pub/sub
layer that fans events out across subscribers (and, with a remote driver, across
processes).

The two packages are deliberately decoupled. `realtime` imports only the
standard library — it never imports `broadcasting`, avoiding a dependency cycle
and keeping it a leaf. You wire them together by handing the Hub's `Broadcast`
method to a broadcasting subscription (see [Broadcasting events](#broadcasting-events)).

```
import (
    "github.com/devituz/lagodev/realtime"
    "github.com/devituz/lagodev/broadcasting"
)
```

## Layers

| Type         | Role                                                             |
|--------------|-----------------------------------------------------------------|
| `Conn`       | Transport abstraction: `ReadMessage` / `WriteMessage` / `Close` |
| `Client`     | A `Conn` registered on a Hub, with a bounded outbox             |
| `Hub`        | Per-app gateway: clients, channels, broadcast, presence        |
| `Authorizer` | Hook deciding who may join a private / presence channel        |

The Hub is written against the `Conn` interface, so it is fully testable
without real sockets — the package tests and the snippets below use an
in-memory `Conn`.

## Quick start — Hub + serve a connection

A server does four things per connection: register the `Conn` on the Hub,
start a read loop, subscribe to channels, and broadcast.

```go
package main

import (
    "fmt"

    "github.com/devituz/lagodev/realtime"
)

func main() {
    hub := realtime.NewHub()
    defer hub.Close()

    // conn is your transport adapter (see "Providing a transport" below).
    var conn realtime.Conn = newConn()

    // Add registers the conn and starts its writer goroutine. The second
    // arg is opaque client credentials forwarded to the Authorizer on
    // private/presence joins; pass nil for none.
    client, err := hub.Add(conn, nil)
    if err != nil {
        return // ErrClosed: the Hub is shut down
    }

    // Subscribe to a public channel — no authorization required.
    if err := hub.Subscribe(client, "orders"); err != nil {
        fmt.Println("subscribe:", err)
    }

    // Deliver a text frame to every subscriber of "orders".
    _ = hub.Broadcast("orders", []byte(`{"event":"created","id":42}`))

    // Run the inbound read loop. Serve blocks until the peer closes, then
    // tears the client down (firing presence-leave events). Run it in a
    // goroutine per connection in a real server.
    client.Serve(func(c *realtime.Client, typ realtime.MessageType, data []byte) {
        // handle inbound frame from c
    })
}
```

`MessageType` is either `realtime.TextMessage` (UTF-8) or
`realtime.BinaryMessage` (opaque bytes), matching WebSocket opcodes `0x1` /
`0x2`.

### Providing a transport

`realtime` defines the `Conn` interface but the Hub never touches a socket
directly — you supply the transport. A single reader goroutine and a single
writer goroutine touch a `Conn`, so `ReadMessage` need not be safe against
itself, nor `WriteMessage` against itself; only `Close` must be safe to call
concurrently with both.

```go
type Conn interface {
    ReadMessage() (MessageType, []byte, error)
    WriteMessage(MessageType, []byte) error
    Close() error
}
```

`ReadMessage` returns `io.EOF` (or a wrapped transport error) once the peer
closes. A minimal adapter wrapping any framed transport:

```go
type myConn struct {
    sock *someWebSocket // your library's connection
}

func (c *myConn) ReadMessage() (realtime.MessageType, []byte, error) {
    opcode, data, err := c.sock.Read()
    if err != nil {
        return 0, nil, err // io.EOF on a clean close
    }
    if opcode == opBinary {
        return realtime.BinaryMessage, data, nil
    }
    return realtime.TextMessage, data, nil
}

func (c *myConn) WriteMessage(t realtime.MessageType, data []byte) error {
    return c.sock.Write(opcodeFor(t), data)
}

func (c *myConn) Close() error { return c.sock.Close() }
```

## Channels & subscriptions

A channel is a named room. Its type is derived from the name prefix
(`ClassifyChannel`), mirroring Laravel Echo:

| Prefix       | `ChannelType` | Authorization      | Presence roster |
|--------------|---------------|--------------------|-----------------|
| *(none)*     | `Public`      | none               | no              |
| `private-`   | `Private`     | Authorizer         | no              |
| `presence-`  | `Presence`    | Authorizer         | yes             |

```go
realtime.ClassifyChannel("orders")          // Public
realtime.ClassifyChannel("private-billing")  // Private
realtime.ClassifyChannel("presence-room.7")  // Presence
```

Subscribe / unsubscribe are idempotent:

```go
err := hub.Subscribe(client, "private-billing")
// ErrUnauthorized if the Authorizer denies (or none is configured)

hub.Unsubscribe(client, "private-billing")
```

Channels are created lazily on first subscribe and destroyed when their last
member leaves. Disconnecting a client (via `Client.Close`, a write error, or
`Hub.Close`) removes it from every channel automatically — no subscription
leaks, and presence-leave events fire for each joined presence channel.

### Inspecting state

```go
hub.Count()                    // live clients
hub.ChannelCount("orders")     // subscribers on a channel
client.ID()                    // Hub-assigned id, e.g. "client-3"
```

## Presence

Presence channels (`presence-*`) maintain a **roster**: a list of `Member`
identities visible to every subscriber. A `Member` is one participant's public
identity:

```go
type Member struct {
    ID   string // stable per user
    Info []byte // opaque payload (typically JSON) shown to other members
}
```

The roster identity comes from the `Authorizer`'s `AuthResult.Member` (see
[Auth](#auth)). When a client joins a presence channel the Hub:

1. sends the **current roster** to the newcomer only, and
2. sends a **join** frame to every member (including the newcomer).

On leave (explicit or via disconnect) every remaining member receives a
**leave** frame. The wire frames are JSON, Echo-compatible:

| Lifecycle      | `event` field                       |
|----------------|-------------------------------------|
| Initial roster | `presence:subscription_succeeded`   |
| Member joins   | `presence:member_added`             |
| Member leaves  | `presence:member_removed`           |

```json
{"event":"presence:member_added","channel":"presence-room.7","member":{"id":"u42","info":{"name":"Ada"}}}
```

`Info` is passed through verbatim when it is already valid JSON, otherwise
encoded as a JSON string.

### Server-side roster & hook

Read the roster at any time (returns a copy; `nil` for unknown or
non-presence channels):

```go
members := hub.Presence("presence-room.7")
for _, m := range members {
    fmt.Println(m.ID, string(m.Info))
}
```

Register a server-side hook for every join/leave — useful for metrics,
audit logs, or mirroring presence to another store. It runs **synchronously**
on the goroutine driving the roster change, so it must not block:

```go
hub.OnPresence(func(e realtime.PresenceEvent) {
    if e.Joined {
        log.Printf("%s joined %s", e.Member.ID, e.Channel)
    } else {
        log.Printf("%s left %s", e.Member.ID, e.Channel)
    }
})
```

`PresenceEvent` carries `Channel`, `Joined bool`, and `Member`.

## Broadcasting events

The Hub exposes four delivery methods. All are non-blocking per-client
(subject to the [slow-consumer policy](#slow-consumer--backpressure)):

```go
hub.Broadcast("orders", payload)              // text frame to all subscribers
hub.BroadcastBinary("orders", payload)        // binary frame to all subscribers
hub.BroadcastExcept("chat.7", payload, sender) // skip one client (Echo "toOthers")
hub.SendTo(clientID, payload)                  // direct send to one client by id
```

`Broadcast` returns `nil` for an unknown channel (a no-op), and `ErrClosed`
after `Hub.Close`.

### Wiring `broadcasting` → `realtime`

`broadcasting` is a standalone pub/sub layer. `Event` is the unit dispatched
to subscribers:

```go
type Event struct {
    Channel string
    Name    string
    Payload []byte // opaque; typically pre-encoded JSON
}
```

The in-box `Memory` driver runs each subscription on its own goroutine reading
from a buffered channel. Bridge it to the Hub by subscribing a handler that
calls `Hub.Broadcast` — `Hub.Broadcast` has the right shape to forward frames
to subscribed clients:

```go
bcast := broadcasting.NewMemory()
defer bcast.Close()

// Every event published on "orders" is fanned out to the Hub's
// "orders" channel subscribers.
_, _ = bcast.Subscribe(ctx, "orders", func(ctx context.Context, e broadcasting.Event) error {
    return hub.Broadcast(e.Channel, e.Payload)
})

// Anywhere in your app (e.g. after persisting an order):
_ = bcast.Publish(ctx, broadcasting.Event{
    Channel: "orders",
    Name:    "OrderCreated",
    Payload: []byte(`{"id":42,"total":1999}`),
})
```

This indirection lets you swap `Memory` for a Redis/NATS/Kafka driver later
(fan-out across processes) without touching the Hub or your handlers.

### Memory driver tuning

```go
bcast := broadcasting.NewMemory(
    broadcasting.WithBuffer(1024),                       // per-subscription queue (default 256)
    broadcasting.WithLogger(func(msg string, a ...any) { // called on drops / handler errors
        log.Printf(msg, a...)
    }),
)
```

`Publish` is non-blocking: if a subscriber's queue is full the event is
dropped for that subscriber and a global counter is incremented. Watch it in
production to detect under-sized buffers:

```go
if d := bcast.Dropped(); d > 0 {
    metrics.Gauge("broadcasting.dropped", float64(d))
}
```

A `Subscription` is cancelled with `Cancel()`; `Close()` cancels all of them.
Handler errors are routed to the logger — they do **not** propagate back to
the publisher.

## Slow-consumer / backpressure

Each `Client` owns a bounded outbox (default 64 frames) drained by a single
writer goroutine. Broadcasting never blocks on a slow client — a stalled
consumer must not stall the fan-out. When a client's outbox is **full** at
enqueue time the Hub applies its `SlowConsumerPolicy`:

| Policy             | Behaviour on overflow                                        |
|--------------------|--------------------------------------------------------------|
| `DropMessage`      | Discard the frame, bump the client's drop counter. **Default.** Connection survives. Good for lossy telemetry. |
| `DisconnectClient` | Close the connection on the first overflow. Good for ordered, can't-miss streams where a gap is worse than a drop. |

```go
hub := realtime.NewHub(
    realtime.WithOutbox(256),                                  // bigger buffer absorbs bursts
    realtime.WithSlowConsumerPolicy(realtime.DisconnectClient),
)
```

Inspect drops per client under the `DropMessage` policy:

```go
if n := client.Drops(); n > 0 {
    log.Printf("client %s dropped %d frames", client.ID(), n)
}
```

Sizing guidance: a small outbox + `DropMessage` favours latency and frees a
slow client from holding memory; a large outbox + `DisconnectClient` favours
delivery integrity at the cost of evicting peers that can't keep up.

## Auth

Public channels need no authorization. **Private and presence** joins consult
the Hub's `Authorizer`. A Hub with **no** authorizer denies every
private/presence join (returns `ErrUnauthorized`).

```go
type Authorizer interface {
    Authorize(AuthRequest) AuthResult
}
```

The request carries everything needed to decide:

```go
type AuthRequest struct {
    Channel  string             // target channel
    Type     realtime.ChannelType
    ClientID string             // Hub-assigned id
    Auth     []byte             // credentials passed to hub.Add(conn, auth)
}

type AuthResult struct {
    Allowed bool
    Member  realtime.Member    // identity published to the presence roster
}
```

Install one with `WithAuthorizer`. Use `AuthorizerFunc` to adapt a plain
function:

```go
hub := realtime.NewHub(
    realtime.WithAuthorizer(realtime.AuthorizerFunc(func(r realtime.AuthRequest) realtime.AuthResult {
        userID, ok := verifyToken(r.Auth) // r.Auth came from hub.Add(conn, token)
        if !ok {
            return realtime.AuthResult{Allowed: false}
        }
        // Scope channel access however you like.
        if r.Channel == "private-billing" && !isBillingAdmin(userID) {
            return realtime.AuthResult{Allowed: false}
        }
        return realtime.AuthResult{
            Allowed: true,
            Member: realtime.Member{
                ID:   userID,
                Info: []byte(`{"name":"Ada"}`), // shown to other presence members
            },
        }
    })),
)
```

The `Auth` bytes are whatever you pass as the second argument to
`hub.Add(conn, auth)` — typically a signed token you extracted from the
connection's query string or headers at upgrade time. They are opaque to the
Hub. If `AuthResult.Member.ID` is empty, the Hub falls back to the
Hub-assigned client id.

The authorizer runs **outside** the Hub's lock (it is user code and may be
slow — a DB lookup, a token verification), so a slow `Authorize` does not
serialize other subscribers.

## Production notes

### Goroutine lifecycle

Per connection there are **two** goroutines:

- **Writer** — started by `hub.Add`, drains the outbox onto the `Conn`. A
  write error closes the client.
- **Reader** — `client.Serve(onMessage)`, which **you** start (run it in a
  goroutine per connection). It blocks until the peer closes, then always
  tears the client down on return.

`Client.Close` is idempotent and safe to call concurrently; it closes the
transport, removes the client from every channel (firing presence-leave
events), and stops the writer. `Hub.Close` closes every client and rejects
further `Add` calls (`ErrClosed`). Always `defer hub.Close()` on the owning
goroutine so shutdown is clean.

`onMessage` and the `OnPresence` hook both run on Hub-internal goroutines and
**must not block** — offload slow work (DB writes, external calls) to your own
worker pool or to `broadcasting`.

### Scaling

A `Hub` is per-process and per-app: it tracks only the clients connected to
**this** instance. To scale horizontally across multiple instances behind a
load balancer:

- Run one `Hub` per process.
- Use a **remote** `broadcasting` driver (Redis/NATS/Kafka in a sub-package)
  instead of `Memory`. Each instance subscribes to the relevant channels and
  forwards events to its local Hub via `hub.Broadcast`. A `Publish` on any
  instance then reaches clients on every instance.
- Keep presence rosters local unless you need a global roster; for a global
  view, mirror `OnPresence` events into a shared store (e.g. Redis) keyed by
  channel.

Because the `realtime` ↔ `broadcasting` bridge is just a handler that calls
`hub.Broadcast`, swapping the in-process `Memory` driver for a cross-process
one requires no change to your channel, presence, or auth code.

## See also

- **[GETTING_STARTED.md](GETTING_STARTED.md)** — bootstrapping a lagodev app.
- **[ORM.md](ORM.md)** — persisting the domain objects you broadcast.
