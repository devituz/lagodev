# Chi example

REST API for `User` using [Chi v5](https://github.com/go-chi/chi) and lagodev.

```bash
cd examples/chi
go mod tidy
go run .
```

Chi is essentially `net/http` with idiomatic routing — the integration is
the most "Go-native". The same `UserService` powers Gin/Fiber/Echo with no
modification.
