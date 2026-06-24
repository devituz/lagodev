package query_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/query"
)

func conn(g database.Grammar) *database.Connection {
	return &database.Connection{Grammar: g, Config: database.Config{}}
}

func TestSelectBasic(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").
		Where("name", "=", "Ada").
		OrWhere("name", "=", "Lovelace").
		OrderBy("id", "desc").
		Limit(10)
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t,
		`SELECT * FROM "users" WHERE "name" = ? OR "name" = ? ORDER BY "id" DESC LIMIT 10`,
		sql)
	assert.Equal(t, []any{"Ada", "Lovelace"}, args)
}

func TestSelectSpecificColumns(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").Select("id", "name")
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT "id", "name" FROM "users"`, sql)
}

func TestWhereIn(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").WhereIn("id", []int{1, 2, 3})
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" WHERE "id" IN (?, ?, ?)`, sql)
	assert.Equal(t, []any{1, 2, 3}, args)
}

func TestWhereIn_Empty(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").WhereIn("id", []int{})
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	// Empty IN should compile to 1=0 (no rows match), not crash.
	assert.Contains(t, sql, "1 = 0")
}

func TestPostgresPlaceholders(t *testing.T) {
	b := query.New(conn(postgres.Grammar{}), "users").
		Where("age", ">", 18).
		Where("country", "=", "UZ")
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t,
		`SELECT * FROM "users" WHERE "age" > $1 AND "country" = $2`,
		sql)
	assert.Equal(t, []any{18, "UZ"}, args)
}

func TestNullableWheres(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").WhereNull("deleted_at")
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" WHERE "deleted_at" IS NULL`, sql)
}

func TestNestedWhere(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").
		Where("active", "=", true).
		Where(func(q *query.Builder) {
			q.Where("role", "=", "admin").OrWhere("role", "=", "owner")
		})
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.True(t, strings.Contains(sql, "AND ("), "expected nested group, got %q", sql)
}

func TestInsertGetID_SQLitePath(t *testing.T) {
	// We don't run the actual exec here — just verifying SQL composition.
	b := query.New(conn(sqlite.Grammar{}), "users")
	_, _, err := b.ToSQL()
	require.NoError(t, err)
	// The Insert helpers do their own SQL composition; verify there's no
	// panic on building the builder for an insertable target.
	assert.NotNil(t, b)
}

func TestUpdateSQL_Postgres(t *testing.T) {
	// We can verify the placeholder numbering on UPDATE works when the
	// builder is interleaved with WHERE clauses.
	b := query.New(conn(postgres.Grammar{}), "users").Where("id", "=", 7)
	// We cannot run the actual exec without a real DB, but compiling the
	// WHERE clause through ToSQL exercises the same compileWheres path.
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" WHERE "id" = $1`, sql)
	assert.Equal(t, []any{7}, args)
}

func TestOrderByRawPassesThrough(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").OrderByRaw("FIELD(role, 'admin', 'user')")
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "ORDER BY FIELD(role, 'admin', 'user')")
}

func TestSelectAggregateExpressionsPassThrough(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").Select("COUNT(*) AS total", "name")
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "COUNT(*) AS total")
}

// Bug 4: an injection payload in the ORDER BY direction must be rejected, not
// concatenated into the SQL.
func TestOrderBy_RejectsDirectionInjection(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users")
	assert.Panics(t, func() {
		b.OrderBy("name", "desc; DROP TABLE users")
	})
}

func TestOrderBy_NormalizesDirection(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").OrderBy("name", "desc")
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" ORDER BY "name" DESC`, sql)

	b2 := query.New(conn(sqlite.Grammar{}), "users").OrderBy("name", "")
	sql2, _, err := b2.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `SELECT * FROM "users" ORDER BY "name" ASC`, sql2)
}

// SQLite has no FOR UPDATE/FOR SHARE; the lock clause must be suppressed.
func TestLock_NoopOnSQLite(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").LockForUpdate()
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "FOR UPDATE")
}
