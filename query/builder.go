// Package query provides a fluent, driver-aware SQL builder. It is used by
// the ORM but is fully usable on its own — it returns *sql.Rows where the
// caller wants raw access.
//
// Chaining methods (Where, OrderBy, Limit, ...) mutate and return the
// receiver, so a Builder is single-use: build it, execute once, discard. The
// terminal aggregate helpers (Count, Exists, Sum, ...) are the exception —
// they operate on an internal clone so they never disturb the receiver's
// clause state and may be called mid-chain.
package query

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/devituz/lagodev/database"
)

// Op enumerates supported comparison operators.
const (
	OpEq         = "="
	OpNe         = "<>"
	OpLt         = "<"
	OpLte        = "<="
	OpGt         = ">"
	OpGte        = ">="
	OpLike       = "like"
	OpILike      = "ilike"
	OpIn         = "in"
	OpNotIn      = "not in"
	OpBetween    = "between"
	OpNotBetween = "not between"
	OpIsNull     = "is null"
	OpNotNull    = "is not null"
)

// where boolean glue.
const (
	bAnd = "AND"
	bOr  = "OR"
)

type condition struct {
	bool   string
	col    string
	op     string
	values []any
	raw    string
	args   []any
	nested *Builder // for nested groups
}

type orderClause struct {
	column string
	dir    string
	raw    bool
}

type joinClause struct {
	kind  string
	table string
	on    string
	args  []any
}

// Builder builds SQL statements.
type Builder struct {
	conn       *database.Connection
	executor   database.Executor
	table      string
	cols       []string
	distinct   bool
	wheres     []condition
	joins      []joinClause
	groups     []string
	havings    []condition
	orders     []orderClause
	limit      int
	offset     int
	bindings   int
	lockClause string
}

// Connection returns the underlying connection.
func (b *Builder) Connection() *database.Connection { return b.conn }

// SetExecutor lets a Tx wrap the builder so it runs inside a transaction.
func (b *Builder) SetExecutor(e database.Executor) *Builder { b.executor = e; return b }

// From sets the table.
func (b *Builder) From(table string) *Builder { b.table = table; return b }

// Select replaces the column list.
func (b *Builder) Select(cols ...string) *Builder {
	b.cols = append(b.cols[:0], cols...)
	return b
}

// Distinct toggles DISTINCT.
func (b *Builder) Distinct() *Builder { b.distinct = true; return b }

// Where appends an AND condition.
//
// Where("name", "=", "Ada") | Where("name", "Ada") | Where(func(q *Builder) { ... })
func (b *Builder) Where(args ...any) *Builder { return b.addWhere(bAnd, args) }

// OrWhere appends an OR condition.
func (b *Builder) OrWhere(args ...any) *Builder { return b.addWhere(bOr, args) }

func (b *Builder) addWhere(boolean string, args []any) *Builder {
	switch len(args) {
	case 1:
		switch v := args[0].(type) {
		case func(*Builder):
			nested := newSubBuilder(b)
			v(nested)
			b.wheres = append(b.wheres, condition{bool: boolean, nested: nested})
		default:
			panic("query: Where with 1 arg requires func(*Builder)")
		}
	case 2:
		b.wheres = append(b.wheres, condition{bool: boolean, col: asString(args[0]), op: OpEq, values: []any{args[1]}})
	case 3:
		b.wheres = append(b.wheres, condition{
			bool: boolean, col: asString(args[0]), op: normalizeOp(asString(args[1])), values: []any{args[2]},
		})
	default:
		panic("query: Where takes 1, 2 or 3 arguments")
	}
	return b
}

// WhereIn appends an IN constraint.
func (b *Builder) WhereIn(col string, values any) *Builder {
	return b.addInWhere(bAnd, col, OpIn, values)
}

// WhereNotIn appends a NOT IN constraint.
func (b *Builder) WhereNotIn(col string, values any) *Builder {
	return b.addInWhere(bAnd, col, OpNotIn, values)
}

func (b *Builder) addInWhere(boolean, col, op string, values any) *Builder {
	expanded := expand(values)
	b.wheres = append(b.wheres, condition{bool: boolean, col: col, op: op, values: expanded})
	return b
}

// WhereNull appends an IS NULL constraint.
func (b *Builder) WhereNull(col string) *Builder {
	b.wheres = append(b.wheres, condition{bool: bAnd, col: col, op: OpIsNull})
	return b
}

// WhereNotNull appends an IS NOT NULL constraint.
func (b *Builder) WhereNotNull(col string) *Builder {
	b.wheres = append(b.wheres, condition{bool: bAnd, col: col, op: OpNotNull})
	return b
}

// WhereBetween appends a BETWEEN constraint.
func (b *Builder) WhereBetween(col string, lo, hi any) *Builder {
	b.wheres = append(b.wheres, condition{bool: bAnd, col: col, op: OpBetween, values: []any{lo, hi}})
	return b
}

// WhereRaw appends a raw expression.
func (b *Builder) WhereRaw(expr string, args ...any) *Builder {
	b.wheres = append(b.wheres, condition{bool: bAnd, raw: expr, args: args})
	return b
}

// Join adds an INNER JOIN.
func (b *Builder) Join(table, on string, args ...any) *Builder {
	b.joins = append(b.joins, joinClause{kind: "INNER", table: table, on: on, args: args})
	return b
}

// LeftJoin adds a LEFT JOIN.
func (b *Builder) LeftJoin(table, on string, args ...any) *Builder {
	b.joins = append(b.joins, joinClause{kind: "LEFT", table: table, on: on, args: args})
	return b
}

// GroupBy adds GROUP BY columns.
func (b *Builder) GroupBy(cols ...string) *Builder {
	b.groups = append(b.groups, cols...)
	return b
}

// Having adds an AND HAVING. The operator is whitelisted (see normalizeOp); an
// unsupported operator panics rather than being concatenated into the SQL.
func (b *Builder) Having(col, op string, value any) *Builder {
	b.havings = append(b.havings, condition{bool: bAnd, col: col, op: normalizeOp(op), values: []any{value}})
	return b
}

// OrderBy adds an ORDER BY. The direction is whitelisted to ASC/DESC; any
// other value (e.g. an injection attempt) panics rather than being emitted
// into the SQL.
func (b *Builder) OrderBy(col, dir string) *Builder {
	b.orders = append(b.orders, orderClause{column: col, dir: normalizeDir(dir)})
	return b
}

// normalizeOp whitelists a comparison operator. The operator is rendered
// verbatim into the SQL (it is a keyword position, never a bound value), so an
// unvalidated operator would be a SQL-injection vector — e.g.
// Where("col", userInput, val). Only the operators the builder knows how to
// compile are accepted; anything else panics rather than being emitted, the
// same defensive policy used for the ORDER BY direction.
func normalizeOp(op string) string {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case OpEq:
		return OpEq
	case OpNe, "!=":
		return OpNe
	case OpLt:
		return OpLt
	case OpLte:
		return OpLte
	case OpGt:
		return OpGt
	case OpGte:
		return OpGte
	case OpLike:
		return OpLike
	case OpILike:
		return OpILike
	default:
		panic("query: unsupported operator " + op)
	}
}

// normalizeDir whitelists the ORDER BY direction. Empty defaults to ASC; any
// value other than ASC/DESC (case-insensitive) is rejected to prevent SQL
// injection through the direction argument.
func normalizeDir(dir string) string {
	switch strings.ToUpper(strings.TrimSpace(dir)) {
	case "", "ASC":
		return "ASC"
	case "DESC":
		return "DESC"
	default:
		panic("query: OrderBy direction must be ASC or DESC, got " + dir)
	}
}

// OrderByRaw adds a raw ORDER BY clause.
func (b *Builder) OrderByRaw(expr string) *Builder {
	b.orders = append(b.orders, orderClause{column: expr, raw: true})
	return b
}

// Latest orders by the column descending. Default column is "created_at".
func (b *Builder) Latest(col ...string) *Builder {
	c := "created_at"
	if len(col) > 0 {
		c = col[0]
	}
	return b.OrderBy(c, "DESC")
}

// Oldest orders by the column ascending. Default column is "created_at".
func (b *Builder) Oldest(col ...string) *Builder {
	c := "created_at"
	if len(col) > 0 {
		c = col[0]
	}
	return b.OrderBy(c, "ASC")
}

// Limit sets the row limit.
func (b *Builder) Limit(n int) *Builder { b.limit = n; return b }

// Offset sets the row offset.
func (b *Builder) Offset(n int) *Builder { b.offset = n; return b }

// LockForUpdate appends FOR UPDATE on supported dialects. SQLite has no row
// locking syntax, so the clause is a no-op there.
func (b *Builder) LockForUpdate() *Builder {
	if b.conn != nil && b.conn.Grammar.Name() == "sqlite" {
		return b
	}
	b.lockClause = " FOR UPDATE"
	return b
}

// SharedLock appends FOR SHARE on supported dialects. SQLite has no row
// locking syntax, so the clause is a no-op there.
func (b *Builder) SharedLock() *Builder {
	if b.conn != nil && b.conn.Grammar.Name() == "sqlite" {
		return b
	}
	b.lockClause = " FOR SHARE"
	return b
}

// New returns a fresh Builder for the given connection/table.
func New(conn *database.Connection, table string) *Builder {
	return &Builder{conn: conn, executor: conn, table: table}
}

func newSubBuilder(parent *Builder) *Builder {
	return &Builder{conn: parent.conn, executor: parent.executor}
}

// Clone returns a deep copy of the builder. Callers that want to branch a
// query (e.g. run First() and then Get() off the same base) use this to avoid
// the receiver's single-use mutation semantics leaking across calls.
func (b *Builder) Clone() *Builder { return b.clone() }

// clone returns a deep copy of the builder so that single-use helpers
// (Count, Exists, aggregates) and the ORM convenience methods can apply extra
// clauses without mutating the shared receiver. Slices are copied; the nested
// sub-builders inside conditions are immutable once built and may be shared.
func (b *Builder) clone() *Builder {
	c := *b
	c.cols = append([]string(nil), b.cols...)
	c.wheres = append([]condition(nil), b.wheres...)
	c.joins = append([]joinClause(nil), b.joins...)
	c.groups = append([]string(nil), b.groups...)
	c.havings = append([]condition(nil), b.havings...)
	c.orders = append([]orderClause(nil), b.orders...)
	return &c
}

// ToSQL compiles the builder into a SELECT statement and its bound args.
func (b *Builder) ToSQL() (string, []any, error) {
	if b.table == "" {
		return "", nil, errors.New("query: table not set")
	}
	args := make([]any, 0, 8)
	b.bindings = 0
	g := b.conn.Grammar

	var sb strings.Builder
	sb.WriteString("SELECT ")
	if b.distinct {
		sb.WriteString("DISTINCT ")
	}
	if len(b.cols) == 0 {
		sb.WriteString("*")
	} else {
		quoted := make([]string, len(b.cols))
		for i, c := range b.cols {
			quoted[i] = quoteSelectable(g, c)
		}
		sb.WriteString(strings.Join(quoted, ", "))
	}
	sb.WriteString(" FROM ")
	sb.WriteString(g.Quote(b.table))

	for _, j := range b.joins {
		sb.WriteString(" ")
		sb.WriteString(j.kind)
		sb.WriteString(" JOIN ")
		sb.WriteString(g.Quote(j.table))
		sb.WriteString(" ON ")
		sb.WriteString(j.on)
		args = append(args, j.args...)
	}

	if len(b.wheres) > 0 {
		sb.WriteString(" WHERE ")
		whereSQL, whereArgs := b.compileWheres(b.wheres, &b.bindings)
		sb.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	if len(b.groups) > 0 {
		sb.WriteString(" GROUP BY ")
		quoted := make([]string, len(b.groups))
		for i, c := range b.groups {
			// GROUP BY takes column identifiers here; quote them strictly so a
			// hostile column name cannot pass through unquoted. Expression
			// grouping is not part of this builder's safe surface.
			quoted[i] = quoteColumn(g, c)
		}
		sb.WriteString(strings.Join(quoted, ", "))
	}
	if len(b.havings) > 0 {
		sb.WriteString(" HAVING ")
		hSQL, hArgs := b.compileWheres(b.havings, &b.bindings)
		sb.WriteString(hSQL)
		args = append(args, hArgs...)
	}
	if len(b.orders) > 0 {
		parts := make([]string, 0, len(b.orders))
		for _, o := range b.orders {
			if o.raw {
				parts = append(parts, o.column)
			} else {
				// Non-raw ORDER BY is a column identifier; quote it strictly.
				// Expression ordering goes through OrderByRaw.
				parts = append(parts, quoteColumn(g, o.column)+" "+o.dir)
			}
		}
		sb.WriteString(" ORDER BY ")
		sb.WriteString(strings.Join(parts, ", "))
	}
	if b.limit > 0 {
		sb.WriteString(" LIMIT ")
		sb.WriteString(strconv.Itoa(b.limit))
	}
	if b.offset > 0 {
		sb.WriteString(" OFFSET ")
		sb.WriteString(strconv.Itoa(b.offset))
	}
	sb.WriteString(b.lockClause)
	return sb.String(), args, nil
}

// Get executes the SELECT and returns the rows.
func (b *Builder) Get(ctx context.Context) (*sql.Rows, error) {
	q, args, err := b.ToSQL()
	if err != nil {
		return nil, err
	}
	return b.executor.QueryContext(ctx, q, args...)
}

// Count returns the COUNT(*) for the current WHERE/JOIN state. When the query
// has a GROUP BY, a plain COUNT(*) would return only the first group's count;
// in that case the grouped query is wrapped in a subquery so the result is the
// number of groups.
func (b *Builder) Count(ctx context.Context) (int64, error) {
	clone := b.clone()
	clone.orders = nil
	clone.limit = 0
	clone.offset = 0

	var q string
	var args []any
	var err error
	if len(clone.groups) > 0 {
		// Wrap the grouped query: SELECT COUNT(*) FROM (<grouped>) sub.
		inner, innerArgs, terr := clone.ToSQL()
		if terr != nil {
			return 0, terr
		}
		q = "SELECT COUNT(*) AS aggregate FROM (" + inner + ") sub"
		args = innerArgs
	} else {
		clone.cols = []string{"COUNT(*) AS aggregate"}
		q, args, err = clone.ToSQL()
		if err != nil {
			return 0, err
		}
	}
	var n int64
	if err := b.executor.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Exists returns true if at least one row matches. It operates on a clone so
// the receiver's state is not mutated.
func (b *Builder) Exists(ctx context.Context) (bool, error) {
	n, err := b.clone().Limit(1).Count(ctx)
	return n > 0, err
}

// Sum returns the sum of a column.
func (b *Builder) Sum(ctx context.Context, col string) (float64, error) {
	return b.aggregate(ctx, "SUM", col)
}

// Avg returns the average of a column.
func (b *Builder) Avg(ctx context.Context, col string) (float64, error) {
	return b.aggregate(ctx, "AVG", col)
}

// Max returns the maximum value.
func (b *Builder) Max(ctx context.Context, col string) (float64, error) {
	return b.aggregate(ctx, "MAX", col)
}

// Min returns the minimum value.
func (b *Builder) Min(ctx context.Context, col string) (float64, error) {
	return b.aggregate(ctx, "MIN", col)
}

func (b *Builder) aggregate(ctx context.Context, fn, col string) (float64, error) {
	clone := b.clone()
	clone.cols = []string{fmt.Sprintf("%s(%s) AS aggregate", fn, b.conn.Grammar.Quote(col))}
	clone.orders = nil
	q, args, err := clone.ToSQL()
	if err != nil {
		return 0, err
	}
	var v sql.NullFloat64
	if err := b.executor.QueryRowContext(ctx, q, args...).Scan(&v); err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Float64, nil
}

func (b *Builder) compileWheres(conds []condition, bindings *int) (string, []any) {
	var sb strings.Builder
	var args []any
	g := b.conn.Grammar
	for i, c := range conds {
		if i > 0 {
			sb.WriteString(" ")
			sb.WriteString(c.bool)
			sb.WriteString(" ")
		}
		switch {
		case c.nested != nil:
			nestedSQL, nestedArgs := b.compileWheres(c.nested.wheres, bindings)
			sb.WriteString("(")
			sb.WriteString(nestedSQL)
			sb.WriteString(")")
			args = append(args, nestedArgs...)
		case c.raw != "":
			sb.WriteString(c.raw)
			args = append(args, c.args...)
			*bindings += len(c.args)
		case c.op == OpIsNull || c.op == OpNotNull:
			sb.WriteString(quoteColumn(g, c.col))
			sb.WriteString(" ")
			sb.WriteString(strings.ToUpper(c.op))
		case c.op == OpIn || c.op == OpNotIn:
			if len(c.values) == 0 {
				if c.op == OpIn {
					sb.WriteString("1 = 0")
				} else {
					sb.WriteString("1 = 1")
				}
				continue
			}
			sb.WriteString(quoteColumn(g, c.col))
			sb.WriteString(" ")
			sb.WriteString(strings.ToUpper(c.op))
			sb.WriteString(" (")
			placeholders := make([]string, len(c.values))
			for k := range c.values {
				*bindings++
				placeholders[k] = g.Placeholder(*bindings)
			}
			sb.WriteString(strings.Join(placeholders, ", "))
			sb.WriteString(")")
			args = append(args, c.values...)
		case c.op == OpBetween || c.op == OpNotBetween:
			sb.WriteString(quoteColumn(g, c.col))
			sb.WriteString(" ")
			sb.WriteString(strings.ToUpper(c.op))
			sb.WriteString(" ")
			*bindings++
			sb.WriteString(g.Placeholder(*bindings))
			sb.WriteString(" AND ")
			*bindings++
			sb.WriteString(g.Placeholder(*bindings))
			args = append(args, c.values...)
		default:
			sb.WriteString(quoteColumn(g, c.col))
			sb.WriteString(" ")
			sb.WriteString(strings.ToUpper(c.op))
			sb.WriteString(" ")
			*bindings++
			sb.WriteString(g.Placeholder(*bindings))
			args = append(args, c.values[0])
		}
	}
	return sb.String(), args
}

// Insert inserts a single row. Returns the *sql.Result.
func (b *Builder) Insert(ctx context.Context, values map[string]any) (sql.Result, error) {
	g := b.conn.Grammar
	cols := make([]string, 0, len(values))
	for c := range values {
		cols = append(cols, c)
	}
	// stable order for tests
	sortStrings(cols)
	args := make([]any, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		args[i] = values[c]
		placeholders[i] = g.Placeholder(i + 1)
	}
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = g.Quote(c)
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		g.Quote(b.table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return b.executor.ExecContext(ctx, q, args...)
}

// InsertGetID inserts and returns the auto-incremented ID. For Postgres this
// appends RETURNING <pk>.
func (b *Builder) InsertGetID(ctx context.Context, values map[string]any, pk string) (int64, error) {
	g := b.conn.Grammar
	cols := make([]string, 0, len(values))
	for c := range values {
		cols = append(cols, c)
	}
	sortStrings(cols)
	args := make([]any, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		args[i] = values[c]
		placeholders[i] = g.Placeholder(i + 1)
	}
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = g.Quote(c)
	}
	base := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		g.Quote(b.table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	if g.LastInsertIDStrategy() == database.InsertIDViaReturning {
		q := base + " RETURNING " + g.Quote(pk)
		var id int64
		if err := b.executor.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	res, err := b.executor.ExecContext(ctx, base, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertBatch performs a multi-row insert.
func (b *Builder) InsertBatch(ctx context.Context, rows []map[string]any) (sql.Result, error) {
	if len(rows) == 0 {
		return nil, errors.New("query: InsertBatch with no rows")
	}
	g := b.conn.Grammar
	cols := make([]string, 0, len(rows[0]))
	for c := range rows[0] {
		cols = append(cols, c)
	}
	sortStrings(cols)
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = g.Quote(c)
	}
	args := make([]any, 0, len(cols)*len(rows))
	placeholderGroups := make([]string, 0, len(rows))
	binding := 0
	for _, row := range rows {
		group := make([]string, len(cols))
		for i, c := range cols {
			binding++
			group[i] = g.Placeholder(binding)
			args = append(args, row[c])
		}
		placeholderGroups = append(placeholderGroups, "("+strings.Join(group, ", ")+")")
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
		g.Quote(b.table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholderGroups, ", "),
	)
	return b.executor.ExecContext(ctx, q, args...)
}

// Update performs an UPDATE with the current WHERE clauses.
func (b *Builder) Update(ctx context.Context, values map[string]any) (int64, error) {
	g := b.conn.Grammar
	if len(values) == 0 {
		return 0, errors.New("query: Update without values")
	}
	cols := make([]string, 0, len(values))
	for c := range values {
		cols = append(cols, c)
	}
	sortStrings(cols)
	args := make([]any, 0, len(values)+8)
	sets := make([]string, len(cols))
	for i, c := range cols {
		args = append(args, values[c])
		sets[i] = fmt.Sprintf("%s = %s", g.Quote(c), g.Placeholder(i+1))
	}
	q := fmt.Sprintf("UPDATE %s SET %s", g.Quote(b.table), strings.Join(sets, ", "))
	b.bindings = len(cols)
	if len(b.wheres) > 0 {
		whereSQL, whereArgs := b.compileWheres(b.wheres, &b.bindings)
		q += " WHERE " + whereSQL
		args = append(args, whereArgs...)
	}
	res, err := b.executor.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Delete deletes rows matching the WHERE clauses. Returns rows affected.
func (b *Builder) Delete(ctx context.Context) (int64, error) {
	g := b.conn.Grammar
	q := fmt.Sprintf("DELETE FROM %s", g.Quote(b.table))
	b.bindings = 0
	var args []any
	if len(b.wheres) > 0 {
		whereSQL, whereArgs := b.compileWheres(b.wheres, &b.bindings)
		q += " WHERE " + whereSQL
		args = whereArgs
	}
	res, err := b.executor.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Truncate empties the table.
func (b *Builder) Truncate(ctx context.Context) error {
	q := "TRUNCATE TABLE " + b.conn.Grammar.Quote(b.table)
	if b.conn.Grammar.Name() == "sqlite" {
		q = "DELETE FROM " + b.conn.Grammar.Quote(b.table)
	}
	_, err := b.executor.ExecContext(ctx, q)
	return err
}

// quoteSelectable handles plain column names, fully-qualified "t.c", aggregates
// and aliases ("col AS alias"). Aggregates and raw expressions pass through.
func quoteSelectable(g database.Grammar, s string) string {
	trim := strings.TrimSpace(s)
	if trim == "" || trim == "*" {
		return "*"
	}
	if strings.ContainsAny(trim, "(") || strings.Contains(strings.ToUpper(trim), " AS ") {
		return trim
	}
	if strings.Contains(trim, ".") {
		return g.Quote(trim)
	}
	return g.Quote(trim)
}

// quoteColumn strictly quotes a column used in a predicate position (WHERE /
// HAVING / IN / BETWEEN). Unlike quoteSelectable it has NO passthrough escape
// hatch: an attacker-controlled column containing "(" or " AS " (or any other
// metacharacter) must not slip into the SQL unquoted. The grammar's Quote
// wraps the identifier in "..." and doubles embedded quotes, so a hostile
// column name is rendered as an inert (non-existent) identifier rather than as
// executable SQL. Dotted "table.col" is preserved (Quote handles the dot). For
// genuinely dynamic predicate expressions, callers use WhereRaw.
func quoteColumn(g database.Grammar, s string) string {
	trim := strings.TrimSpace(s)
	if trim == "" || trim == "*" {
		return "*"
	}
	return g.Quote(trim)
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	default:
		return fmt.Sprintf("%v", v)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// expand normalizes a value into a slice. Supports []int, []int64, []string
// and []any directly.
func expand(values any) []any {
	switch v := values.(type) {
	case []any:
		return v
	case []int:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	case []int64:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	case []uint64:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	default:
		return []any{v}
	}
}
