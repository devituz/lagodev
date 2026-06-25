package benchmarks

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/devituz/lagodev/web"
)

// staticApp builds a web.App with default middleware disabled so the
// benchmark isolates the router match + dispatch cost itself. A handful of
// routes (including a parameterised one) are registered to give the matcher a
// non-trivial table.
func staticApp() *web.App {
	app := web.New(web.WithoutDefaultMiddleware())
	app.Get("/health", func(c *web.Context) (any, error) { return map[string]string{"status": "ok"}, nil })
	app.Get("/users", func(c *web.Context) (any, error) { return nil, nil })
	app.Get("/users/{id}", func(c *web.Context) (any, error) {
		return map[string]string{"id": c.Param("id")}, nil
	})
	app.Post("/users", func(c *web.Context) (any, error) { return nil, nil })
	app.Get("/posts/{id}/comments", func(c *web.Context) (any, error) { return nil, nil })
	return app
}

// BenchmarkRouter_MatchStatic measures match + dispatch + JSON encode for a
// static route through the full web.App request pipeline.
func BenchmarkRouter_MatchStatic(b *testing.B) {
	app := staticApp()
	req := httptest.NewRequest("GET", "/health", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := app.Test(req)
		if rec.Code != 200 {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkRouter_MatchParam measures the same pipeline for a route with a
// path parameter, which forces parameter extraction.
func BenchmarkRouter_MatchParam(b *testing.B) {
	app := staticApp()
	req := httptest.NewRequest("GET", "/users/42", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := app.Test(req)
		if rec.Code != 200 {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

// BenchmarkMiddlewareChain measures the per-request cost of threading a
// request through a stack of middleware before reaching the handler.
func BenchmarkMiddlewareChain(b *testing.B) {
	app := web.New(web.WithoutDefaultMiddleware())

	// A cheap pass-through middleware repeated to model a realistic stack
	// depth without dragging in I/O-bound concerns (logging, rate limiting).
	passthrough := func(next web.Handler) web.Handler {
		return func(c *web.Context) (any, error) {
			return next(c)
		}
	}
	app.Use(passthrough, passthrough, passthrough, passthrough, passthrough)
	app.Get("/x", func(c *web.Context) (any, error) { return nil, nil })

	req := httptest.NewRequest("GET", "/x", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := app.Test(req)
		if rec.Code != 204 && rec.Code != 200 {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

type createUserReq struct {
	Name  string `json:"name" validate:"required,min=3,max=255"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age" validate:"min=18,max=120"`
	Role  string `json:"role" validate:"oneof=admin user guest"`
}

// BenchmarkBindAndValidate measures the JSON decode + struct validation path
// that every write endpoint runs, end-to-end through a real request.
func BenchmarkBindAndValidate(b *testing.B) {
	app := web.New(web.WithoutDefaultMiddleware())
	app.Post("/users", func(c *web.Context) (any, error) {
		var in createUserReq
		if err := c.Bind(&in); err != nil {
			return nil, err
		}
		if err := web.Validate(&in); err != nil {
			return nil, err
		}
		return in, nil
	})

	body := []byte(`{"name":"Ada Lovelace","email":"ada@example.com","age":36,"role":"admin"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/users", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := app.Test(req)
		if rec.Code != 200 {
			b.Fatalf("status %d body %s", rec.Code, rec.Body.String())
		}
	}
}
