// Package testing provides helpers for running migrations against an
// ephemeral SQLite database, useful for unit and integration tests.
//
// Importing this package introduces a transitive dependency on the SQLite
// driver and the migrations engine; tests in production code may want a
// thinner harness.
package testing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/logger"
	"github.com/devituz/lagodev/migrations"
)

// SQLite opens a temporary SQLite database, runs all registered migrations,
// and returns a connection and a cleanup function. Use it inside TestMain or
// individual tests.
func SQLite(t testing.TB, options ...Option) (*database.Connection, func()) {
	t.Helper()
	opts := defaults()
	for _, o := range options {
		o(&opts)
	}
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	if opts.Memory {
		dsn = "file::memory:?cache=shared"
	}
	mgr := database.NewManager()
	cfg := database.Config{Driver: "sqlite", DSN: dsn, LogQueries: opts.LogQueries}
	conn, err := mgr.Open("test", cfg)
	if err != nil {
		t.Fatalf("testing: open sqlite: %v", err)
	}
	if opts.Logger != nil {
		conn.Log = opts.Logger
	}
	if opts.Migrate {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		migrator := migrations.New(conn, opts.Registry, migrations.Options{Logger: opts.Logger})
		if _, err := migrator.Up(ctx); err != nil {
			t.Fatalf("testing: migrate: %v", err)
		}
	}
	cleanup := func() {
		_ = mgr.Close()
		if !opts.Memory {
			_ = os.RemoveAll(dir)
		}
	}
	return conn, cleanup
}

// Option configures the harness.
type Option func(*opts)

type opts struct {
	Memory     bool
	Migrate    bool
	Registry   *migrations.Registry
	Logger     *logger.Logger
	LogQueries bool
}

func defaults() opts {
	return opts{Memory: true, Migrate: true, Registry: migrations.Default}
}

// WithMemory toggles the in-memory database (default true).
func WithMemory(v bool) Option { return func(o *opts) { o.Memory = v } }

// WithMigrate toggles automatic migration on open.
func WithMigrate(v bool) Option { return func(o *opts) { o.Migrate = v } }

// WithRegistry overrides the migrations registry.
func WithRegistry(r *migrations.Registry) Option { return func(o *opts) { o.Registry = r } }

// WithLogger enables SQL logging on the connection.
func WithLogger(l *logger.Logger) Option { return func(o *opts) { o.Logger = l; o.LogQueries = true } }

// MustMigrate runs the migrator and fails t on error.
func MustMigrate(t testing.TB, conn *database.Connection) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(ctx); err != nil {
		t.Fatalf("testing: migrate: %v", err)
	}
}

// Require fails t if err is non-nil.
func Require(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("require: %v", err)
	}
}

// AssertExists fails if the row matching where does not exist.
func AssertExists(t testing.TB, conn *database.Connection, table string, where map[string]any) {
	t.Helper()
	if !exists(t, conn, table, where) {
		t.Fatalf("AssertExists: no row in %s matching %v", table, where)
	}
}

// AssertMissing fails if a row matching where exists.
func AssertMissing(t testing.TB, conn *database.Connection, table string, where map[string]any) {
	t.Helper()
	if exists(t, conn, table, where) {
		t.Fatalf("AssertMissing: row in %s matched %v", table, where)
	}
}

func exists(t testing.TB, conn *database.Connection, table string, where map[string]any) bool {
	t.Helper()
	g := conn.Grammar
	var sb strings.Builder
	sb.WriteString("SELECT COUNT(*) FROM ")
	sb.WriteString(g.Quote(table))
	args := make([]any, 0, len(where))
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		i := 0
		for k, v := range where {
			if i > 0 {
				sb.WriteString(" AND ")
			}
			i++
			fmt.Fprintf(&sb, "%s = %s", g.Quote(k), g.Placeholder(i))
			args = append(args, v)
		}
	}
	var n int
	if err := conn.QueryRowContext(context.Background(), sb.String(), args...).Scan(&n); err != nil {
		t.Fatalf("exists: %v", err)
	}
	return n > 0
}

// SentinelError ensures errors are not silently dropped.
var ErrAssertion = errors.New("testing: assertion failed")
