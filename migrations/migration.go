// Package migrations implements a Laravel-style migration engine. A
// migration is any type that implements the Migration interface; the engine
// applies them in lexical name order (timestamp-prefixed by convention) and
// records each application in a repository table.
//
// Migrations interact with the database through schema.Definition values
// returned via the Context API, so they remain driver-agnostic.
package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/schema"
)

// Migration is the canonical interface migrations implement. The Name() must
// be globally unique within a registry and is the migration's primary
// identifier in the migrations table.
type Migration interface {
	Name() string
	Up(ctx *Context) error
	Down(ctx *Context) error
}

// Context is the value passed to migration callbacks. It exposes the
// underlying connection plus convenience helpers for applying schema
// definitions and executing raw SQL.
type Context struct {
	Conn *database.Connection
	Tx   *database.Tx
	Ctx  context.Context

	preview bool
	sqlSink []string
}

// IsPreview reports whether this is a SQL-preview run (no execution).
func (c *Context) IsPreview() bool { return c.preview }

// Executor returns the active executor (transaction if open, else connection).
func (c *Context) Executor() database.Executor {
	if c.Tx != nil {
		return c.Tx
	}
	return c.Conn
}

// Schema applies a schema.Definition to the database (or records its SQL in
// preview mode). It is the primary API used inside Up/Down.
func (c *Context) Schema(def *schema.Definition) error {
	stmts, err := schema.NewCompiler(c.Conn.Grammar).Compile(def)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if c.preview {
			c.sqlSink = append(c.sqlSink, s.SQL)
			continue
		}
		if _, err := c.Executor().ExecContext(c.Ctx, s.SQL, s.Args...); err != nil {
			return err
		}
	}
	return nil
}

// Raw executes a raw SQL statement.
func (c *Context) Raw(sqlStr string, args ...any) error {
	if c.preview {
		c.sqlSink = append(c.sqlSink, sqlStr)
		return nil
	}
	_, err := c.Executor().ExecContext(c.Ctx, sqlStr, args...)
	return err
}

// CollectedSQL returns the SQL accumulated during a preview run.
func (c *Context) CollectedSQL() []string { return c.sqlSink }

// FuncMigration adapts up/down callbacks into a Migration. It is the most
// common way to declare a migration in user code.
type FuncMigration struct {
	N    string
	UpFn func(ctx *Context) error
	DnFn func(ctx *Context) error
}

// Name returns the migration name.
func (m *FuncMigration) Name() string { return m.N }

// Up runs the up callback.
func (m *FuncMigration) Up(ctx *Context) error {
	if m.UpFn == nil {
		return nil
	}
	return m.UpFn(ctx)
}

// Down runs the down callback.
func (m *FuncMigration) Down(ctx *Context) error {
	if m.DnFn == nil {
		return nil
	}
	return m.DnFn(ctx)
}

// Define builds a FuncMigration with the given name and callbacks.
// Use this in init() to register an inline migration without writing a type.
func Define(name string, up, down func(ctx *Context) error) Migration {
	return &FuncMigration{N: name, UpFn: up, DnFn: down}
}

// Checksum computes a stable checksum for a migration by hashing the SQL it
// emits in preview mode. This lets the engine detect when a migration was
// edited after being applied.
func Checksum(conn *database.Connection, m Migration) (string, error) {
	ctx := &Context{Conn: conn, Ctx: context.Background(), preview: true}
	if err := m.Up(ctx); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, s := range ctx.sqlSink {
		h.Write([]byte(s))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Now returns the current wall clock; abstracted so tests can stub it.
var Now = func() time.Time { return time.Now().UTC() }
