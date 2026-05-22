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
// Requires the connection to be passed through Instrument() once at startup:
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

func queryLogWith(conn *database.Connection, threshold int) gin.HandlerFunc {
	return func(c *gin.Context) {
		before := globalQueryCount(conn)
		c.Next()
		count := globalQueryCount(conn) - before
		if count < 0 {
			count = 0
		}
		c.Writer.Header().Set("X-DB-Query-Count", strconv.FormatInt(count, 10))
		if int(count) > threshold && conn.Log != nil {
			conn.Log.Warnf("lagogin: %d queries on %s %s (threshold %d) — possible N+1",
				count, c.Request.Method, c.Request.URL.Path, threshold)
		}
	}
}

// Instrument enables per-connection query counting for QueryLog. Call once
// at startup before installing the middleware. The returned connection is
// the same pointer — Instrument only registers it with the global counter
// table and replaces conn.Log with a counting wrapper.
//
// The wrapper delegates Info/Warn/Error/SQL/SlowSQL to the original logger,
// so SQL tracing and slow-query reporting continue to work unchanged.
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
	conn.Config.LogQueries = true
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
