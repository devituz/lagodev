package web

import (
	"bufio"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/devituz/lagodev/auth"
)

// Logger — request access logger. Writes one line per request with
// method, path, status, latency, client IP, response size (bytes
// written), and (if RequestID middleware ran before it) the request
// id. App.New() applies this automatically.
//
// Format: `METHOD PATH STATUS DURATION ip=IP size=BYTES req=ID`.
func Logger(l *log.Logger) Middleware {
	return func(next Handler) Handler {
		return func(c *Context) (any, error) {
			start := time.Now()
			lw := &loggingWriter{ResponseWriter: c.Writer, status: 200}
			c.Writer = lw
			value, err := next(c)
			id, _ := c.Get("request_id")
			reqID, _ := id.(string)
			if reqID == "" {
				reqID = "-"
			}
			l.Printf("%s %s %d %s ip=%s size=%d req=%s",
				c.Request.Method,
				c.Request.URL.Path,
				lw.status,
				time.Since(start).Round(time.Microsecond),
				clientIP(c.Request),
				lw.bytes,
				reqID,
			)
			return value, err
		}
	}
}

// Recovery — catches handler panics, logs the recovered value + stack
// trace to l, and returns a generic 500 JSON response. The recovered
// value is **never** sent to the client (it would otherwise leak
// secrets like "panic: db password: hunter2"). App.New() applies this
// automatically.
func Recovery(l *log.Logger) Middleware {
	return func(next Handler) Handler {
		return func(c *Context) (value any, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					l.Printf("panic %s %s: %v\n%s",
						c.Request.Method, c.Request.URL.Path, rec, debug.Stack())
					value = nil
					err = errInternal
				}
			}()
			return next(c)
		}
	}
}

// errInternal is the opaque error Recovery returns. respond/Error
// surface a generic "internal server error" so the recovered value
// never reaches the client.
var errInternal = errors.New("internal server error")

// CORS — sodda CORS middleware. Origin'ni allowedOrigins ro'yxati bilan
// taqqoslaydi (yoki "*" — har qanday). Kuchaytirilgan default'lar bilan:
//   - Faqat ruxsat etilgan origin'ga "Access-Control-Allow-Origin" sarlavhasi
//     yuboriladi (boshqa origin'ga umuman jo'natilmaydi).
//   - Preflight uchun Access-Control-Request-Headers aks sado beriladi.
//   - "*" + credentials kombinatsiyasi taqiqlangan (brauzer ham qabul
//     qilmaydi; framework bu xavfsiz emas konfiguratsiyani yubormaydi).
//   - Preflight javoblarini brauzer 10 minut cache qiladi.
//
// Credentials (cookie/Authorization) ruxsat berish uchun
// CORSConfig orqali AllowCredentials = true sozlang va wildcard
// ishlatmang.
func CORS(allowedOrigins ...string) Middleware {
	return CORSWithConfig(CORSConfig{AllowedOrigins: allowedOrigins})
}

// CORSConfig kengaytirilgan CORS sozlamalari.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAgeSeconds    int
}

// CORSWithConfig — CORSConfig'dan middleware quradi.
func CORSWithConfig(cfg CORSConfig) Middleware {
	allowAll := false
	allowed := map[string]struct{}{}
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			allowAll = true
		}
		allowed[o] = struct{}{}
	}
	if allowAll && cfg.AllowCredentials {
		panic("web: CORS wildcard origin with AllowCredentials is unsafe")
	}
	if len(cfg.AllowedMethods) == 0 {
		cfg.AllowedMethods = []string{
			http.MethodGet, http.MethodPost, http.MethodPut,
			http.MethodPatch, http.MethodDelete, http.MethodOptions,
		}
	}
	if len(cfg.AllowedHeaders) == 0 {
		cfg.AllowedHeaders = []string{"Content-Type", "Authorization", "X-CSRF-Token", "X-Request-ID"}
	}
	if cfg.MaxAgeSeconds == 0 {
		cfg.MaxAgeSeconds = 600
	}
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	exposed := strings.Join(cfg.ExposedHeaders, ", ")

	return func(next Handler) Handler {
		return func(c *Context) (any, error) {
			origin := c.Request.Header.Get("Origin")
			h := c.Writer.Header()
			matched := false
			switch {
			case allowAll:
				h.Set("Access-Control-Allow-Origin", "*")
				matched = true
			case origin != "":
				if _, ok := allowed[origin]; ok {
					h.Set("Access-Control-Allow-Origin", origin)
					h.Add("Vary", "Origin")
					matched = true
				}
			}
			if matched {
				h.Set("Access-Control-Allow-Methods", methods)
				if reqHeaders := c.Request.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					h.Set("Access-Control-Allow-Headers", reqHeaders)
				} else {
					h.Set("Access-Control-Allow-Headers", headers)
				}
				if exposed != "" {
					h.Set("Access-Control-Expose-Headers", exposed)
				}
				if cfg.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				h.Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAgeSeconds))
			}
			if c.Request.Method == http.MethodOptions {
				c.NoContent()
				return nil, nil
			}
			return next(c)
		}
	}
}

// Auth — sodda Bearer-token middleware. Faqat sarlavha mavjudligini
// tekshiradi va xom token'ni context'ga yozadi. Token'ni tekshirish kerak
// bo'lsa AuthJWT(manager) ishlating.
func Auth() Middleware {
	return func(next Handler) Handler {
		return func(c *Context) (any, error) {
			h := c.Request.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				c.Unauthorized("missing bearer token")
				return nil, nil
			}
			token := strings.TrimPrefix(h, "Bearer ")
			c.Set("auth_token", token)
			return next(c)
		}
	}
}

// AuthJWT — JWT'ni tekshiradigan to'liq middleware. Token noto'g'ri yoki
// muddati o'tgan bo'lsa 401 qaytaradi. Muvaffaqiyat holida claims
// context'ga yoziladi: "auth_user_id", "auth_role", "auth_claims".
func AuthJWT(m *auth.Manager) Middleware {
	return func(next Handler) Handler {
		return func(c *Context) (any, error) {
			h := c.Request.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				c.Unauthorized("missing bearer token")
				return nil, nil
			}
			claims, err := m.Parse(strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				c.Unauthorized("invalid or expired token")
				return nil, nil
			}
			c.Set("auth_user_id", claims.UserID)
			c.Set("auth_role", claims.Role)
			c.Set("auth_claims", claims)
			return next(c)
		}
	}
}

// loggingWriter wraps http.ResponseWriter so Logger() can capture the
// final status code and the number of body bytes written.
type loggingWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush forwards to the underlying writer so streaming/SSE responses are
// not buffered when Logger wraps the writer. http.ResponseWriter does not
// embed http.Flusher, so without this method `c.Writer.(http.Flusher)`
// would fail through the default middleware stack and Flush would be a
// silent no-op.
func (w *loggingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer when it supports connection
// hijacking (WebSocket upgrades, raw TCP). Returns http.ErrNotSupported
// otherwise, matching the stdlib contract.
func (w *loggingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Push forwards HTTP/2 server push to the underlying writer when
// supported, otherwise reports http.ErrNotSupported.
func (w *loggingWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// ReadFrom forwards to the underlying writer's io.ReaderFrom when
// available (zero-copy file sends via sendfile), counting the bytes for
// the access log. Falls back to a plain copy otherwise.
func (w *loggingWriter) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		w.bytes += int(n)
		return n, err
	}
	n, err := io.Copy(w.ResponseWriter, r)
	w.bytes += int(n)
	return n, err
}
