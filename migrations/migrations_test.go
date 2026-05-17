package migrations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/schema"
)

// migrationsTest builds an isolated registry with three migrations, opens
// an in-memory SQLite and returns both. Tests in this file own the registry
// (no global side-effects).
func setup(t *testing.T) (*database.Connection, *migrations.Registry, func()) {
	t.Helper()
	mgr := database.NewManager()
	conn, err := mgr.Open("test-"+t.Name(), database.Config{
		Driver: "sqlite",
		DSN:    "file:" + t.Name() + "?mode=memory&cache=shared",
	})
	require.NoError(t, err)

	reg := migrations.NewRegistry()
	reg.Register(migrations.Define("00001_users",
		func(c *migrations.Context) error {
			return c.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.Timestamps()
			}))
		},
		func(c *migrations.Context) error {
			return c.Schema(schema.DropIfExists("users"))
		},
	))
	reg.Register(migrations.Define("00002_posts",
		func(c *migrations.Context) error {
			return c.Schema(schema.Create("posts", func(t *schema.Blueprint) {
				t.ID()
				t.String("title")
			}))
		},
		func(c *migrations.Context) error {
			return c.Schema(schema.DropIfExists("posts"))
		},
	))
	return conn, reg, func() { _ = mgr.Close() }
}

func TestUp_Idempotent(t *testing.T) {
	conn, reg, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	m := migrations.New(conn, reg, migrations.Options{})
	applied, err := m.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"00001_users", "00002_posts"}, applied)

	// Second run is a no-op.
	applied, err = m.Up(ctx)
	require.NoError(t, err)
	assert.Empty(t, applied)
}

func TestRollback_LastBatch(t *testing.T) {
	conn, reg, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	m := migrations.New(conn, reg, migrations.Options{})
	_, err := m.Up(ctx)
	require.NoError(t, err)

	rolled, err := m.Rollback(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"00002_posts", "00001_users"}, rolled)

	// After full rollback Up runs again.
	applied, err := m.Up(ctx)
	require.NoError(t, err)
	assert.Len(t, applied, 2)
}

func TestRollback_StepLimit(t *testing.T) {
	conn, reg, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	m := migrations.New(conn, reg, migrations.Options{})
	_, err := m.Up(ctx)
	require.NoError(t, err)

	rolled, err := m.Rollback(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"00002_posts"}, rolled)
}

func TestStatus(t *testing.T) {
	conn, reg, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	m := migrations.New(conn, reg, migrations.Options{})
	_, err := m.Step(ctx, 1)
	require.NoError(t, err)
	rows, err := m.Status(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Applied, "first migration should be applied")
	assert.False(t, rows[1].Applied, "second migration should be pending")
}

func TestFresh_DropsEverything(t *testing.T) {
	conn, reg, cleanup := setup(t)
	defer cleanup()
	ctx := context.Background()

	m := migrations.New(conn, reg, migrations.Options{})
	_, err := m.Up(ctx)
	require.NoError(t, err)
	applied, err := m.Fresh(ctx)
	require.NoError(t, err)
	assert.Len(t, applied, 2)
}

func TestChecksum_Stable(t *testing.T) {
	conn, reg, cleanup := setup(t)
	defer cleanup()

	mig, _ := reg.Get("00001_users")
	a, err := migrations.Checksum(conn, mig)
	require.NoError(t, err)
	b, err := migrations.Checksum(conn, mig)
	require.NoError(t, err)
	assert.Equal(t, a, b, "checksum must be deterministic")
}
