# Fiber example

REST API for `User` using [Fiber v2](https://gofiber.io/) and lagodev.

```bash
cd examples/fiber
go mod tidy
go run .
```

The `UserService` returns plain Go values + errors — the only Fiber-specific
code is the 5 `app.Get/Post/Delete` handlers that adapt the service to
Fiber's `*fiber.Ctx` signature.
