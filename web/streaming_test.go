package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedirect(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		c.Redirect(http.StatusMovedPermanently, "/elsewhere")
		return nil, nil
	})
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/elsewhere" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestRedirect_NormalisesBadStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		c.Redirect(200, "/x") // 200 isn't a redirect — should clamp to 302
		return nil, nil
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
}

func TestFile_ServesAndSetsInlineDisposition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hi from file"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		c.File(path)
		return nil, nil
	})
	if rec.Body.String() != "hi from file" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if disp := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(disp, "inline;") {
		t.Fatalf("Content-Disposition = %q", disp)
	}
}

func TestAttachment_TriggersDownload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.csv")
	_ = os.WriteFile(path, []byte("id,name\n1,Ada\n"), 0o644)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		c.Attachment(path, "users.csv")
		return nil, nil
	})
	disp := rec.Header().Get("Content-Disposition")
	if !strings.Contains(disp, `attachment`) || !strings.Contains(disp, `users.csv`) {
		t.Fatalf("Content-Disposition = %q", disp)
	}
}

func TestStream_WritesBytesThroughWithContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		_, err := c.Stream(200, "text/plain", strings.NewReader("streamed payload"))
		return nil, err
	})
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "streamed payload" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestSSE_FrameFormat(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		_ = c.SSE("message", "hello\nworld")
		_ = c.SSE("", "just data")
		return nil, nil
	})
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: message\ndata: hello\ndata: world\n\n") {
		t.Fatalf("first frame badly formatted:\n%s", body)
	}
	if !strings.Contains(body, "data: just data\n\n") {
		t.Fatalf("event-less frame badly formatted:\n%s", body)
	}
}

func TestRecovery_DoesNotLeakPanicValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		panic("db password: hunter2")
	}, Recovery(testLogger()))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "hunter2") || strings.Contains(body, "password") {
		t.Fatalf("Recovery leaked panic value to client: %q", body)
	}
	if !strings.Contains(body, "internal server error") {
		t.Fatalf("expected generic error message, got %q", body)
	}
}

func TestHealth_Returns200(t *testing.T) {
	app := New()
	app.Health()
	rec := app.Test(httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestReady_AllPassReturns200(t *testing.T) {
	app := New()
	app.Ready(
		HealthCheck{Name: "db", Probe: func(context.Context) error { return nil }},
		HealthCheck{Name: "cache", Probe: func(context.Context) error { return nil }},
	)
	rec := app.Test(httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ready"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestReady_AnyFailReturns503(t *testing.T) {
	app := New()
	app.Ready(
		HealthCheck{Name: "db", Probe: func(context.Context) error { return errFake }},
	)
	rec := app.Test(httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"first_fail":"db"`) {
		t.Fatalf("body must name failing check: %s", rec.Body.String())
	}
}

var errFake = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "boom" }

func TestApp_Test_DrivesRequestsWithoutPort(t *testing.T) {
	app := New()
	app.Get("/echo/{name}", func(c *Context) (any, error) {
		return map[string]string{"name": c.Param("name")}, nil
	})
	rec := app.Test(httptest.NewRequest("GET", "/echo/ada", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ada"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
