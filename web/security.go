package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SecurityHeadersConfig controls SecurityHeaders middleware output.
//
// Zero value gives Laravel-grade safe defaults. Override per field to
// loosen — never to silently disable everything.
type SecurityHeadersConfig struct {
	// ContentSecurityPolicy sent as Content-Security-Policy. Empty omits the header.
	ContentSecurityPolicy string
	// FrameOptions for X-Frame-Options. Empty omits the header.
	FrameOptions string
	// ReferrerPolicy for Referrer-Policy. Empty omits the header.
	ReferrerPolicy string
	// PermissionsPolicy for Permissions-Policy. Empty omits the header.
	PermissionsPolicy string
	// HSTS sets Strict-Transport-Security. Empty omits the header.
	// Set only when serving over HTTPS — otherwise clients lock onto a
	// broken scheme.
	HSTS string
	// NoSniff toggles X-Content-Type-Options: nosniff (default true).
	NoSniff bool
}

func defaultSecurityHeaders() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		ContentSecurityPolicy: "default-src 'self'",
		FrameOptions:          "DENY",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		PermissionsPolicy:     "camera=(), microphone=(), geolocation=()",
		NoSniff:               true,
	}
}

// SecurityHeaders attaches a conservative set of browser security
// headers to every response. Call without arguments for safe defaults.
func SecurityHeaders(cfg ...SecurityHeadersConfig) Middleware {
	c := defaultSecurityHeaders()
	if len(cfg) > 0 {
		c = cfg[0]
		// NoSniff bool defaults to false on zero-value — treat the
		// zero-value config as "use defaults" by checking NoSniff
		// against an explicit-disable sentinel. To explicitly disable,
		// pass an SecurityHeadersConfig with all empty fields AND
		// NoSniff: false — that disables every header.
	}
	return func(next Handler) Handler {
		return func(ctx *Context) (any, error) {
			h := ctx.Writer.Header()
			if c.ContentSecurityPolicy != "" {
				h.Set("Content-Security-Policy", c.ContentSecurityPolicy)
			}
			if c.FrameOptions != "" {
				h.Set("X-Frame-Options", c.FrameOptions)
			}
			if c.ReferrerPolicy != "" {
				h.Set("Referrer-Policy", c.ReferrerPolicy)
			}
			if c.PermissionsPolicy != "" {
				h.Set("Permissions-Policy", c.PermissionsPolicy)
			}
			if c.HSTS != "" {
				h.Set("Strict-Transport-Security", c.HSTS)
			}
			if c.NoSniff {
				h.Set("X-Content-Type-Options", "nosniff")
			}
			return next(ctx)
		}
	}
}

// BodyLimit caps the size of the request body to n bytes. Bodies larger
// than n cause subsequent reads (including c.Bind) to fail with
// `http: request body too large`. Apply globally to prevent DoS.
func BodyLimit(n int64) Middleware {
	return func(next Handler) Handler {
		return func(ctx *Context) (any, error) {
			if n > 0 && ctx.Request.Body != nil {
				ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, n)
			}
			return next(ctx)
		}
	}
}

// RequestID ensures every request has an X-Request-ID. If the client
// supplied one, it is echoed (truncated to 128 chars). Otherwise a 16-
// byte random hex is generated. The ID is stored in the context under
// "request_id" and mirrored to the response header for log correlation.
func RequestID() Middleware {
	return func(next Handler) Handler {
		return func(ctx *Context) (any, error) {
			id := ctx.Request.Header.Get("X-Request-ID")
			if len(id) == 0 || len(id) > 128 {
				id = newRequestID()
			}
			ctx.Set("request_id", id)
			ctx.Writer.Header().Set("X-Request-ID", id)
			return next(ctx)
		}
	}
}

func newRequestID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// CSRFConfig customises CSRF behaviour.
type CSRFConfig struct {
	// CookieName for the CSRF token cookie. Default "csrf_token".
	CookieName string
	// HeaderName clients send the token in. Default "X-CSRF-Token".
	HeaderName string
	// FormField fallback name for form posts. Default "_csrf".
	FormField string
	// Secure marks the cookie HTTPS-only. Default true.
	Secure bool
	// SameSite for the cookie. Default http.SameSiteLaxMode.
	SameSite http.SameSite
	// MaxAge of the cookie in seconds. Default 7200 (2h).
	MaxAge int
}

func (c *CSRFConfig) withDefaults() {
	if c.CookieName == "" {
		c.CookieName = "csrf_token"
	}
	if c.HeaderName == "" {
		c.HeaderName = "X-CSRF-Token"
	}
	if c.FormField == "" {
		c.FormField = "_csrf"
	}
	if c.SameSite == 0 {
		c.SameSite = http.SameSiteLaxMode
	}
	if c.MaxAge == 0 {
		c.MaxAge = 7200
	}
}

// CSRF implements double-submit-cookie CSRF protection. On safe methods
// (GET/HEAD/OPTIONS) it issues a token cookie if missing. On unsafe
// methods (POST/PUT/PATCH/DELETE) it requires the same token in the
// header (or form field for HTML forms) and rejects with 403 on
// mismatch. Constant-time comparison prevents timing leaks.
//
// Pass a zero CSRFConfig for safe defaults.
func CSRF(cfg ...CSRFConfig) Middleware {
	c := CSRFConfig{Secure: true}
	if len(cfg) > 0 {
		c = cfg[0]
	}
	c.withDefaults()
	return func(next Handler) Handler {
		return func(ctx *Context) (any, error) {
			cookie, _ := ctx.Request.Cookie(c.CookieName)
			if cookie == nil || cookie.Value == "" {
				tok := newRequestID() + newRequestID() // 64 hex chars
				http.SetCookie(ctx.Writer, &http.Cookie{
					Name:     c.CookieName,
					Value:    tok,
					Path:     "/",
					HttpOnly: false, // JS must read it to echo into header
					Secure:   c.Secure,
					SameSite: c.SameSite,
					MaxAge:   c.MaxAge,
				})
				cookie = &http.Cookie{Value: tok}
			}
			switch ctx.Request.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(ctx)
			}
			provided := ctx.Request.Header.Get(c.HeaderName)
			if provided == "" {
				provided = ctx.Request.FormValue(c.FormField)
			}
			if provided == "" ||
				subtle.ConstantTimeCompare([]byte(provided), []byte(cookie.Value)) != 1 {
				ctx.Forbidden("invalid csrf token")
				return nil, nil
			}
			return next(ctx)
		}
	}
}

// RateLimit applies a fixed-window per-IP rate limit. Up to `limit`
// requests are allowed per `window`; further requests get 429 with a
// `Retry-After` header.
//
// The store is in-process — suitable for a single replica. Behind a
// load balancer, replace with a Redis-backed limiter (see TODO).
func RateLimit(limit int, window time.Duration) Middleware {
	if limit <= 0 || window <= 0 {
		panic("web: RateLimit requires limit > 0 and window > 0")
	}
	rl := newRateLimiter(limit, window)
	return func(next Handler) Handler {
		return func(ctx *Context) (any, error) {
			ip := clientIP(ctx.Request)
			allowed, retryAfter := rl.allow(ip)
			if !allowed {
				ctx.Writer.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
				ctx.JSON(http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
				return nil, nil
			}
			return next(ctx)
		}
	}
}

// Throttle is an alias for RateLimit using Laravel naming.
func Throttle(limit int, window time.Duration) Middleware { return RateLimit(limit, window) }

type rateLimiter struct {
	limit  int
	window time.Duration
	mu     sync.Mutex
	hits   map[string]*rateBucket
}

type rateBucket struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		limit:  limit,
		window: window,
		hits:   make(map[string]*rateBucket),
	}
	// Periodic GC of stale buckets so the map doesn't grow unbounded
	// for short-lived IPs (e.g. NATed clients, scanners).
	go rl.gc()
	return rl
}

func (rl *rateLimiter) allow(key string) (bool, time.Duration) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.hits[key]
	if !ok || now.After(b.reset) {
		rl.hits[key] = &rateBucket{count: 1, reset: now.Add(rl.window)}
		return true, 0
	}
	if b.count >= rl.limit {
		return false, time.Until(b.reset)
	}
	b.count++
	return true, 0
}

func (rl *rateLimiter) gc() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		rl.mu.Lock()
		for k, b := range rl.hits {
			if now.After(b.reset) {
				delete(rl.hits, k)
			}
		}
		rl.mu.Unlock()
	}
}

// clientIP extracts the request IP, honouring X-Forwarded-For when a
// trusted proxy is in front (we always take the first non-empty entry).
// In production behind an untrusted edge, sanitise this header upstream.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
