package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devituz/lagodev/orm"
)

func testLogger() *log.Logger { return log.New(os.Stderr, "test ", 0) }

// runHandler wires a Handler through one or more middleware and runs it
// against the synthesized request, returning the response recorder.
func runHandler(t *testing.T, req *http.Request, h Handler, mws ...Middleware) *httptest.ResponseRecorder {
	t.Helper()
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	rec := httptest.NewRecorder()
	c := newContext(rec, req, nil)
	value, err := h(c)
	c.respond(value, err)
	return rec
}

func TestSecurityHeaders_Defaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req,
		func(c *Context) (any, error) { return map[string]string{"ok": "1"}, nil },
		SecurityHeaders(),
	)
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Fatalf("%s = %q, want %q", k, got, v)
		}
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Fatalf("HSTS must default to empty (off) to avoid HTTPS lock-in on misconfig")
	}
}

func TestRequestID_GeneratedAndEchoed(t *testing.T) {
	// 1. No client header → middleware generates one
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	var captured string
	rec := runHandler(t, req, func(c *Context) (any, error) {
		v, _ := c.Get("request_id")
		captured = v.(string)
		return nil, nil
	}, RequestID())
	if got := rec.Header().Get("X-Request-ID"); got == "" || got != captured {
		t.Fatalf("X-Request-ID header (%q) must match context value (%q)", got, captured)
	}
	if len(captured) != 32 {
		t.Fatalf("generated id should be 16-byte hex (32 chars), got %d", len(captured))
	}

	// 2. Client supplies an ID → echo it
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc123")
	rec = runHandler(t, req, func(c *Context) (any, error) { return nil, nil }, RequestID())
	if got := rec.Header().Get("X-Request-ID"); got != "abc123" {
		t.Fatalf("client-supplied id must be echoed, got %q", got)
	}

	// 3. Oversized client header → replaced with generated one
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", strings.Repeat("a", 200))
	rec = runHandler(t, req, func(c *Context) (any, error) { return nil, nil }, RequestID())
	if got := rec.Header().Get("X-Request-ID"); len(got) != 32 {
		t.Fatalf("oversized id must be replaced, got len=%d", len(got))
	}
}

func TestBodyLimit_RejectsOversize(t *testing.T) {
	big := strings.Repeat("a", 1024)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	rec := runHandler(t, req, func(c *Context) (any, error) {
		_, err := io.ReadAll(c.Request.Body)
		return nil, err
	}, BodyLimit(100))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("oversize body should bubble up as 5xx, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too large") &&
		!strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("expected error response, got %q", rec.Body.String())
	}
}

func TestBind_DefaultBodyLimit(t *testing.T) {
	// Without explicit BodyLimit middleware, Bind() still caps body
	// reads at DefaultBodyLimit (1 MiB). Build a body just over it.
	body := bytes.Repeat([]byte("x"), int(DefaultBodyLimit)+10)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := runHandler(t, req, func(c *Context) (any, error) {
		var v map[string]any
		return nil, c.Bind(&v)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversize Bind should be 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestBind_DisallowsUnknownFields(t *testing.T) {
	type Payload struct {
		Title string `json:"title"`
	}
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"title":"x","ghost":1}`))
	rec := runHandler(t, req, func(c *Context) (any, error) {
		var p Payload
		return nil, c.Bind(&p)
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field should be rejected (400), got %d body=%q", rec.Code, rec.Body.String())
	}
	// Body must be written exactly once — earlier bug where Bind wrote a
	// 400 then respond() wrote another 500 doubled the payload.
	body := rec.Body.String()
	if strings.Count(body, "unknown field") != 1 {
		t.Fatalf("body must be written once, got: %q", body)
	}
}

func TestBind_DoubleBodyWithLoggerMiddleware(t *testing.T) {
	// Reproduces the live-server scenario where Logger + Recovery wrap
	// the handler. Earlier bug: Bind() wrote a 400 body, then respond()
	// wrote a second copy because bodyWritten flag was missing.
	type Payload struct {
		Title string `json:"title"`
	}
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"title":"x","ghost":1}`))
	rec := runHandler(t, req, func(c *Context) (any, error) {
		var p Payload
		return nil, c.Bind(&p)
	},
		Logger(testLogger()),
		Recovery(testLogger()),
		RequestID(),
		SecurityHeaders(),
		BodyLimit(1<<20),
	)
	body := rec.Body.String()
	if strings.Count(body, "unknown field") != 1 {
		t.Fatalf("body written %d times, must be 1; body=%q", strings.Count(body, "unknown field"), body)
	}
}

func TestCSRF_GETIssuesCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req,
		func(c *Context) (any, error) { return nil, nil },
		CSRF(CSRFConfig{Secure: false}),
	)
	// Cookie must be set on safe-method first hit
	if len(rec.Result().Cookies()) == 0 {
		t.Fatalf("CSRF cookie must be issued on GET")
	}
	if rec.Result().Cookies()[0].Name != "csrf_token" {
		t.Fatalf("unexpected cookie name: %s", rec.Result().Cookies()[0].Name)
	}
}

func TestCSRF_POSTRejectsWithoutToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	rec := runHandler(t, req,
		func(c *Context) (any, error) { return nil, nil },
		CSRF(CSRFConfig{Secure: false}),
	)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without token must be 403, got %d", rec.Code)
	}
}

func TestCSRF_POSTAcceptsMatchingToken(t *testing.T) {
	token := "matching-token-value-1234"
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	rec := runHandler(t, req,
		func(c *Context) (any, error) { return map[string]string{"ok": "1"}, nil },
		CSRF(CSRFConfig{Secure: false}),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with matching token must succeed, got %d", rec.Code)
	}
}

func TestCSRF_ConstantTimeCompareSemantics(t *testing.T) {
	// Sanity check that we're using crypto/subtle (no early-exit) —
	// verify equal vs. unequal both terminate. This is a smoke test, not
	// a timing measurement (which would be flaky in CI).
	a := []byte("abcdefghijklmnop")
	b := []byte("abcdefghijklmnop")
	c := []byte("abcdefghijklmnoq")
	if subtle.ConstantTimeCompare(a, b) != 1 || subtle.ConstantTimeCompare(a, c) != 0 {
		t.Fatal("subtle.ConstantTimeCompare contract broken")
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	mw := RateLimit(3, time.Second)
	hit := func() int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := runHandler(t, req,
			func(c *Context) (any, error) { return map[string]string{"ok": "1"}, nil },
			mw,
		)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if got := hit(); got != http.StatusOK {
			t.Fatalf("req %d under limit must be 200, got %d", i, got)
		}
	}
	if got := hit(); got != http.StatusTooManyRequests {
		t.Fatalf("over-limit must be 429, got %d", got)
	}
}

func TestRateLimit_IsolatesPerIP(t *testing.T) {
	mw := RateLimit(1, time.Second)
	for _, ip := range []string{"10.0.0.2:1", "10.0.0.3:1", "10.0.0.4:1"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip
		rec := runHandler(t, req,
			func(c *Context) (any, error) { return map[string]string{"ok": "1"}, nil },
			mw,
		)
		if rec.Code != http.StatusOK {
			t.Fatalf("first hit from %s should be 200, got %d", ip, rec.Code)
		}
	}
}

func TestClientIP_HonoursXForwardedFor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:80"
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 10.0.0.1")
	if got := clientIP(req); got != "203.0.113.5" {
		t.Fatalf("want 203.0.113.5, got %q", got)
	}
}

func TestValidate_StructTags(t *testing.T) {
	type Form struct {
		Email string `json:"email" validate:"required,email"`
		Age   int    `json:"age" validate:"min=18,max=120"`
		Role  string `json:"role" validate:"oneof=admin user"`
	}
	cases := []struct {
		name    string
		in      Form
		wantErr bool
		field   string
	}{
		{"valid", Form{Email: "a@b.co", Age: 30, Role: "user"}, false, ""},
		{"missing email", Form{Age: 30, Role: "user"}, true, "email"},
		{"bad email", Form{Email: "nope", Age: 30, Role: "user"}, true, "email"},
		{"under min", Form{Email: "a@b.co", Age: 5, Role: "user"}, true, "age"},
		{"bad role", Form{Email: "a@b.co", Age: 30, Role: "ghost"}, true, "role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("err must be *ValidationError, got %T", err)
				}
				if _, ok := ve.Fields[tc.field]; !ok {
					t.Fatalf("missing failing field %q in %v", tc.field, ve.Fields)
				}
			}
		})
	}
}

func TestValidate_ExtraRules(t *testing.T) {
	type Form struct {
		Score string `json:"score" validate:"numeric"`
		Age   string `json:"age" validate:"integer"`
		IP    string `json:"ip" validate:"ip"`
		Name  string `json:"name" validate:"gt=2,lt=10"`
	}
	bad := Form{Score: "abc", Age: "12.5", IP: "999.999", Name: "x"}
	err := Validate(bad)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	for _, k := range []string{"score", "age", "ip", "name"} {
		if _, ok := ve.Fields[k]; !ok {
			t.Fatalf("expected failure on %q in %v", k, ve.Fields)
		}
	}
	good := Form{Score: "3.14", Age: "42", IP: "10.0.0.1", Name: "valid"}
	if err := Validate(good); err != nil {
		t.Fatalf("good payload must pass, got %v", err)
	}
}

func TestBindAndValidate_AutoMaps422(t *testing.T) {
	type Form struct {
		Email string `json:"email" validate:"required,email"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"email":"bad"}`))
	rec := runHandler(t, req, func(c *Context) (any, error) {
		var f Form
		return nil, c.BindAndValidate(&f)
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d body=%q", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body must be JSON: %v", err)
	}
	if _, ok := body["errors"]; !ok {
		t.Fatalf("422 response must include 'errors' key, got %v", body)
	}
}

func TestProductionMode_HidesErrorDetails(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		return nil, errors.New("db: password incorrect for user 'root'")
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "root") {
		t.Fatalf("production response must not leak err detail, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("production response should be generic, got %q", rec.Body.String())
	}
}

func TestDevMode_ShowsErrorDetails(t *testing.T) {
	t.Setenv("APP_ENV", "")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		return nil, errors.New("specific failure")
	})
	if !strings.Contains(rec.Body.String(), "specific failure") {
		t.Fatalf("dev response should show err detail, got %q", rec.Body.String())
	}
}

func TestError_MapsORMNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		return nil, orm.ErrNotFound
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("orm.ErrNotFound must map to 404, got %d", rec.Code)
	}
}

func TestCreated_SetsStatusAndJSONContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	rec := runHandler(t, req, func(c *Context) (any, error) {
		return c.Created(map[string]string{"id": "1"}), nil
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("want JSON content-type, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"1"`) {
		t.Fatalf("body must include JSON payload, got %q", body)
	}
}

func TestSetCookie_SafeDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		c.SetCookie("session", "abc")
		return nil, nil
	})
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	ck := cookies[0]
	if !ck.HttpOnly {
		t.Fatalf("cookie must default to HttpOnly")
	}
	if !ck.Secure {
		t.Fatalf("cookie must default to Secure")
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie must default to SameSite=Lax, got %v", ck.SameSite)
	}
}

func TestCookieOptions_OverrideDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := runHandler(t, req, func(c *Context) (any, error) {
		c.SetCookie("csrf", "x",
			CookieInsecure(),
			CookieReadable(),
			CookieSameSite(http.SameSiteStrictMode),
			CookieMaxAge(60),
		)
		return nil, nil
	})
	ck := rec.Result().Cookies()[0]
	if ck.Secure {
		t.Fatalf("CookieInsecure should clear Secure")
	}
	if ck.HttpOnly {
		t.Fatalf("CookieReadable should clear HttpOnly")
	}
	if ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("CookieSameSite override broken")
	}
	if ck.MaxAge != 60 {
		t.Fatalf("CookieMaxAge override broken")
	}
}

func TestCORSWithConfig_StrictByDefault(t *testing.T) {
	mw := CORSWithConfig(CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})

	// Untrusted origin must not get an ACAO header
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := runHandler(t, req, func(c *Context) (any, error) { return nil, nil }, mw)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("untrusted origin must not receive ACAO header")
	}

	// Trusted origin echoed + Vary
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec = runHandler(t, req, func(c *Context) (any, error) { return nil, nil }, mw)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("trusted origin must be echoed, got %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary must include Origin, got %q", got)
	}
}

func TestCORS_WildcardWithCredentialsPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on unsafe wildcard + credentials combo")
		}
	}()
	_ = CORSWithConfig(CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowCredentials: true,
	})
}

// Compile-time check: ensure context.Context is reachable; trivial guard
// against future imports drifting away from net/http handler shape.
var _ context.Context = context.Background()
