package query_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/drivers/mysql"
	"github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/query"
	lagotest "github.com/devituz/lagodev/testing"
)

// OFFSET without LIMIT is a syntax error on SQLite and MySQL; each dialect
// must emit its "no limit" form.
func TestSweep_OffsetWithoutLimit(t *testing.T) {
	cases := map[string]struct {
		b    *query.Builder
		want string
	}{
		"sqlite":   {query.New(conn(sqlite.Grammar{}), "users").Offset(5), ` LIMIT -1 OFFSET 5`},
		"mysql":    {query.New(conn(mysql.Grammar{}), "users").Offset(5), ` LIMIT 18446744073709551615 OFFSET 5`},
		"postgres": {query.New(conn(postgres.Grammar{}), "users").Offset(5), `"users" OFFSET 5`},
	}
	for name, tc := range cases {
		sql, _, err := tc.b.ToSQL()
		require.NoError(t, err, name)
		assert.Contains(t, sql, tc.want, name)
	}

	c, cleanup := lagotest.SQLite(t, lagotest.WithRegistry(countRegistry))
	defer cleanup()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := query.New(c, "sales").Insert(ctx, map[string]any{"region": "x", "amount": i})
		require.NoError(t, err)
	}
	rows, err := query.New(c, "sales").OrderBy("id", "asc").Offset(1).Get(ctx)
	require.NoError(t, err)
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 2, n)
}

// WhereIn with a slice type outside the hard-coded list used to bind the
// whole slice as one argument, which every driver rejects.
func TestSweep_WhereInArbitrarySlice(t *testing.T) {
	type myID int32
	sql, args, err := query.New(conn(postgres.Grammar{}), "users").
		WhereIn("id", []uint{1, 2}).
		WhereNotIn("kind", []myID{7}).
		ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, `"id" IN ($1, $2) AND "kind" NOT IN ($3)`)
	assert.Equal(t, []any{uint(1), uint(2), myID(7)}, args)
}

// An empty nested group compiled to "()" — invalid SQL.
func TestSweep_EmptyNestedGroupSkipped(t *testing.T) {
	sql, _, err := query.New(conn(sqlite.Grammar{}), "users").
		Where("a", 1).
		Where(func(q *query.Builder) {}).
		ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "()")
	assert.Contains(t, sql, `WHERE "a" = ?`)
}

// Join args occupy the first positional slots, so Postgres WHERE
// placeholders must be numbered after them.
func TestSweep_JoinArgsShiftPostgresPlaceholders(t *testing.T) {
	sql, args, err := query.New(conn(postgres.Grammar{}), "users").
		Join("posts", `posts.user_id = users.id AND posts.kind = $1`, "news").
		Where("users.active", true).
		ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, `WHERE "users"."active" = $2`)
	assert.Equal(t, []any{"news", true}, args)
}

// WrapWheres groups OR-ed conditions so a later AND binds to the whole
// filter; without OR the SQL is unchanged.
func TestSweep_WrapWheres(t *testing.T) {
	sql, _, err := query.New(conn(sqlite.Grammar{}), "users").
		Where("a", 1).OrWhere("b", 2).WrapWheres().WhereNull("deleted_at").
		ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, `WHERE ("a" = ? OR "b" = ?) AND "deleted_at" IS NULL`)

	sql, _, err = query.New(conn(sqlite.Grammar{}), "users").
		Where("a", 1).WrapWheres().WhereNull("deleted_at").
		ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, `WHERE "a" = ? AND "deleted_at" IS NULL`)
}

// DISTINCT Count counted every row ("SELECT DISTINCT COUNT(*)"), and an
// aggregate with OFFSET returned sql.ErrNoRows.
func TestSweep_DistinctCountAndAggregateOffset(t *testing.T) {
	c, cleanup := lagotest.SQLite(t, lagotest.WithRegistry(countRegistry))
	defer cleanup()
	ctx := context.Background()
	for _, r := range []map[string]any{
		{"region": "eu", "amount": 10},
		{"region": "eu", "amount": 20},
		{"region": "us", "amount": 5},
	} {
		_, err := query.New(c, "sales").Insert(ctx, r)
		require.NoError(t, err)
	}

	n, err := query.New(c, "sales").Distinct().Select("region").Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	sum, err := query.New(c, "sales").Limit(1).Offset(1).Sum(ctx, "amount")
	require.NoError(t, err)
	assert.Equal(t, float64(35), sum)
}
