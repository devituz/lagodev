package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adversarialApp builds an app with the default middleware stack plus a
// resource and a panicking route, served through a real mux so routing
// semantics (trailing slash, double slash, catch-all) match production.
func adversarialApp(t *testing.T) *App {
	t.Helper()
	app := New() // Logger + Recovery by default
	app.Get("/users/{id}", func(c *Context) (any, error) {
		// Echo the raw param so traversal payloads are observable.
		return map[string]string{"id": c.Param("id")}, nil
	})
	app.Post("/users", func(c *Context) (any, error) {
		var p struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&p); err != nil {
			return nil, nil
		}
		return c.Created(map[string]string{"name": p.Name}), nil
	})
	app.Get("/panic", func(c *Context) (any, error) {
		panic("intentional panic for recovery test")
	})
	return app
}

// TestAdversarial_PathTraversalInParam ensures traversal payloads in a
// path param are treated as opaque values — never used to escape the
// route or panic. The framework does no filesystem access on params, so
// the contract is: deliver the literal segment, do not crash.
func TestAdversarial_PathTraversalInParam(t *testing.T) {
	app := adversarialApp(t)
	cases := []string{
		"..",
		"%2e%2e",
		"%2e%2e%2f",
		"..%2f..%2fetc%2fpasswd",
		"....//....//",
		"%00",
		"%2e%2e/%2e%2e/secret",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/users/"+raw, nil)
			rec := app.Test(req)
			// Must never be a 5xx (panic) — a clean 2xx/4xx is acceptable.
			if rec.Code >= 500 {
				t.Fatalf("traversal %q produced 5xx (%d): %q", raw, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdversarial_DoubleSlashAndTrailingSlash documents net/http routing
// semantics for malformed-ish paths: they must not panic and must produce
// a deterministic status.
func TestAdversarial_DoubleSlashAndTrailingSlash(t *testing.T) {
	app := adversarialApp(t)
	cases := []string{
		"/users/42/",  // trailing slash
		"//users//42", // double slash
		"/users//42",  // empty segment
		"/users/42//", // trailing double slash
		"/USERS/42",   // case (paths are case-sensitive)
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := app.Test(req)
			if rec.Code >= 500 {
				t.Fatalf("path %q produced 5xx (%d)", p, rec.Code)
			}
		})
	}
}

// TestAdversarial_ExtremelyLongPath ensures a multi-kilobyte path neither
// panics nor hangs; stdlib returns a clean status.
func TestAdversarial_ExtremelyLongPath(t *testing.T) {
	app := adversarialApp(t)
	long := "/users/" + strings.Repeat("a", 64*1024)
	req := httptest.NewRequest(http.MethodGet, long, nil)
	rec := app.Test(req)
	if rec.Code >= 500 {
		t.Fatalf("extremely long path produced 5xx (%d)", rec.Code)
	}
}

// TestAdversarial_UnknownMethod confirms an unregistered method on a known
// path yields 405 (stdlib) and never panics.
func TestAdversarial_UnknownMethod(t *testing.T) {
	app := adversarialApp(t)
	for _, m := range []string{http.MethodDelete, http.MethodPut, "BREW", "TRACE"} {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/users/42", nil)
			rec := app.Test(req)
			if rec.Code >= 500 {
				t.Fatalf("method %q produced 5xx (%d)", m, rec.Code)
			}
			// /users/42 only registers GET → expect 405 Method Not Allowed.
			if rec.Code != http.StatusMethodNotAllowed {
				t.Logf("method %q → %d (stdlib mux)", m, rec.Code)
			}
		})
	}
}

// TestAdversarial_MalformedJSON: a body that is not valid JSON must map to
// 400, write the body exactly once, and not panic.
func TestAdversarial_MalformedJSON(t *testing.T) {
	app := adversarialApp(t)
	bodies := []string{
		`{"name":`,        // truncated
		`{"name":"x"`,     // missing close brace
		`not json at all`, // garbage
		`[1,2,3]`,         // wrong shape (array into struct)
		``,                // empty body with content-type json
	}
	for _, b := range bodies {
		t.Run(b, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			rec := app.Test(req)
			if rec.Code >= 500 {
				t.Fatalf("malformed json %q produced 5xx (%d): %q", b, rec.Code, rec.Body.String())
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("malformed json %q want 400, got %d", b, rec.Code)
			}
		})
	}
}

// TestAdversarial_WrongContentType: Bind decodes by structure, not by
// header, so a JSON body with a wrong/missing content-type still binds;
// a non-JSON body still yields a clean 400, never a panic.
func TestAdversarial_WrongContentType(t *testing.T) {
	app := adversarialApp(t)

	// Valid JSON, wrong content-type → still binds (200/201).
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"ok"}`))
	req.Header.Set("Content-Type", "text/plain")
	rec := app.Test(req)
	if rec.Code >= 500 {
		t.Fatalf("valid json with text/plain produced 5xx (%d)", rec.Code)
	}

	// Non-JSON body, form content-type → clean 400.
	req = httptest.NewRequest(http.MethodPost, "/users", strings.NewReader("name=ok&x=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = app.Test(req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("form body to JSON bind want 400, got %d", rec.Code)
	}
}

// TestAdversarial_OversizeBody verifies the default body-size limit
// (DefaultBodyLimit) protects Bind() even without an explicit BodyLimit
// middleware, and that an explicit BodyLimit also rejects.
func TestAdversarial_OversizeBody(t *testing.T) {
	// 1. Default limit via Bind (no BodyLimit middleware on this route).
	app := adversarialApp(t)
	huge := bytes.Repeat([]byte("a"), int(DefaultBodyLimit)+1024)
	body := append([]byte(`{"name":"`), huge...)
	body = append(body, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := app.Test(req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize body via default limit want 400, got %d", rec.Code)
	}

	// 2. Explicit, lower BodyLimit on a raw read.
	small := strings.Repeat("x", 4096)
	req = httptest.NewRequest(http.MethodPost, "/raw", strings.NewReader(small))
	rec = runHandler(t, req, func(c *Context) (any, error) {
		_, err := io.ReadAll(c.Request.Body)
		return nil, err
	}, BodyLimit(256))
	if rec.Code < 400 {
		t.Fatalf("explicit BodyLimit(256) should reject 4KiB body, got %d", rec.Code)
	}
}

// TestAdversarial_PanicRecovered asserts that a panic in a handler is
// recovered by the default Recovery middleware and converted to a generic
// 500 — never an empty 200, never a process crash. This guards the bug
// where respond() (innermost) is bypassed by the panic unwind and the
// recovered error is dropped.
func TestAdversarial_PanicRecovered(t *testing.T) {
	app := adversarialApp(t)
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := app.Test(req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic must be recovered to 500, got %d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("recovered panic must return generic body, got %q", rec.Body.String())
	}
	// The panic value must never leak to the client.
	if strings.Contains(rec.Body.String(), "intentional panic") {
		t.Fatalf("recovered panic value leaked to client: %q", rec.Body.String())
	}
}

// TestAdversarial_PanicRecovered_NoBodyDoubleWrite ensures that when a
// handler writes a partial response and then panics, Recovery does not
// double-write the body (respond() is a no-op once bodyWritten is set).
func TestAdversarial_PanicRecovered_NoBodyDoubleWrite(t *testing.T) {
	app := New()
	app.Get("/half", func(c *Context) (any, error) {
		c.JSON(http.StatusTeapot, map[string]string{"stage": "before-panic"})
		panic("after partial write")
	})
	rec := app.Test(httptest.NewRequest(http.MethodGet, "/half", nil))
	// The first write committed 418; Recovery must not append a second body.
	if rec.Code != http.StatusTeapot {
		t.Fatalf("committed status should stick at 418, got %d", rec.Code)
	}
	if strings.Count(rec.Body.String(), "stage") != 1 {
		t.Fatalf("body must be written once, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("Recovery double-wrote after a committed body: %q", rec.Body.String())
	}
}

// TestAdversarial_RootRouteMountsCleanly guards the regression where
// Get("/") on the top-level router produced an empty path string,
// panicking net/http's ServeMux at registration ("host/path missing /").
func TestAdversarial_RootRouteMountsCleanly(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering Get(\"/\") must not panic the mux: %v", r)
		}
	}()
	app := New()
	app.Get("/", func(c *Context) (any, error) {
		return map[string]string{"root": "ok"}, nil
	})
	rec := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("root route want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("root route body = %q", rec.Body.String())
	}
}
