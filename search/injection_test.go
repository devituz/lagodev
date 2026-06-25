package search

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// These tests treat the Postgres engine as the attack surface: the only safe
// way to feed a tsquery/tsvector engine is to bind the user query text and
// every filter value as parameters ($N) and to never splice them into the SQL
// string. The fakeDB recorder lets us inspect the exact SQL and args the engine
// would hand the driver, so we can assert no raw interpolation survives.

// payloads are strings an attacker would throw at a full-text search box. They
// include SQL-breakout attempts, tsquery metacharacters (which are meaningful
// to to_tsquery and could corrupt a query if inlined), and unicode.
var injectionPayloads = []string{
	`'; DROP TABLE posts; --`,
	`" OR "1"="1`,
	`hello'); DELETE FROM posts WHERE ('1'='1`,
	`:*`,
	`foo:*`,
	`a & b | c ! d`,
	`( ) & | ! <-> :*`,
	`'$1' OR 1=1`,
	`$1; $2`,
	`back\slash`,
	`tab	newline
end`,
	`%s %q %v`,            // fmt verbs must not be interpreted as a format string
	`café & naïve | über`, // unicode + metachars
	`日本語 OR 한국어`,
	`'''''''''`,
	strings.Repeat("' OR 1=1; --", 50),
}

// assertParameterized is the core invariant: the recorded SQL must contain only
// the placeholders the engine generated, and the raw attacker text must never
// appear verbatim in the SQL string. The text belongs in args, bound to $N.
func assertParameterized(t *testing.T, sqlText string, args []any, raw string) {
	t.Helper()

	// The query text the engine sends to the driver is the tokenized form, not
	// the raw input. But under no circumstances may any chunk that could break
	// SQL appear inline. We assert the raw payload is not embedded.
	if raw != "" && strings.Contains(sqlText, raw) {
		t.Fatalf("raw payload interpolated into SQL:\npayload=%q\nsql=%s", raw, sqlText)
	}

	// SQL-breakout primitives must never reach the SQL text. The only quotes
	// allowed are the static ones the builder emits around the trusted config
	// name (e.g. 'english') and the double quotes around identifiers.
	for _, bad := range []string{"DROP TABLE", "DELETE FROM", "OR 1=1", "--", "1=1"} {
		if strings.Contains(sqlText, bad) {
			t.Fatalf("SQL-breakout fragment %q present in SQL: %s", bad, sqlText)
		}
	}

	// The query param ($1) must be present and the user text must be carried in
	// args, not in the SQL.
	if !strings.Contains(sqlText, "$1") {
		t.Fatalf("expected query bound as $1, sql=%s", sqlText)
	}
	if len(args) == 0 {
		t.Fatalf("expected at least the tsquery bound arg, got none; sql=%s", sqlText)
	}
}

func TestPostgresInjectionPlainQuery(t *testing.T) {
	for _, payload := range injectionPayloads {
		db := &fakeDB{queryRows: &fakeRows{}}
		pg := NewPostgres(db, PgConfig{})

		if _, err := pg.Search(context.Background(), "posts", payload, Options{}); err != nil {
			t.Fatalf("payload %q: search error %v", payload, err)
		}

		// Tokenizing may strip every char (e.g. ":*" alone). If so the engine
		// short-circuits without touching the DB, which is also safe.
		if db.lastQuery == "" {
			continue
		}
		assertParameterized(t, db.lastQuery, db.lastArgs, payload)

		// arg[0] is the bound tsquery text; it must be a string (plainto_tsquery
		// will parse it as plain text — metachars are literalized by PG, not us).
		if _, ok := db.lastArgs[0].(string); !ok {
			t.Fatalf("payload %q: tsquery arg not a string: %#v", payload, db.lastArgs[0])
		}
	}
}

func TestPostgresInjectionPrefixQuery(t *testing.T) {
	// Prefix mode uses to_tsquery, which DOES interpret operators. The danger is
	// twofold: the value is still bound ($1, so no SQL injection), and the
	// builder appends ":*" per lexeme. Metacharacters in the payload are folded
	// away by Tokenize before they ever reach the tsquery string, so an attacker
	// cannot inject to_tsquery operators that would error or alter semantics.
	for _, payload := range injectionPayloads {
		db := &fakeDB{queryRows: &fakeRows{}}
		pg := NewPostgres(db, PgConfig{})

		if _, err := pg.Search(context.Background(), "posts", payload, Options{Prefix: true}); err != nil {
			t.Fatalf("payload %q: prefix search error %v", payload, err)
		}
		if db.lastQuery == "" {
			continue
		}
		assertParameterized(t, db.lastQuery, db.lastArgs, payload)

		tsq, ok := db.lastArgs[0].(string)
		if !ok {
			t.Fatalf("payload %q: prefix tsquery arg not a string", payload)
		}
		// The bound to_tsquery text is built only from tokenized lexemes joined
		// by " & " and suffixed with ":*". No tsquery control char other than the
		// builder's own "&" and ":*" may appear — Tokenize drops the rest.
		for _, lex := range strings.Split(tsq, " & ") {
			if lex == "" {
				continue
			}
			body := strings.TrimSuffix(lex, ":*")
			if strings.ContainsAny(body, "&|!()<>:* '\"\\;") {
				t.Fatalf("payload %q: tsquery lexeme %q carries control chars; tsq=%q",
					payload, body, tsq)
			}
		}
	}
}

func TestPostgresInjectionFilterValues(t *testing.T) {
	// Filter VALUES are pure user data and must be bound, never inlined, even
	// when they carry SQL metacharacters.
	for _, payload := range injectionPayloads {
		db := &fakeDB{queryRows: &fakeRows{}}
		pg := NewPostgres(db, PgConfig{})

		_, err := pg.Search(context.Background(), "posts", "hello", Options{
			Filters: map[string]any{"lang": payload},
		})
		if err != nil {
			t.Fatalf("payload %q: search error %v", payload, err)
		}

		// The filter predicate must reference a placeholder, not the value.
		if !strings.Contains(db.lastQuery, `"lang" = $`) {
			t.Fatalf("payload %q: filter not parameterized: %s", payload, db.lastQuery)
		}
		if strings.Contains(db.lastQuery, payload) {
			t.Fatalf("payload %q: filter value interpolated into SQL: %s", payload, db.lastQuery)
		}
		// The payload must be present in args verbatim — proving it was bound.
		var found bool
		for _, a := range db.lastArgs {
			if s, ok := a.(string); ok && s == payload {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("payload %q: filter value not bound in args: %#v", payload, db.lastArgs)
		}
	}
}

func TestPostgresInjectionDeleteIDs(t *testing.T) {
	// Delete IDs are user-controlled; each must become a bound $N, never inlined.
	ids := append([]string{}, injectionPayloads...)
	db := &fakeDB{}
	pg := NewPostgres(db, PgConfig{})

	if err := pg.Delete(context.Background(), "posts", ids...); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Every id must be a placeholder in the IN list and bound in args.
	for i := range ids {
		ph := "$" + strconv.Itoa(i+1)
		if !strings.Contains(db.lastQuery, ph) {
			t.Fatalf("delete missing placeholder %s: %s", ph, db.lastQuery)
		}
	}
	for _, p := range ids {
		if strings.Contains(db.lastQuery, p) {
			t.Fatalf("delete inlined id %q into SQL: %s", p, db.lastQuery)
		}
	}
	if len(db.lastArgs) != len(ids) {
		t.Fatalf("delete arg count = %d, want %d", len(db.lastArgs), len(ids))
	}
}

func TestPostgresInjectionIndexAndColumnIdentifiers(t *testing.T) {
	// Index/column names come from configuration (trusted), but the engine still
	// must quote them so a name containing a double-quote cannot break out of the
	// identifier. Confirm quoteIdent doubles embedded quotes.
	evil := `posts" ; DROP TABLE x; --`
	db := &fakeDB{queryRows: &fakeRows{}}
	pg := NewPostgres(db, PgConfig{IDColumn: `id" --`})

	_, _ = pg.Search(context.Background(), evil, "hello", Options{})

	// The closing quote of the identifier must be the escaped form; the raw
	// single closing quote followed by SQL must not appear unescaped.
	if strings.Contains(db.lastQuery, `DROP TABLE x`) && !strings.Contains(db.lastQuery, `""`) {
		t.Fatalf("identifier breakout not escaped: %s", db.lastQuery)
	}
	// quoteIdent doubles embedded double-quotes: the index name must render with
	// "" inside it.
	if !strings.Contains(db.lastQuery, `"posts"" ; DROP TABLE x; --"`) {
		t.Fatalf("index identifier not safely quoted: %s", db.lastQuery)
	}
}

func TestQuoteIdentEscaping(t *testing.T) {
	cases := map[string]string{
		`id`:        `"id"`,
		`a"b`:       `"a""b"`,
		`"`:         `""""`,
		`x"";y`:     `"x"""";y"`,
		`tbl";DROP`: `"tbl"";DROP"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQuoteLiteralEscaping(t *testing.T) {
	cases := map[string]string{
		`english`:    `'english'`,
		`o'brien`:    `'o''brien'`,
		`' OR 1=1--`: `''' OR 1=1--'`,
	}
	for in, want := range cases {
		if got := quoteLiteral(in); got != want {
			t.Errorf("quoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}
