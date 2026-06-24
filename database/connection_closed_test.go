package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
)

func openMem(t *testing.T) (*database.Manager, *database.Connection) {
	t.Helper()
	mgr := database.NewManager()
	conn, err := mgr.Open("default", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	require.NoError(t, err)
	return mgr, conn
}

func TestConnection_ClosedRejectsExecQuery(t *testing.T) {
	// Regression: exec/query/transaction must surface a clear "connection
	// closed" error after Close, not blindly forward to a closed pool.
	mgr, conn := openMem(t)
	_ = mgr // closed below via conn.Close()

	require.NoError(t, conn.Close())

	ctx := context.Background()

	_, err := conn.ExecContext(ctx, "SELECT 1")
	assert.ErrorIs(t, err, database.ErrClosed)

	_, err = conn.QueryContext(ctx, "SELECT 1")
	assert.ErrorIs(t, err, database.ErrClosed)

	_, err = conn.PrepareContext(ctx, "SELECT 1")
	assert.ErrorIs(t, err, database.ErrClosed)

	err = conn.Transaction(ctx, func(tx *database.Tx) error { return nil })
	assert.ErrorIs(t, err, database.ErrClosed)
}

func TestConnection_OpenWorksBeforeClose(t *testing.T) {
	mgr, conn := openMem(t)
	defer mgr.Close()
	_, err := conn.ExecContext(context.Background(), "SELECT 1")
	require.NoError(t, err)
}

func TestManager_UseClosesPreviousConnection(t *testing.T) {
	// Regression: Use() replacing a registered connection must close the
	// previous one so its pool is not leaked.
	mgr := database.NewManager()
	first, err := mgr.Open("db", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	require.NoError(t, err)

	second, err := mgr.Open("other", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	require.NoError(t, err)
	defer mgr.Close()

	// Re-register "db" with a different connection; the first must be closed.
	mgr.Use("db", second)

	_, err = first.ExecContext(context.Background(), "SELECT 1")
	assert.ErrorIs(t, err, database.ErrClosed)

	got, err := mgr.Connection("db")
	require.NoError(t, err)
	assert.Same(t, second, got)

	// Re-registering the same connection under its name must be a no-op
	// (not close itself).
	mgr.Use("db", second)
	_, err = second.ExecContext(context.Background(), "SELECT 1")
	require.NoError(t, err)
}

func TestConnection_ErrClosedIsSentinel(t *testing.T) {
	assert.True(t, errors.Is(database.ErrClosed, database.ErrClosed))
}
