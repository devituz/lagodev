package migrations_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/migrations"
)

// The Postgres advisory lock was taken and released through the pool, i.e.
// possibly on two different sessions: the unlock became a no-op and the lock
// stayed held by an idle pooled connection, so the next migrator blocked
// forever. Runs only when LAGODEV_TEST_PG_DSN points at a Postgres server.
func TestLock_PostgresReleaseUnlocksHoldingSession(t *testing.T) {
	dsn := os.Getenv("LAGODEV_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("LAGODEV_TEST_PG_DSN not set")
	}
	mgr := database.NewManager()
	conn, err := mgr.Open("pg-lock", database.Config{Driver: "postgres", DSN: dsn})
	require.NoError(t, err)
	defer mgr.Close()
	ctx := context.Background()

	first := migrations.NewLock(conn, "lock-test")
	require.NoError(t, first.Acquire(ctx, time.Second))
	// Occupy the most recently used idle connection so a pooled unlock
	// would run on a different session.
	busy, err := conn.DB.Conn(ctx)
	require.NoError(t, err)
	defer busy.Close()
	require.NoError(t, first.Release(ctx))

	second := migrations.NewLock(conn, "lock-test")
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.NoError(t, second.Acquire(tctx, 2*time.Second), "lock leaked after Release")
	require.NoError(t, second.Release(ctx))

	// While held, a competing acquire honors its timeout instead of hanging.
	holder := migrations.NewLock(conn, "lock-test")
	require.NoError(t, holder.Acquire(ctx, time.Second))
	defer holder.Release(ctx)
	start := time.Now()
	err = migrations.NewLock(conn, "lock-test").Acquire(ctx, 500*time.Millisecond)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)
}
