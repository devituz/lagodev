// Example: secure-by-default lagodev HTTP service.
//
// Run with `go run ./examples/secure` and try the endpoints:
//
//	# 1. Health — passes through the security stack
//	curl -i http://localhost:8080/ping
//
//	# 2. Validation failure → 422 with {"errors":{...}}
//	curl -i -X POST http://localhost:8080/users \
//	     -H 'Content-Type: application/json' \
//	     -d '{"email":"not-an-email","name":""}'
//
//	# 3. Unknown field is rejected → 400
//	curl -i -X POST http://localhost:8080/users \
//	     -H 'Content-Type: application/json' \
//	     -d '{"name":"A","email":"a@b.co","ghost":1}'
//
//	# 4. Rate limit (3 req/10s) → 429 on the 4th
//	for i in 1 2 3 4; do curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/ping; done
//
// The middleware stack mirrors the snippet in SECURITY.md.
package main

import (
	"time"

	"github.com/devituz/lagodev/web"
)

// CreateUser is the JSON shape we expect on POST /users. Struct tags
// drive both decoding and validation.
type CreateUser struct {
	Name  string `json:"name"  validate:"required,min=2,max=64"`
	Email string `json:"email" validate:"required,email"`
}

func main() {
	app := web.New(web.WithAddr(":8090"))

	app.Use(
		web.RequestID(),
		web.SecurityHeaders(),
		web.BodyLimit(1<<20),                // 1 MiB
		web.RateLimit(3, 10*time.Second),    // 3 req / 10s per IP
		web.CORS("https://app.example.com"), // strict allow-list
	)

	app.Get("/ping", func(c *web.Context) (any, error) {
		return map[string]string{"status": "ok"}, nil
	})

	app.Post("/users", func(c *web.Context) (any, error) {
		var in CreateUser
		if err := c.BindAndValidate(&in); err != nil {
			return nil, err // → 422 with field errors
		}
		// Pretend we persisted it.
		return c.Created(map[string]any{
			"name":  in.Name,
			"email": in.Email,
		}), nil
	})

	app.MustRun()
}
