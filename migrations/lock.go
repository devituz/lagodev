package migrations

import (
	"context"
	"database/sql"
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

	// pgConn pins the session holding the Postgres advisory lock. Advisory
	// locks are per-session: locking and unlocking through the pool could hit
	// two different sessions, leaving the lock held by an idle pooled
	// connection so the next migrator blocked forever.
	pgConn *sql.Conn
}

// NewLock builds a Lock against conn.
func NewLock(conn *database.Connection, holder string) *Lock {
	return &Lock{Conn: conn, Table: "migrations_lock", HolderID: holder}
}

// Acquire blocks until the lock is held or ctx expires.
func (l *Lock) Acquire(ctx context.Context, timeout time.Duration) error {
	if l.Conn.Grammar.Name() == "postgres" {
		return l.acquirePgAdvisory(ctx, timeout)
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
		if l.pgConn == nil {
			return nil
		}
		c := l.pgConn
		l.pgConn = nil
		_, err := c.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey(l.HolderID))
		if cerr := c.Close(); err == nil {
			err = cerr
		}
		return err
	}
	g := l.Conn.Grammar
	q := fmt.Sprintf(`DELETE FROM %s WHERE holder = %s`, g.Quote(l.Table), g.Placeholder(1))
	_, err := l.Conn.ExecContext(ctx, q, l.HolderID)
	return err
}

// acquirePgAdvisory takes the advisory lock on a dedicated session (see
// pgConn), polling pg_try_advisory_lock so the timeout is honored like the
// row-based lock instead of blocking indefinitely.
func (l *Lock) acquirePgAdvisory(ctx context.Context, timeout time.Duration) error {
	if l.pgConn != nil {
		return nil
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	c, err := l.Conn.DB.Conn(ctx)
	if err != nil {
		return err
	}
	for {
		var ok bool
		if err := c.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", advisoryKey(l.HolderID)).Scan(&ok); err != nil {
			_ = c.Close()
			return err
		}
		if ok {
			l.pgConn = c
			return nil
		}
		if time.Now().After(deadline) {
			_ = c.Close()
			return errors.New("migrations: lock acquisition timed out")
		}
		select {
		case <-ctx.Done():
			_ = c.Close()
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
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
