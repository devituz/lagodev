package lagogin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/auth"
	"github.com/devituz/lagodev/database"
)

// AuthJWT verifies an Authorization: Bearer <token> header. Valid claims are
// stored on the gin.Context as "auth_user_id", "auth_role", "auth_claims" —
// retrievable via Ctx.UserID(), Ctx.Role(), or c.Get(...).
//
// Invalid or missing tokens abort with 401 + JSON {"error": "..."}.
func AuthJWT(m *auth.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		claims, err := m.Parse(strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}
		c.Set("auth_user_id", claims.UserID)
		c.Set("auth_role", claims.Role)
		c.Set("auth_claims", claims)
		c.Next()
	}
}

// Auth is the lightweight cousin of AuthJWT — only checks the header is
// present, stores the raw token, and lets the handler verify it however
// it likes. Use AuthJWT when you already have an auth.Manager handy.
func Auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		c.Set("auth_token", strings.TrimPrefix(h, "Bearer "))
		c.Next()
	}
}

// CORS returns a CORS middleware. Pass explicit origins to restrict;
// use "*" or no args for permissive.
func CORS(allowed ...string) gin.HandlerFunc {
	allowAll := len(allowed) == 0
	set := map[string]struct{}{}
	for _, o := range allowed {
		if o == "*" {
			allowAll = true
		}
		set[o] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if allowAll {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		} else if _, ok := set[origin]; ok {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Vary", "Origin")
		}
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// RequestTimeout aborts the request after d. The handler observes a canceled
// context.Context; long-running DB calls bail out automatically.
func RequestTimeout(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// QueryLog — per-request SQL counter, surfaces as X-DB-Query-Count.
// ---------------------------------------------------------------------------

// QueryLog returns a middleware that counts SQL queries for the lifetime of
// each request and writes the total in the X-DB-Query-Count response header.
// If the count crosses threshold (20 by default), a WARN is logged with the
// request path — a cheap N+1 detector for dev environments.
//
// Queries are counted per request: those executed with the request's context
// (c.Ctx() / c.Request.Context()) on conn, plus manual ObserveQuery calls.
// QueryLog instruments conn itself; calling Instrument() first is optional:
//
//	conn = lagogin.Instrument(conn)
//	r.Use(lagogin.QueryLog(conn))
//
// Use QueryLogN to override the threshold.
func QueryLog(conn *database.Connection) gin.HandlerFunc {
	return queryLogWith(conn, 20)
}

// QueryLogN is QueryLog with a custom N+1 warning threshold.
func QueryLogN(conn *database.Connection, threshold int) gin.HandlerFunc {
	return queryLogWith(conn, threshold)
}

// queryCountKey carries the per-request query counter in the request context.
type queryCountKey struct{}

func queryLogWith(conn *database.Connection, threshold int) gin.HandlerFunc {
	Instrument(conn)
	return func(c *gin.Context) {
		n := new(atomic.Int64)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), queryCountKey{}, n))
		before := globalQueryCount(conn)
		counted := func() int64 {
			if v := n.Load() + globalQueryCount(conn) - before; v > 0 {
				return v
			}
			return 0
		}
		// Headers set after the handler wrote the body never reach the
		// client, so stamp the header when the status line is written.
		w := &queryCountWriter{ResponseWriter: c.Writer, count: counted}
		c.Writer = w
		c.Next()
		w.stamp()
		count := counted()
		if int(count) > threshold && conn.Log != nil {
			conn.Log.Warnf("lagogin: %d queries on %s %s (threshold %d) — possible N+1",
				count, c.Request.Method, c.Request.URL.Path, threshold)
		}
	}
}

// queryCountWriter sets X-DB-Query-Count right before the response header
// is committed.
type queryCountWriter struct {
	gin.ResponseWriter
	count   func() int64
	stamped bool
}

func (w *queryCountWriter) stamp() {
	if w.stamped || w.ResponseWriter.Written() {
		return
	}
	w.stamped = true
	w.Header().Set("X-DB-Query-Count", strconv.FormatInt(w.count(), 10))
}

func (w *queryCountWriter) WriteHeader(code int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(code)
}

func (w *queryCountWriter) WriteHeaderNow() {
	w.stamp()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *queryCountWriter) Write(b []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(b)
}

func (w *queryCountWriter) WriteString(s string) (int, error) {
	w.stamp()
	return w.ResponseWriter.WriteString(s)
}

// Instrument enables query counting for QueryLog. It is idempotent and
// returns the same pointer: it registers conn with the counter table and
// installs a database query hook that bumps the counter of the request whose
// context the statement ran with. Logging settings are left untouched.
//
// Previously nothing but manual ObserveQuery calls fed the counter, so
// X-DB-Query-Count was always 0 for real traffic.
func Instrument(conn *database.Connection) *database.Connection {
	if conn == nil {
		return nil
	}
	countersMu.Lock()
	defer countersMu.Unlock()
	if _, ok := counters[conn]; ok {
		return conn
	}
	counters[conn] = new(atomic.Int64)
	conn.OnQuery(func(ctx context.Context, _ string, _ []any, _ time.Duration, _ error) {
		if n, ok := ctx.Value(queryCountKey{}).(*atomic.Int64); ok {
			n.Add(1)
		}
	})
	return conn
}

// ObserveQuery bumps the per-connection counter. Called by Connection
// observation hooks; tests and custom executors can call it directly.
func ObserveQuery(conn *database.Connection) {
	countersMu.RLock()
	v, ok := counters[conn]
	countersMu.RUnlock()
	if !ok {
		return
	}
	v.Add(1)
}

func globalQueryCount(conn *database.Connection) int64 {
	countersMu.RLock()
	v, ok := counters[conn]
	countersMu.RUnlock()
	if !ok {
		return 0
	}
	return v.Load()
}

var (
	countersMu sync.RWMutex
	counters   = map[*database.Connection]*atomic.Int64{}
)
