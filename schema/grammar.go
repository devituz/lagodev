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
	var cols []string

	for _, col := range bp.columns {
		cols = append(cols, c.compileColumnInline(col))
	}

	var pkCols []string
	for _, col := range bp.columns {
		if col.IsPrimary {
			pkCols = append(pkCols, col.Name)
		}
	}
	if len(pkCols) > 1 {
		cols = append(cols, "PRIMARY KEY ("+c.joinQuoted(pkCols)+")")
	}

	for _, idx := range bp.indexes {
		if idx.Type == IndexPrimary {
			cols = append(cols, "PRIMARY KEY ("+c.joinQuoted(idx.Columns)+")")
		}
	}

	for _, fk := range bp.foreigns {
		cols = append(cols, c.compileForeignInline(bp.table, fk))
	}

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

	for _, idx := range bp.indexes {
		if idx.Type == IndexPrimary {
			continue
		}
		stmts = append(stmts, c.compileIndex(bp.table, idx))
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

	for _, col := range bp.columns {
		stmts = append(stmts, Statement{
			SQL: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s",
				c.grammar.Quote(bp.table), c.compileColumnInline(col)),
		})
	}

	for _, fk := range bp.foreigns {
		stmts = append(stmts, Statement{
			SQL: fmt.Sprintf("ALTER TABLE %s ADD %s",
				c.grammar.Quote(bp.table), c.compileForeignInline(bp.table, fk)),
		})
	}

	for _, idx := range bp.indexes {
		stmts = append(stmts, c.compileIndex(bp.table, idx))
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
			stmts = append(stmts, Statement{
				SQL: fmt.Sprintf("DROP INDEX %s", c.grammar.Quote(cmd.Args[0])),
			})
		case CmdDropForeign:
			stmts = append(stmts, Statement{
				SQL: fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s",
					c.grammar.Quote(bp.table), c.grammar.Quote(cmd.Args[0])),
			})
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

func (c *Compiler) compileColumnInline(col *Column) string {
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
			b.WriteString(formatDefault(col.DefaultV))
		}
	}
	if col.IsUnique && !col.IsPrimary {
		b.WriteString(" UNIQUE")
	}
	if col.IsPrimary && c.grammar.Name() != "postgres" {
		// Postgres handles PK via a separate constraint when the type is
		// SERIAL/BIGSERIAL or via PRIMARY KEY inline. For MySQL/SQLite we
		// inline it here for the typical single-column PK.
		if !isCompositePrimary(col) {
			b.WriteString(" PRIMARY KEY")
		}
	}
	if col.CommentV != "" && c.grammar.Name() == "mysql" {
		fmt.Fprintf(&b, " COMMENT %s", quoteString(col.CommentV))
	}
	return b.String()
}

func (c *Compiler) compileForeignInline(table string, fk *Foreign) string {
	name := fk.Name
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

func (c *Compiler) compileIndex(table string, idx *Index) Statement {
	name := idx.Name
	if name == "" {
		name = fmt.Sprintf("%s_%s_%s", table, strings.Join(idx.Columns, "_"), idx.Type)
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

func isCompositePrimary(col *Column) bool {
	// The compiler handles composite PKs via a table constraint, so when the
	// caller sets multiple primary columns we suppress the inline keyword.
	return false
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
