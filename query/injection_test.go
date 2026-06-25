package query_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/drivers/postgres"
	"github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/query"
)

// injectionPayloads are values an attacker might feed where a bound value is
// expected. None of them must ever appear verbatim inside the compiled SQL —
// they must be carried out-of-band in the args slice.
var injectionPayloads = []string{
	"' OR 1=1 --",
	"'; DROP TABLE users; --",
	"\" OR \"\"=\"",
	"1; DELETE FROM users",
	"admin'--",
	"' UNION SELECT password FROM users --",
	"Ada\x00Lovelace", // embedded NUL
	"O'Brien",         // legitimate apostrophe
	"café — 日本語 — ‮",  // unicode incl. RTL override
	"%' OR '1'='1",
	"\\'; DROP TABLE x; --",
}

// countPlaceholders counts bound-parameter markers for a given grammar. SQLite
// uses "?", Postgres uses "$N". Markers that appear INSIDE a double-quoted
// identifier (e.g. a column literally named "?") are not parameters and are
// skipped — the builder quotes all identifiers with "..." and doubles embedded
// quotes, so we track quote nesting to stay outside identifier text.
func countPlaceholders(sql string, postgresStyle bool) int {
	n := 0
	inIdent := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '"' {
			// A doubled "" inside an identifier is an escaped quote, not a
			// boundary: skip the pair and stay in the same state.
			if inIdent && i+1 < len(sql) && sql[i+1] == '"' {
				i++
				continue
			}
			inIdent = !inIdent
			continue
		}
		if inIdent {
			continue
		}
		if postgresStyle {
			if c == '$' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
				n++
			}
		} else if c == '?' {
			n++
		}
	}
	return n
}

// assertValueBound asserts that the payload was bound as an argument and never
// inlined into the SQL string, for both grammars.
func assertValueBound(t *testing.T, build func(b *query.Builder) *query.Builder, payload string) {
	t.Helper()

	// SQLite (positional "?").
	sb := build(query.New(conn(sqlite.Grammar{}), "users"))
	sqlS, argsS, err := sb.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sqlS, payload,
		"sqlite: payload must not be inlined into SQL: %q", sqlS)
	assert.Contains(t, argsS, payload,
		"sqlite: payload must be carried as a bound arg")
	assert.Equal(t, len(argsS), countPlaceholders(sqlS, false),
		"sqlite: placeholder count must equal args count: %q args=%v", sqlS, argsS)

	// Postgres ("$N").
	pb := build(query.New(conn(postgres.Grammar{}), "users"))
	sqlP, argsP, err := pb.ToSQL()
	require.NoError(t, err)
	assert.NotContains(t, sqlP, payload,
		"postgres: payload must not be inlined into SQL: %q", sqlP)
	assert.Contains(t, argsP, payload,
		"postgres: payload must be carried as a bound arg")
	assert.Equal(t, len(argsP), countPlaceholders(sqlP, true),
		"postgres: placeholder count must equal args count: %q args=%v", sqlP, argsP)
}

// TestInjection_Where: a 3-arg / 2-arg Where value is always bound.
func TestInjection_Where(t *testing.T) {
	for _, p := range injectionPayloads {
		p := p
		t.Run(p, func(t *testing.T) {
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.Where("name", "=", p)
			}, p)
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.Where("name", p) // 2-arg form, op defaults to "="
			}, p)
		})
	}
}

// TestInjection_WhereLike: LIKE operands are bound, not concatenated.
func TestInjection_WhereLike(t *testing.T) {
	for _, p := range injectionPayloads {
		p := p
		t.Run(p, func(t *testing.T) {
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.Where("name", "like", "%"+p+"%")
			}, "%"+p+"%")
		})
	}
}

// TestInjection_WhereIn: every IN element is bound as a placeholder.
func TestInjection_WhereIn(t *testing.T) {
	for _, p := range injectionPayloads {
		p := p
		t.Run(p, func(t *testing.T) {
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.WhereIn("id", []string{"safe", p, "other"})
			}, p)
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.WhereNotIn("id", []string{p})
			}, p)
		})
	}
}

// TestInjection_WhereBetween: lo/hi bounds are bound.
func TestInjection_WhereBetween(t *testing.T) {
	for _, p := range injectionPayloads {
		p := p
		t.Run(p, func(t *testing.T) {
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.WhereBetween("age", p, "z")
			}, p)
		})
	}
}

// TestInjection_Having: HAVING value is bound.
func TestInjection_Having(t *testing.T) {
	for _, p := range injectionPayloads {
		p := p
		t.Run(p, func(t *testing.T) {
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.GroupBy("name").Having("name", "=", p)
			}, p)
		})
	}
}

// TestInjection_NestedWhere: values inside a nested group are still bound.
func TestInjection_NestedWhere(t *testing.T) {
	for _, p := range injectionPayloads {
		p := p
		t.Run(p, func(t *testing.T) {
			assertValueBound(t, func(b *query.Builder) *query.Builder {
				return b.Where(func(q *query.Builder) {
					q.Where("name", "=", p).OrWhere("alias", "=", p)
				})
			}, p)
		})
	}
}

// TestInjection_IdentifierQuoting: a malicious *column identifier* fed to a
// non-raw method cannot break out of its quoted context. The grammar doubles
// embedded double-quotes, so a `"`-based break-out is neutralised: the payload
// ends up as a single quoted (escaped) identifier, never as free SQL.
func TestInjection_IdentifierQuoting(t *testing.T) {
	// Column name attempting to break out of the "..." quoting and inject.
	evil := `name" = name OR "1"="1`
	b := query.New(conn(sqlite.Grammar{}), "users").Where(evil, "=", "x")
	sql, args, err := b.ToSQL()
	require.NoError(t, err)

	// The embedded double-quote must have been doubled ("") inside the
	// identifier, so the injected ` = name OR ` is INSIDE the quotes, inert.
	assert.Contains(t, sql, `""`,
		"embedded quote in identifier must be doubled: %q", sql)
	// Exactly one bound value, one placeholder.
	assert.Equal(t, []any{"x"}, args)
	assert.Equal(t, 1, countPlaceholders(sql, false))
	// The opening identifier quote must immediately follow WHERE — the column
	// is fully wrapped, the payload cannot escape into a second predicate that
	// the grammar didn't intend.
	assert.True(t, strings.HasPrefix(sql, `SELECT * FROM "users" WHERE "name`),
		"column must be quoted: %q", sql)
}

// TestInjection_TableQuoting: a malicious table name is quoted/escaped.
func TestInjection_TableQuoting(t *testing.T) {
	evil := `users"; DROP TABLE x; --`
	b := query.New(conn(sqlite.Grammar{}), evil)
	sql, _, err := b.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, `""`, "embedded quote in table must be doubled: %q", sql)
	assert.NotContains(t, sql, `DROP TABLE x; --"`+" ",
		"table payload must stay inside quotes: %q", sql)
}

// TestInjection_OperatorWhitelist: the comparison operator is rendered into a
// SQL keyword position, so a caller-supplied operator must be whitelisted. An
// injection payload passed as the operator must be rejected (panic), never
// concatenated into the SQL. Found by FuzzBuilder (op="?" leaked into the
// query as a bare token, also breaking the placeholder/args invariant).
func TestInjection_OperatorWhitelist(t *testing.T) {
	evilOps := []string{
		"?",
		"= 1 OR 1=1 --",
		"; DROP TABLE users; --",
		"=)",
		"IN (SELECT 1)",
		"",
	}
	for _, op := range evilOps {
		op := op
		t.Run(op, func(t *testing.T) {
			assert.Panics(t, func() {
				query.New(conn(sqlite.Grammar{}), "users").Where("id", op, 1)
			}, "operator %q must be rejected", op)
			assert.Panics(t, func() {
				query.New(conn(sqlite.Grammar{}), "users").
					GroupBy("id").Having("id", op, 1)
			}, "Having operator %q must be rejected", op)
		})
	}
}

// TestInjection_OperatorWhitelist_Accepts: all supported operators (any case)
// are accepted and produce exactly one bound placeholder.
func TestInjection_OperatorWhitelist_Accepts(t *testing.T) {
	for _, op := range []string{"=", "<>", "!=", "<", "<=", ">", ">=", "LIKE", "like", "ILIKE", "  =  "} {
		op := op
		t.Run(op, func(t *testing.T) {
			b := query.New(conn(sqlite.Grammar{}), "users").Where("name", op, "x")
			sql, args, err := b.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, []any{"x"}, args)
			assert.Equal(t, 1, countPlaceholders(sql, false))
		})
	}
}

// TestWhereRaw_IsIntentionallyRaw documents the contract: WhereRaw emits the
// expression verbatim (the caller owns escaping) but its *args* are still
// bound. This is by design — WhereRaw is the explicit escape hatch.
func TestWhereRaw_IsIntentionallyRaw(t *testing.T) {
	b := query.New(conn(sqlite.Grammar{}), "users").
		WhereRaw("LOWER(name) = ?", "ada")
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	// Raw expression passes through verbatim...
	assert.Contains(t, sql, "LOWER(name) = ?")
	// ...but the value is still a bound arg, not inlined.
	assert.Equal(t, []any{"ada"}, args)
	assert.Equal(t, 1, countPlaceholders(sql, false))
}
