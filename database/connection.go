package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devituz/lagodev/logger"
)

// Executor is the minimal interface that every query target satisfies — both
// raw *sql.DB and *sql.Tx implement it. ORM/query code accepts an Executor so
// the same calls work transparently inside or outside a transaction.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// Grammar is implemented by drivers to provide SQL dialect-specific
// rendering. Schema/migration/query packages depend on this interface
// rather than on a concrete driver.
type Grammar interface {
	// Name returns the driver name, e.g. "postgres".
	Name() string
	// Quote wraps an identifier (table or column) in dialect quotes.
	Quote(ident string) string
	// Placeholder returns the placeholder for the n-th positional argument
	// (1-indexed). "?" for MySQL/SQLite, "$1", "$2", … for Postgres.
	Placeholder(n int) string
	// CompileType maps a portable column kind + options to a dialect-specific
	// SQL fragment. See schema.CompileType for full options.
	CompileType(kind string, opts ColumnTypeOptions) string
	// SupportsReturning indicates whether INSERT … RETURNING is supported.
	SupportsReturning() bool
	// LastInsertIDStrategy describes how a driver returns IDs from inserts.
	LastInsertIDStrategy() InsertIDStrategy
}

// InsertIDStrategy enumerates how a driver returns the last-inserted ID.
type InsertIDStrategy int

const (
	// InsertIDFromResult uses sql.Result.LastInsertId() (MySQL, SQLite).
	InsertIDFromResult InsertIDStrategy = iota
	// InsertIDViaReturning appends RETURNING <pk> to inserts (Postgres).
	InsertIDViaReturning
)

// ColumnTypeOptions carries portable options understood by Grammar.CompileType.
type ColumnTypeOptions struct {
	Length        int
	Precision     int
	Scale         int
	Unsigned      bool
	AutoIncrement bool
	Allowed       []string // for ENUM/SET
}

// Connection wraps *sql.DB together with the dialect grammar and connection
// metadata. It is goroutine-safe and the canonical handle passed across the
// framework.
type Connection struct {
	Name    string
	DB      *sql.DB
	Grammar Grammar
	Config  Config
	Log     *logger.Logger
	// Loc is the active *time.Location resolved from Config.TimeZone. The
	// ORM uses it for CreatedAt/UpdatedAt timestamps so values written to
	// the database honor the application's selected time zone.
	Loc *time.Location

	mu     sync.RWMutex
	closed bool
}

// Location returns the active *time.Location (always non-nil; defaults to
// time.UTC). Use this to format or convert times consistently across the
// app: `t.In(conn.Location())`.
func (c *Connection) Location() *time.Location {
	if c.Loc != nil {
		return c.Loc
	}
	return time.UTC
}

// Now returns the current time in the connection's configured location.
// The ORM uses this when populating CreatedAt/UpdatedAt.
func (c *Connection) Now() time.Time {
	return time.Now().In(c.Location())
}

// Ping verifies the connection is alive.
func (c *Connection) Ping(ctx context.Context) error {
	return c.DB.PingContext(ctx)
}

// Close terminates the underlying pool. Subsequent calls are no-ops.
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.DB.Close()
}

// ExecContext logs (if enabled) and forwards Exec to the pool.
func (c *Connection) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	start := time.Now()
	res, err := c.DB.ExecContext(ctx, query, args...)
	c.observe(ctx, query, args, time.Since(start), err)
	return res, err
}

// QueryContext logs and forwards Query.
func (c *Connection) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := c.DB.QueryContext(ctx, query, args...)
	c.observe(ctx, query, args, time.Since(start), err)
	return rows, err
}

// QueryRowContext logs and forwards QueryRow.
func (c *Connection) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	start := time.Now()
	row := c.DB.QueryRowContext(ctx, query, args...)
	c.observe(ctx, query, args, time.Since(start), nil)
	return row
}

// PrepareContext prepares a statement.
func (c *Connection) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return c.DB.PrepareContext(ctx, query)
}

// Transaction runs fn inside a transaction, committing on success or rolling
// back on error / panic. fn receives a *Tx that satisfies Executor.
func (c *Connection) Transaction(ctx context.Context, fn func(tx *Tx) error) error {
	return c.TransactionWith(ctx, nil, fn)
}

// TransactionWith allows setting transaction options.
func (c *Connection) TransactionWith(ctx context.Context, opts *sql.TxOptions, fn func(tx *Tx) error) (err error) {
	if fn == nil {
		return errors.New("database: nil transaction function")
	}
	raw, err := c.DB.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("database: begin: %w", err)
	}
	tx := &Tx{Tx: raw, conn: c}
	defer func() {
		if r := recover(); r != nil {
			_ = raw.Rollback()
			panic(r)
		}
	}()
	if err := fn(tx); err != nil {
		if rbErr := raw.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("database: rollback after error %v: %w", err, rbErr)
		}
		return err
	}
	if err := raw.Commit(); err != nil {
		return fmt.Errorf("database: commit: %w", err)
	}
	return nil
}

func (c *Connection) observe(ctx context.Context, query string, args []any, took time.Duration, err error) {
	if c.Log == nil {
		return
	}
	if c.Config.LogQueries {
		c.Log.SQL(ctx, query, args, took, err)
		return
	}
	if c.Config.SlowQuery > 0 && took >= c.Config.SlowQuery {
		c.Log.SlowSQL(ctx, query, args, took, err)
	}
}
