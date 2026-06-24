package search

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// querier is the minimal subset of *sql.DB the Postgres engine needs. Both
// *sql.DB and *sql.Tx satisfy it, and tests can supply a fake recorder, so the
// SQL generation is exercised without a live server.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (result, error)
}

// rows is the minimal cursor contract the engine consumes from QueryContext. It
// mirrors the *sql.Rows methods used here so a fake can stand in for a driver.
type rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// result mirrors the part of sql.Result the engine needs. ExecContext is used
// only by Delete, where the affected-row count is ignored.
type result interface {
	RowsAffected() (int64, error)
}

// PgConfig customizes the SQL the Postgres engine emits. The zero value is
// valid and targets a conventional layout: one physical table per index, with a
// text "id" column, a generated/maintained tsvector "search" column, and a
// JSON/JSONB "fields" column scanned back into Hit.Fields.
type PgConfig struct {
	// IDColumn is the primary-key column scanned into Hit.ID. Default: "id".
	IDColumn string
	// VectorColumn is the tsvector column matched against the tsquery and fed
	// to ts_rank for scoring. Default: "search".
	VectorColumn string
	// FieldsColumn, when non-empty, is selected and scanned into Hit.Fields via
	// a sql.Scanner-compatible destination supplied by the caller's driver.
	// Default: "" (Hit.Fields left nil).
	FieldsColumn string
	// Config is the Postgres text-search configuration passed to the tsquery/
	// tsvector functions (e.g. "english", "simple"). Default: "english".
	Config string
	// WebSearch selects websearch_to_tsquery instead of plainto_tsquery. It is
	// ignored when Prefix matching is requested, since websearch syntax does
	// not compose with the ":*" prefix operator.
	WebSearch bool
}

func (c PgConfig) withDefaults() PgConfig {
	if c.IDColumn == "" {
		c.IDColumn = "id"
	}
	if c.VectorColumn == "" {
		c.VectorColumn = "search"
	}
	if c.Config == "" {
		c.Config = "english"
	}
	return c
}

// Postgres is a full-text search Engine backed by database/sql and Postgres'
// tsvector/tsquery machinery. It compiles parameterized SELECTs that rank rows
// with ts_rank and paginate with LIMIT/OFFSET. The engine never interpolates
// user input into SQL text: index names and column identifiers come from
// configuration, while the query and filter values are always bound parameters.
type Postgres struct {
	db  querier
	cfg PgConfig
}

// NewPostgres constructs a Postgres engine over the given querier (typically a
// *sql.DB wrapped via WrapDB) using cfg, applying defaults for unset fields.
func NewPostgres(db querier, cfg PgConfig) *Postgres {
	return &Postgres{db: db, cfg: cfg.withDefaults()}
}

// Index is a no-op for the Postgres engine: documents live in the underlying
// table and are populated by the application's normal writes (the tsvector
// column is expected to be generated or trigger-maintained). It satisfies the
// Engine interface so a Postgres-backed app can share search-agnostic code.
func (p *Postgres) Index(_ context.Context, _ string, _ ...Document) error {
	return nil
}

// Delete removes rows by ID from the index table. It emits a single
// parameterized DELETE ... WHERE id IN ($1, $2, ...).
func (p *Postgres) Delete(ctx context.Context, index string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	table := quoteIdent(index)
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = id
	}
	sqlText := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
		table, quoteIdent(p.cfg.IDColumn), strings.Join(placeholders, ", "))
	if _, err := p.db.ExecContext(ctx, sqlText, args...); err != nil {
		return fmt.Errorf("search: postgres delete: %w", err)
	}
	return nil
}

// Search compiles and runs a tsquery search against the index table, ordering
// by ts_rank descending and paginating with LIMIT/OFFSET. A blank query (no
// lexemes after tokenizing) short-circuits to an empty result without a round
// trip. Equality Filters become additional WHERE predicates with bound values.
func (p *Postgres) Search(ctx context.Context, index, query string, opts Options) (Results, error) {
	lexemes := Tokenize(query)
	if len(lexemes) == 0 {
		return Results{}, nil
	}

	sqlText, args := p.buildSearchSQL(index, lexemes, opts)

	rs, err := p.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return Results{}, fmt.Errorf("search: postgres query: %w", err)
	}
	defer rs.Close()

	hits, total, err := p.scanHits(rs)
	if err != nil {
		return Results{}, err
	}
	return Results{Hits: hits, Total: total}, nil
}

// buildSearchSQL compiles the parameterized SELECT and its bound arguments. The
// argument order is deterministic: the tsquery text first, then each filter
// value in sorted-key order, then LIMIT and OFFSET. A window-function COUNT(*)
// OVER () yields the pre-pagination total in the same round trip.
func (p *Postgres) buildSearchSQL(index string, lexemes []string, opts Options) (string, []any) {
	var args []any

	tsquery := p.tsqueryExpr(lexemes, opts.Prefix)
	args = append(args, tsquery)
	queryParam := "$1"

	vec := quoteIdent(p.cfg.VectorColumn)
	tsq := fmt.Sprintf("%s(%s, %s)", p.tsqueryFn(opts.Prefix), quoteLiteral(p.cfg.Config), queryParam)

	cols := []string{
		quoteIdent(p.cfg.IDColumn),
		fmt.Sprintf("ts_rank(%s, %s) AS rank", vec, tsq),
	}
	if p.cfg.FieldsColumn != "" {
		cols = append(cols, quoteIdent(p.cfg.FieldsColumn))
	}
	cols = append(cols, "count(*) OVER () AS total")

	where := []string{fmt.Sprintf("%s @@ %s", vec, tsq)}

	// Deterministic filter ordering keeps generated SQL/args stable for tests.
	keys := make([]string, 0, len(opts.Filters))
	for k := range opts.Filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, opts.Filters[k])
		where = append(where, fmt.Sprintf("%s = $%d", quoteIdent(k), len(args)))
	}

	page, perPage := opts.Page, opts.PerPage
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	args = append(args, perPage)
	limitParam := len(args)
	args = append(args, (page-1)*perPage)
	offsetParam := len(args)

	sqlText := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY rank DESC, %s ASC LIMIT $%d OFFSET $%d",
		strings.Join(cols, ", "),
		quoteIdent(index),
		strings.Join(where, " AND "),
		quoteIdent(p.cfg.IDColumn),
		limitParam,
		offsetParam,
	)
	return sqlText, args
}

// tsqueryFn picks the query-compilation function. Prefix matching needs the
// raw "&"-joined lexemes with ":*", which only to_tsquery accepts. Otherwise
// websearch_to_tsquery or plainto_tsquery handles user phrasing safely.
func (p *Postgres) tsqueryFn(prefix bool) string {
	if prefix {
		return "to_tsquery"
	}
	if p.cfg.WebSearch {
		return "websearch_to_tsquery"
	}
	return "plainto_tsquery"
}

// tsqueryExpr builds the bound query text. For prefix matching the tokens are
// AND-joined with the ":*" prefix operator for to_tsquery; otherwise the raw
// query terms are passed through for plainto_/websearch_to_tsquery, which parse
// them as plain text. Either way the value is a bound parameter, never inlined.
func (p *Postgres) tsqueryExpr(lexemes []string, prefix bool) string {
	if prefix {
		parts := make([]string, len(lexemes))
		for i, l := range lexemes {
			parts[i] = l + ":*"
		}
		return strings.Join(parts, " & ")
	}
	return strings.Join(lexemes, " ")
}

// scanHits drains the cursor into Hits. Each row carries a window-function
// total, so the first row sets Results.Total; rank maps to Hit.Score. When a
// FieldsColumn is configured it is scanned into Hit.Fields via a fieldsScanner
// that the driver fills (JSON/JSONB → map).
func (p *Postgres) scanHits(rs rows) ([]Hit, int, error) {
	var (
		hits  []Hit
		total int
	)
	for rs.Next() {
		var (
			id     string
			rank   float64
			fields fieldsScanner
			t      int
		)
		dest := []any{&id, &rank}
		if p.cfg.FieldsColumn != "" {
			dest = append(dest, &fields)
		}
		dest = append(dest, &t)
		if err := rs.Scan(dest...); err != nil {
			return nil, 0, fmt.Errorf("search: postgres scan: %w", err)
		}
		total = t
		hits = append(hits, Hit{ID: id, Score: rank, Fields: fields.m})
	}
	if err := rs.Err(); err != nil {
		return nil, 0, fmt.Errorf("search: postgres rows: %w", err)
	}
	return hits, total, nil
}

// fieldsScanner is a sql.Scanner that decodes a JSON/JSONB fields column into a
// map. It accepts the []byte or string forms a driver may hand back; a NULL
// leaves the map nil.
type fieldsScanner struct {
	m map[string]any
}

// Scan implements sql.Scanner. It tolerates the common representations Postgres
// drivers return for json/jsonb: []byte, string, or already-decoded map.
func (f *fieldsScanner) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		return nil
	case map[string]any:
		f.m = v
		return nil
	case []byte:
		return decodeJSONFields(v, &f.m)
	case string:
		return decodeJSONFields([]byte(v), &f.m)
	default:
		return fmt.Errorf("search: cannot scan fields column from %T", src)
	}
}

// quoteIdent wraps a SQL identifier in double quotes, escaping embedded quotes,
// so configured table/column names are safe even if they collide with keywords.
func quoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// quoteLiteral wraps a SQL string literal in single quotes, escaping embedded
// quotes. Used only for the trusted, non-user text-search config name.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
