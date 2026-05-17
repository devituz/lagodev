package migrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devituz/lagodev/database"
)

// Lock prevents two migrators from running concurrently against the same
// database. The implementation uses a row in a dedicated `migrations_lock`
// table; on databases that support advisory locks (Postgres) we use those
// instead transparently.
type Lock struct {
	Conn      *database.Connection
	Table     string
	HolderID  string
	heartbeat func() // tear-down for the held lock
}

// NewLock builds a Lock against conn.
func NewLock(conn *database.Connection, holder string) *Lock {
	return &Lock{Conn: conn, Table: "migrations_lock", HolderID: holder}
}

// Acquire blocks until the lock is held or ctx expires.
func (l *Lock) Acquire(ctx context.Context, timeout time.Duration) error {
	if l.Conn.Grammar.Name() == "postgres" {
		return l.acquirePgAdvisory(ctx)
	}
	return l.acquireRow(ctx, timeout)
}

// Release frees the lock.
func (l *Lock) Release(ctx context.Context) error {
	if l.heartbeat != nil {
		l.heartbeat()
		l.heartbeat = nil
	}
	if l.Conn.Grammar.Name() == "postgres" {
		_, err := l.Conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey(l.HolderID))
		return err
	}
	g := l.Conn.Grammar
	q := fmt.Sprintf(`DELETE FROM %s WHERE holder = %s`, g.Quote(l.Table), g.Placeholder(1))
	_, err := l.Conn.ExecContext(ctx, q, l.HolderID)
	return err
}

func (l *Lock) acquirePgAdvisory(ctx context.Context) error {
	_, err := l.Conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryKey(l.HolderID))
	return err
}

func (l *Lock) acquireRow(ctx context.Context, timeout time.Duration) error {
	if err := l.ensureTable(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if timeout == 0 {
		deadline = time.Now().Add(30 * time.Second)
	}
	g := l.Conn.Grammar
	insert := fmt.Sprintf(`INSERT INTO %s (holder, acquired_at) VALUES (%s, %s)`,
		g.Quote(l.Table), g.Placeholder(1), g.Placeholder(2))
	for {
		_, err := l.Conn.ExecContext(ctx, insert, l.HolderID, time.Now().UTC())
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("migrations: lock acquisition timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (l *Lock) ensureTable(ctx context.Context) error {
	g := l.Conn.Grammar
	stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		holder      VARCHAR(64) NOT NULL,
		acquired_at %s NOT NULL,
		PRIMARY KEY (holder)
	)`, g.Quote(l.Table), g.CompileType("timestamp", database.ColumnTypeOptions{}))
	_, err := l.Conn.ExecContext(ctx, stmt)
	return err
}

// advisoryKey deterministically maps a string holder to an int64 lock key.
func advisoryKey(holder string) int64 {
	const fnvOffset = 1469598103934665603
	const fnvPrime = 1099511628211
	var h uint64 = fnvOffset
	for i := 0; i < len(holder); i++ {
		h ^= uint64(holder[i])
		h *= fnvPrime
	}
	return int64(h)
}
