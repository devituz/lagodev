package web

import (
	"bufio"
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// serveApp materialises the App's route table onto a live httptest server
// so tests can exercise the real ResponseWriter (flushing, header
// freezing) — behaviours that httptest.Recorder hides. Mirrors the wiring
// in App.Run / App.Test.
func serveApp(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, rt := range app.Routes() {
		method, path, h := rt.Method, rt.Path, rt.Handler
		mux.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			ctx := newContext(w, r, app.conn)
			_, _ = h(ctx)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Bug #1: SSE/streaming must flush through the DEFAULT middleware stack
// (App.New installs Logger, whose loggingWriter wraps c.Writer). Before
// the fix loggingWriter did not implement http.Flusher, so Flush() was a
// silent no-op and the first SSE frame stayed buffered until the handler
// returned. This test blocks the handler until it has confirmed receipt
// of the first frame, so it can only pass if Flush actually reaches the
// network.
func TestSSE_FlushesThroughDefaultMiddleware(t *testing.T) {
	released := make(chan struct{})
	app := New() // installs Logger + Recovery by default
	app.Get("/stream", func(c *Context) (any, error) {
		if err := c.SSE("tick", "first"); err != nil {
			return nil, err
		}
		// Block until the test confirms it received the frame. If Flush is
		// a no-op the client never sees the frame and this never unblocks.
		select {
		case <-released:
		case <-time.After(3 * time.Second):
		}
		return nil, nil
	})
	srv := serveApp(t, app)

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := br.ReadString('\n')
			b.WriteString(line)
			if strings.Contains(b.String(), "data: first\n") {
				got <- b.String()
				return
			}
			if err != nil {
				got <- b.String()
				return
			}
		}
	}()

	select {
	case frame := <-got:
		if !strings.Contains(frame, "event: tick") || !strings.Contains(frame, "data: first") {
			t.Fatalf("frame received but malformed: %q", frame)
		}
	case <-time.After(2 * time.Second):
		close(released)
		t.Fatal("first SSE frame was not flushed before the handler unblocked (Flush no-op through Logger)")
	}
	close(released)
}

// Bug #2: c.Status(N) followed by `return value, nil` must produce the
// correct Content-Type. Status() used to call WriteHeader eagerly, freezing
// headers before respond()->JSON() set Content-Type, so the client got
// text/plain. Must use a real server — httptest.Recorder lets late header
// writes through and hides the bug.
func TestStatusThenJSON_SetsContentType(t *testing.T) {
	app := New()
	app.Post("/posts", func(c *Context) (any, error) {
		c.Status(http.StatusCreated)
		return map[string]string{"id": "1"}, nil
	})
	srv := serveApp(t, app)

	resp, err := http.Post(srv.URL+"/posts", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// Bug #3: the access log must report the real status/size for the
// `return value, nil` pattern. Logger used to capture status/bytes before
// respond() wrote anything (respond ran outside the middleware chain), so
// it always logged status=200 size=0. With respond() now the innermost
// chain layer, Logger observes the committed status and bytes — including
// for error returns (500/404).
func TestLogger_RecordsRealStatusAndSize(t *testing.T) {
	var buf bytes.Buffer
	l := log.New(&buf, "", 0)

	app := New(WithoutDefaultMiddleware())
	app.Use(Logger(l))
	app.Get("/boom", func(c *Context) (any, error) {
		return nil, errFake // mapped to 500 by respond()->Error()
	})
	app.Get("/ok", func(c *Context) (any, error) {
		return map[string]string{"hello": "world"}, nil
	})

	rec := app.Test(httptest.NewRequest("GET", "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	logLine := buf.String()
	if !strings.Contains(logLine, " 500 ") {
		t.Fatalf("Logger should record real status 500, got: %q", logLine)
	}
	if strings.Contains(logLine, "size=0") {
		t.Fatalf("Logger should record the error body size, not 0: %q", logLine)
	}

	buf.Reset()
	rec = app.Test(httptest.NewRequest("GET", "/ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if logLine := buf.String(); strings.Contains(logLine, "size=0") {
		t.Fatalf("Logger should record the JSON body size, not 0: %q", logLine)
	}
}

// Bug #2 variant via Created(): Content-Type stays application/json.
func TestCreated_SetsJSONContentType(t *testing.T) {
	app := New()
	app.Post("/things", func(c *Context) (any, error) {
		return c.Created(map[string]string{"id": "7"}), nil
	})
	srv := serveApp(t, app)

	resp, err := http.Post(srv.URL+"/things", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

// SecurityHeaders: a custom config (overriding e.g. CSP) must NOT silently
// drop nosniff. Only DisableNoSniff: true removes it.
func TestSecurityHeaders_CustomConfigKeepsNoSniff(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req,
		func(c *Context) (any, error) { return map[string]string{"ok": "1"}, nil },
		SecurityHeaders(SecurityHeadersConfig{ContentSecurityPolicy: "default-src 'none'"}),
	)
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("custom config silently dropped nosniff: %q", got)
	}

	rec = runHandler(t, req,
		func(c *Context) (any, error) { return map[string]string{"ok": "1"}, nil },
		SecurityHeaders(SecurityHeadersConfig{DisableNoSniff: true}),
	)
	if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
		t.Fatalf("DisableNoSniff did not remove nosniff: %q", got)
	}
}
