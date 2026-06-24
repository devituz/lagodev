package schema

import (
	"fmt"
	"strings"

	"github.com/devituz/lagodev/database"
)

// Statement is a compiled SQL string with optional positional args. Most
// schema statements have no args, but driver-specific code (e.g. comments on
// Postgres) may emit them.
type Statement struct {
	SQL  string
	Args []any
}

// Compiler compiles a blueprint into one or more Statements. It is a thin
// orchestrator on top of database.Grammar — the grammar handles type
// rendering and identifier quoting; the compiler walks the blueprint and
// assembles DDL.
type Compiler struct {
	grammar database.Grammar
}

// NewCompiler builds a compiler bound to the given grammar.
func NewCompiler(g database.Grammar) *Compiler { return &Compiler{grammar: g} }

// Compile returns all statements for a Definition.
func (c *Compiler) Compile(def *Definition) ([]Statement, error) {
	if def == nil || def.Blueprint == nil {
		return nil, fmt.Errorf("schema: nil definition")
	}
	bp := def.Blueprint
	if err := bp.validate(); err != nil {
		return nil, err
	}

	switch def.Op {
	case OpCreate, OpCreateIfNotExists:
		return c.compileCreate(bp, def.Op == OpCreateIfNotExists)
	case OpAlter:
		return c.compileAlter(bp)
	case OpDrop:
		return []Statement{{SQL: "DROP TABLE " + c.grammar.Quote(bp.table)}}, nil
	case OpDropIfExists:
		return []Statement{{SQL: "DROP TABLE IF EXISTS " + c.grammar.Quote(bp.table)}}, nil
	case OpRename:
		return c.compileRename(bp)
	}
	return nil, fmt.Errorf("schema: unsupported operation %v", def.Op)
}

func (c *Compiler) compileCreate(bp *Blueprint, ifNotExists bool) ([]Statement, error) {
	var stmts []Statement

	// Resolve the effective primary-key columns once, merging columns flagged
	// IsPrimary with any Primary() index declaration and de-duplicating. A
	// single-column PK is rendered inline on its column — SQLite needs this for
	// the INTEGER rowid alias and Postgres needs it so BIGSERIAL/SERIAL gets a
	// real PRIMARY KEY (otherwise FKs referencing it fail with SQLSTATE 42830).
	// A composite PK is rendered as a table-level constraint. This keeps the
	// same migration code working across all dialects and avoids emitting the
	// PK twice when both ID() and Primary("id") name the same column.
	pkCols := primaryKeyColumns(bp)
	inlinePK := ""
	if len(pkCols) == 1 {
		inlinePK = pkCols[0]
	}

	var cols []string
	var checks []string
	for _, col := range bp.columns {
		cols = append(cols, c.compileColumnInline(col, inlinePK != "" && col.Name == inlinePK))
		// ENUM/SET on non-MySQL dialects are stored as VARCHAR/TEXT; constrain
		// them to the declared values via a table-level CHECK. Table constraints
		// must follow every column definition, so collect them and append below.
		// MySQL uses native ENUM/SET, so no CHECK is needed there.
		if chk := c.compileEnumCheck(col); chk != "" {
			checks = append(checks, chk)
		}
	}

	if len(pkCols) > 1 {
		cols = append(cols, "PRIMARY KEY ("+c.joinQuoted(pkCols)+")")
	}

	for _, fk := range bp.foreigns {
		cols = append(cols, c.compileForeignInline(bp.table, fk))
	}

	cols = append(cols, checks...)

	keyword := "CREATE TABLE"
	if bp.temporary {
		keyword = "CREATE TEMPORARY TABLE"
	}
	if ifNotExists {
		keyword += " IF NOT EXISTS"
	}

	body := strings.Join(cols, ", ")
	create := fmt.Sprintf("%s %s (%s)", keyword, c.grammar.Quote(bp.table), body)

	if bp.engine != "" && c.grammar.Name() == "mysql" {
		create += " ENGINE=" + bp.engine
	}
	if bp.charset != "" && c.grammar.Name() == "mysql" {
		create += " DEFAULT CHARSET=" + bp.charset
	}
	if bp.collation != "" && c.grammar.Name() == "mysql" {
		create += " COLLATE=" + bp.collation
	}

	stmts = append(stmts, Statement{SQL: create})

	names := newNameAllocator()
	for _, idx := range bp.indexes {
		if idx.Type == IndexPrimary {
			continue
		}
		stmts = append(stmts, c.compileIndex(bp.table, idx, names))
	}

	// Single-column .Index() declarations are not represented as Index entries;
	// emit a CREATE INDEX for each. (Single-column .Unique() is rendered inline
	// as a column UNIQUE constraint on CREATE, so it is not repeated here.)
	for _, col := range bp.columns {
		if col.IsIndex {
			stmts = append(stmts, c.compileIndex(bp.table, &Index{
				Type:    IndexPlain,
				Columns: []string{col.Name},
			}, names))
		}
	}

	if bp.comment != "" {
		if s, ok := c.tableComment(bp.table, bp.comment); ok {
			stmts = append(stmts, s)
		}
	}
	return stmts, nil
}

func (c *Compiler) compileAlter(bp *Blueprint) ([]Statement, error) {
	var stmts []Statement
	names := newNameAllocator()

	for _, col := range bp.columns {
		// On ADD COLUMN, UNIQUE must never be inlined: SQLite rejects
		// "ADD COLUMN ... UNIQUE" ("Cannot add a UNIQUE column"). Emit the
		// column without the inline UNIQUE and add a CREATE UNIQUE INDEX below.
		stmts = append(stmts, Statement{
			SQL: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s",
				c.grammar.Quote(bp.table), c.compileColumnAdd(col, col.IsPrimary)),
		})
		if chk := c.compileEnumCheck(col); chk != "" {
			// Best-effort: ENUM/SET CHECK on ALTER ADD COLUMN. SQLite cannot add
			// a CHECK to an existing table, so we skip it there.
			if c.grammar.Name() != "sqlite" {
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("ALTER TABLE %s ADD %s",
						c.grammar.Quote(bp.table), chk),
				})
			}
		}
	}

	for _, fk := range bp.foreigns {
		stmts = append(stmts, Statement{
			SQL: fmt.Sprintf("ALTER TABLE %s ADD %s",
				c.grammar.Quote(bp.table), c.compileForeignInline(bp.table, fk)),
		})
	}

	for _, idx := range bp.indexes {
		stmts = append(stmts, c.compileIndex(bp.table, idx, names))
	}

	// Single-column .Index()/.Unique() on added columns become separate
	// CREATE [UNIQUE] INDEX statements (UNIQUE is never inlined on ALTER).
	for _, col := range bp.columns {
		switch {
		case col.IsUnique:
			stmts = append(stmts, c.compileIndex(bp.table, &Index{
				Type:    IndexUnique,
				Columns: []string{col.Name},
			}, names))
		case col.IsIndex:
			stmts = append(stmts, c.compileIndex(bp.table, &Index{
				Type:    IndexPlain,
				Columns: []string{col.Name},
			}, names))
		}
	}

	for _, cmd := range bp.commands {
		switch cmd.Type {
		case CmdDropColumn:
			for _, col := range cmd.Args {
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s",
						c.grammar.Quote(bp.table), c.grammar.Quote(col)),
				})
			}
		case CmdRenameColumn:
			if len(cmd.Args) == 2 {
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s",
						c.grammar.Quote(bp.table),
						c.grammar.Quote(cmd.Args[0]),
						c.grammar.Quote(cmd.Args[1])),
				})
			}
		case CmdDropIndex:
			// MySQL requires "DROP INDEX <name> ON <table>"; SQLite and Postgres
			// take a bare "DROP INDEX <name>".
			if c.grammar.Name() == "mysql" {
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("DROP INDEX %s ON %s",
						c.grammar.Quote(cmd.Args[0]), c.grammar.Quote(bp.table)),
				})
			} else {
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("DROP INDEX %s", c.grammar.Quote(cmd.Args[0])),
				})
			}
		case CmdDropForeign:
			// FK drop syntax is dialect-specific: Postgres uses DROP CONSTRAINT,
			// MySQL uses DROP FOREIGN KEY, and SQLite cannot drop a FK without a
			// full table rebuild (unsupported here).
			switch c.grammar.Name() {
			case "mysql":
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s",
						c.grammar.Quote(bp.table), c.grammar.Quote(cmd.Args[0])),
				})
			case "sqlite":
				return nil, fmt.Errorf("schema: SQLite cannot drop foreign key %q on %q: it requires a full table rebuild (unsupported)", cmd.Args[0], bp.table)
			default:
				stmts = append(stmts, Statement{
					SQL: fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s",
						c.grammar.Quote(bp.table), c.grammar.Quote(cmd.Args[0])),
				})
			}
		case CmdRenameTable:
			stmts = append(stmts, Statement{
				SQL: fmt.Sprintf("ALTER TABLE %s RENAME TO %s",
					c.grammar.Quote(bp.table), c.grammar.Quote(cmd.Args[0])),
			})
		}
	}
	return stmts, nil
}

func (c *Compiler) compileRename(bp *Blueprint) ([]Statement, error) {
	for _, cmd := range bp.commands {
		if cmd.Type == CmdRenameTable && len(cmd.Args) == 1 {
			return []Statement{{SQL: fmt.Sprintf("ALTER TABLE %s RENAME TO %s",
				c.grammar.Quote(bp.table), c.grammar.Quote(cmd.Args[0]))}}, nil
		}
	}
	return nil, fmt.Errorf("schema: rename requires a target")
}

func (c *Compiler) compileColumnInline(col *Column, primary bool) string {
	return c.compileColumn(col, primary, true)
}

// compileColumnAdd renders a column for ALTER TABLE ADD COLUMN. UNIQUE is never
// inlined here — SQLite rejects "ADD COLUMN ... UNIQUE" — the caller emits a
// separate CREATE UNIQUE INDEX instead.
func (c *Compiler) compileColumnAdd(col *Column, primary bool) string {
	return c.compileColumn(col, primary, false)
}

func (c *Compiler) compileColumn(col *Column, primary, inlineUnique bool) string {
	var b strings.Builder
	b.WriteString(c.grammar.Quote(col.Name))
	b.WriteByte(' ')
	b.WriteString(c.grammar.CompileType(string(col.Kind), database.ColumnTypeOptions{
		Length:        col.Length,
		Precision:     col.Precision,
		Scale:         col.Scale,
		Unsigned:      col.UnsignedV,
		AutoIncrement: col.AutoInc,
		Allowed:       col.Allowed,
	}))
	if col.NullableV {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	if col.HasDefault {
		b.WriteString(" DEFAULT ")
		if col.UseCurrent {
			b.WriteString("CURRENT_TIMESTAMP")
		} else {
			b.WriteString(c.formatDefault(col))
		}
	}
	if col.IsUnique && !primary && inlineUnique {
		b.WriteString(" UNIQUE")
	}
	if primary {
		// Single-column PK is rendered inline for every dialect; composite PKs
		// are emitted as a table-level constraint by compileCreate instead.
		b.WriteString(" PRIMARY KEY")
	}
	if col.CommentV != "" && c.grammar.Name() == "mysql" {
		fmt.Fprintf(&b, " COMMENT %s", quoteString(col.CommentV))
	}
	return b.String()
}

// compileEnumCheck returns a table-level CHECK constraint constraining an
// ENUM/SET column to its declared values, or "" when not applicable. MySQL uses
// native ENUM/SET and needs no CHECK; columns without allowed values are
// skipped.
func (c *Compiler) compileEnumCheck(col *Column) string {
	if col.Kind != KindEnum && col.Kind != KindSet {
		return ""
	}
	if c.grammar.Name() == "mysql" || len(col.Allowed) == 0 {
		return ""
	}
	quoted := make([]string, len(col.Allowed))
	for i, a := range col.Allowed {
		quoted[i] = quoteString(a)
	}
	return fmt.Sprintf("CHECK (%s IN (%s))",
		c.grammar.Quote(col.Name), strings.Join(quoted, ", "))
}

func (c *Compiler) compileForeignInline(table string, fk *Foreign) string {
	name := fk.NameV
	if name == "" {
		name = fmt.Sprintf("fk_%s_%s", table, strings.Join(fk.Columns, "_"))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
		c.grammar.Quote(name),
		c.joinQuoted(fk.Columns),
		c.grammar.Quote(fk.ReferencesTable),
		c.joinQuoted(fk.ReferencesCols),
	)
	if fk.OnDeleteAction != "" {
		fmt.Fprintf(&b, " ON DELETE %s", strings.ToUpper(fk.OnDeleteAction))
	}
	if fk.OnUpdateAction != "" {
		fmt.Fprintf(&b, " ON UPDATE %s", strings.ToUpper(fk.OnUpdateAction))
	}
	return b.String()
}

func (c *Compiler) compileIndex(table string, idx *Index, names *nameAllocator) Statement {
	name := idx.NameV
	if name == "" {
		// Generated names can collide when two same-type indexes cover the same
		// columns; the allocator suffixes duplicates (_2, _3, …) to keep them
		// unique within a blueprint.
		name = names.unique(fmt.Sprintf("%s_%s_%s", table, strings.Join(idx.Columns, "_"), idx.Type))
	}
	var keyword string
	switch idx.Type {
	case IndexUnique:
		keyword = "CREATE UNIQUE INDEX"
	case IndexFull:
		keyword = "CREATE FULLTEXT INDEX"
	case IndexSpatial:
		keyword = "CREATE SPATIAL INDEX"
	default:
		keyword = "CREATE INDEX"
	}
	return Statement{
		SQL: fmt.Sprintf("%s %s ON %s (%s)",
			keyword, c.grammar.Quote(name),
			c.grammar.Quote(table), c.joinQuoted(idx.Columns)),
	}
}

func (c *Compiler) tableComment(table, comment string) (Statement, bool) {
	switch c.grammar.Name() {
	case "postgres":
		return Statement{
			SQL: fmt.Sprintf("COMMENT ON TABLE %s IS %s", c.grammar.Quote(table), quoteString(comment)),
		}, true
	case "mysql":
		return Statement{
			SQL: fmt.Sprintf("ALTER TABLE %s COMMENT = %s", c.grammar.Quote(table), quoteString(comment)),
		}, true
	}
	return Statement{}, false
}

func (c *Compiler) joinQuoted(cols []string) string {
	out := make([]string, len(cols))
	for i, col := range cols {
		out[i] = c.grammar.Quote(col)
	}
	return strings.Join(out, ", ")
}

// primaryKeyColumns returns the effective primary-key column set for a
// blueprint, merging columns flagged IsPrimary with any Primary() index
// declaration. Order is preserved and duplicates are removed so that a column
// named both via ID()/Primary() and Primary("id") yields a single PK.
func primaryKeyColumns(bp *Blueprint) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, col := range bp.columns {
		if col.IsPrimary {
			add(col.Name)
		}
	}
	for _, idx := range bp.indexes {
		if idx.Type == IndexPrimary {
			for _, name := range idx.Columns {
				add(name)
			}
		}
	}
	return out
}

// formatDefault renders a column default, honoring dialect-specific literal
// rules. Boolean defaults are the notable case: Postgres BOOLEAN rejects an
// integer literal (SQLSTATE 42804) and needs TRUE/FALSE, whereas MySQL
// TINYINT(1) and SQLite's NUMERIC-affinity BOOLEAN take 1/0.
func (c *Compiler) formatDefault(col *Column) string {
	if x, ok := col.DefaultV.(bool); ok {
		if c.grammar.Name() == "postgres" {
			if x {
				return "TRUE"
			}
			return "FALSE"
		}
		if x {
			return "1"
		}
		return "0"
	}
	return formatDefault(col.DefaultV)
}

func formatDefault(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return quoteString(x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", x)
	}
}

func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// nameAllocator de-duplicates generated index/constraint names within a single
// blueprint compilation. The first use of a name is returned verbatim; later
// collisions get a numeric suffix ("_2", "_3", …).
type nameAllocator struct {
	seen map[string]int
}

func newNameAllocator() *nameAllocator {
	return &nameAllocator{seen: map[string]int{}}
}

func (a *nameAllocator) unique(base string) string {
	a.seen[base]++
	if n := a.seen[base]; n > 1 {
		return fmt.Sprintf("%s_%d", base, n)
	}
	return base
}
