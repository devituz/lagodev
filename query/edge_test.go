package query_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/query"
)

// TestEdge_WhereInEmpty: empty IN/NOT IN compile to constant predicates with
// no bound args, never to invalid "IN ()".
func TestEdge_WhereInEmpty(t *testing.T) {
	in := query.New(conn(sqlite.Grammar{}), "users").WhereIn("id", []int{})
	sql, args, err := in.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "1 = 0")
	assert.NotContains(t, sql, "IN ()")
	assert.Empty(t, args)

	notIn := query.New(conn(sqlite.Grammar{}), "users").WhereNotIn("id", []int{})
	sql, args, err = notIn.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "1 = 1")
	assert.Empty(t, args)
}

// TestEdge_WhereInNilValue: a nil element is still bound as a placeholder arg.
func TestEdge_WhereInNilValue(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").WhereIn("id", []any{nil, 1, nil})
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(sql, "?"))
	require.Len(t, args, 3)
	assert.Nil(t, args[0])
	assert.Nil(t, args[2])
}

// TestEdge_WhereNilValue: a nil value in a plain Where binds as a nil arg
// (not inlined as the literal text "NULL"/"<nil>").
func TestEdge_WhereNilValue(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").Where("deleted_at", "=", nil)
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(sql, "?"))
	require.Len(t, args, 1)
	assert.Nil(t, args[0])
	assert.NotContains(t, sql, "<nil>")
}

// TestEdge_LongInList: a very long IN list keeps placeholder/args count in
// lock-step and numbers Postgres placeholders sequentially.
func TestEdge_LongInList(t *testing.T) {
	const n = 5000
	vals := make([]int, n)
	for i := range vals {
		vals[i] = i
	}

	bs := query.New(conn(sqlite.Grammar{}), "users").WhereIn("id", vals)
	sqlS, argsS, err := bs.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, n, strings.Count(sqlS, "?"))
	assert.Len(t, argsS, n)

	bp := query.New(conn(postgres.Grammar{}), "users").WhereIn("id", vals)
	sqlP, argsP, err := bp.ToSQL()
	require.NoError(t, err)
	assert.Len(t, argsP, n)
	// Highest placeholder must be $n and $1 must be present.
	assert.Contains(t, sqlP, "$1,")
	assert.Contains(t, sqlP, "$"+strconv.Itoa(n)+")")
}

// TestEdge_CloneIndependence: mutating a clone must not affect the original.
func TestEdge_CloneIndependence(t *testing.T) {
	base := query.New(conn(sqlite.Grammar{}), "users").Where("active", "=", true)

	clone := base.Clone().Where("role", "=", "admin").Limit(5).OrderBy("id", "desc")

	baseSQL, baseArgs, err := base.ToSQL()
	require.NoError(t, err)
	cloneSQL, cloneArgs, err := clone.ToSQL()
	require.NoError(t, err)

	// Original must be untouched.
	assert.Equal(t, `SELECT * FROM "users" WHERE "active" = ?`, baseSQL)
	assert.Equal(t, []any{true}, baseArgs)

	// Clone carries the extra clauses.
	assert.Contains(t, cloneSQL, `"role" = ?`)
	assert.Contains(t, cloneSQL, "LIMIT 5")
	assert.Contains(t, cloneSQL, "ORDER BY")
	assert.Equal(t, []any{true, "admin"}, cloneArgs)
}

// TestEdge_CloneSliceAliasing: appending to the clone's where slice must not
// corrupt the original's backing array.
func TestEdge_CloneSliceAliasing(t *testing.T) {
	base := query.New(conn(sqlite.Grammar{}), "users").
		Where("a", "=", 1).
		Where("b", "=", 2)

	clone := base.Clone()
	for i := 0; i < 50; i++ {
		clone.Where("c", "=", i)
	}

	baseSQL, baseArgs, err := base.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, []any{1, 2}, baseArgs)
	assert.Equal(t, 2, strings.Count(baseSQL, "?"))
}

// TestEdge_OffsetLimitBounds: zero/negative limit and offset are suppressed;
// positive values are emitted.
func TestEdge_OffsetLimitBounds(t *testing.T) {
	// Zero => no LIMIT/OFFSET.
	b := query.New(conn(sqlite.Grammar{}), "users").Limit(0).Offset(0)
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "LIMIT")
	assert.NotContains(t, sql, "OFFSET")

	// Negative => also suppressed (guarded by > 0).
	b = query.New(conn(sqlite.Grammar{}), "users").Limit(-10).Offset(-5)
	sql, _, err = b.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sql, "LIMIT")
	assert.NotContains(t, sql, "OFFSET")

	// Positive => emitted.
	b = query.New(conn(sqlite.Grammar{}), "users").Limit(25).Offset(50)
	sql, _, err = b.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 25")
	assert.Contains(t, sql, "OFFSET 50")
}

// TestEdge_NoTable: ToSQL on a builder with no table returns an error, no panic.
func TestEdge_NoTable(t *testing.T) {
	nb := query.New(conn(sqlite.Grammar{}), "")
	_, _, err := nb.ToSQL()
	require.Error(t, err)
}
