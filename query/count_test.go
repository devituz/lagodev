package query_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/query"
	"github.com/devituz/lagodev/schema"
	lagotest "github.com/devituz/lagodev/testing"
)

var countRegistry = migrations.NewRegistry()

func init() {
	countRegistry.Register(migrations.Define("00001_sales",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("sales", func(t *schema.Blueprint) {
				t.ID()
				t.String("region")
				t.Integer("amount").Default(0)
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("sales"))
		},
	))
}

// Bug 5: Count() with a GROUP BY must return the number of groups, not the
// first group's row count.
func TestCount_WithGroupBy(t *testing.T) {
	conn, cleanup := lagotest.SQLite(t, lagotest.WithRegistry(countRegistry))
	defer cleanup()
	ctx := context.Background()

	rows := []map[string]any{
		{"region": "north", "amount": 1},
		{"region": "north", "amount": 2},
		{"region": "south", "amount": 3},
		{"region": "east", "amount": 4},
		{"region": "east", "amount": 5},
	}
	for _, r := range rows {
		_, err := query.New(conn, "sales").Insert(ctx, r)
		require.NoError(t, err)
	}

	// Three distinct regions => three groups.
	n, err := query.New(conn, "sales").GroupBy("region").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), n)

	// Without grouping, Count is the total row count.
	total, err := query.New(conn, "sales").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
}
