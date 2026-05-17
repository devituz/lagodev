package web

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/devituz/lagodev/auth"
)

// Logger — har bir so'rov uchun method, path, status kodi va davomiyligini
// log'ga yozadigan standart middleware. App.New() avtomat qo'shadi.
func Logger(l *log.Logger) Middleware {
	return func(next Handler) Handler {
		return func(c *Context) (any, error) {
			start := time.Now()
			lw := &loggingWriter{ResponseWriter: c.Writer, status: 200}
			c.Writer = lw
			value, err := next(c)
			l.Printf("%s %s %d %s", c.Request.Method, c.Request.URL.Path, lw.status, time.Since(start).Round(time.Microsecond))
			return value, err
		}
	}
}

// Recovery — panic'ni tutib, JSON 500 javobini qaytaradigan middleware.
// Stack trace stderrga yoziladi. App.New() avtomat qo'shadi.
func Recovery(l *log.Logger) Middleware {
	return func(next Handler) Handler {
		return func(c *Context) (value any, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					l.Printf("panic: %v\n%s", rec, debug.Stack())
					value = nil
					err = fmt.Errorf("internal server error: %v", rec)
				}
			}()
			return next(c)
		}
	}
}

// CORS — sodda CORS middleware. Origin'ni allowedOrigins ro'yxati bilan
// taqqoslaydi (yoki "*" — har qanday).
func CORS(allowedOrigins ...string) Middleware {
	allowAll := false
	allowed := map[string]struct{}{}
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAll = true
		}
		allowed[o] = struct{}{}
	}
	return func(next Handler) Handler {
		return func(c *Context) (any, error) {
			origin := c.Request.Header.Get("Origin")
			if allowAll {
				c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
			} else if _, ok := allowed[origin]; ok {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
				c.Writer.Header().Set("Vary", "Origin")
			}
			c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
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

// loggingWriter — Logger() middleware'i status kodini saqlash uchun
// ResponseWriter'ni o'rab oladi.
type loggingWriter struct {
	http.ResponseWriter
	status int
}

func (w *loggingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
