package database

import (
	"context"
	"database/sql"
	"errors"
)

// Tx wraps *sql.Tx with the parent Connection, satisfying Executor.
type Tx struct {
	*sql.Tx
	conn *Connection
}

// Connection returns the parent connection (provides Grammar etc.).
func (t *Tx) Connection() *Connection { return t.conn }

// Savepoint creates a SQL savepoint within this transaction.
func (t *Tx) Savepoint(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("database: savepoint name required")
	}
	_, err := t.Tx.ExecContext(ctx, "SAVEPOINT "+name)
	return err
}

// RollbackTo rolls back to a previously created savepoint.
func (t *Tx) RollbackTo(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("database: savepoint name required")
	}
	_, err := t.Tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name)
	return err
}
