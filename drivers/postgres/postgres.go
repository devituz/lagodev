// Package postgres registers the PostgreSQL driver and grammar.
package postgres

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver

	"github.com/devituz/lagodev/database"
)

func init() {
	database.Register("postgres", open)
	database.Register("postgresql", open)
	database.Register("pgx", open)
}

func open(dsn string) (*sql.DB, database.Grammar, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, err
	}
	return db, Grammar{}, nil
}

// Grammar implements database.Grammar for PostgreSQL.
type Grammar struct{}

// Name returns the driver name.
func (Grammar) Name() string { return "postgres" }

// Quote wraps an identifier in double quotes, escaping embedded quotes. It
// preserves dotted "schema.table" forms.
func (Grammar) Quote(ident string) string {
	if ident == "*" {
		return "*"
	}
	parts := strings.Split(ident, ".")
	for i, p := range parts {
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// Placeholder returns "$N" for the n-th argument.
func (Grammar) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

// SupportsReturning is true: Postgres handles INSERT ... RETURNING natively.
func (Grammar) SupportsReturning() bool { return true }

// LastInsertIDStrategy returns ViaReturning.
func (Grammar) LastInsertIDStrategy() database.InsertIDStrategy {
	return database.InsertIDViaReturning
}

// CompileType renders a Postgres column type.
func (Grammar) CompileType(kind string, opts database.ColumnTypeOptions) string {
	switch kind {
	case "bigIncrements":
		return "BIGSERIAL"
	case "increments":
		return "SERIAL"
	case "bigInteger", "unsignedBigInteger":
		return "BIGINT"
	case "integer", "unsignedInteger":
		return "INTEGER"
	case "smallInteger":
		return "SMALLINT"
	case "tinyInteger":
		return "SMALLINT"
	case "float":
		return "REAL"
	case "double":
		return "DOUBLE PRECISION"
	case "decimal":
		p, s := opts.Precision, opts.Scale
		if p == 0 {
			p = 8
		}
		return fmt.Sprintf("NUMERIC(%d,%d)", p, s)
	case "boolean":
		return "BOOLEAN"
	case "string":
		l := opts.Length
		if l == 0 {
			l = 255
		}
		return "VARCHAR(" + strconv.Itoa(l) + ")"
	case "char":
		l := opts.Length
		if l == 0 {
			l = 1
		}
		return "CHAR(" + strconv.Itoa(l) + ")"
	case "text", "mediumText", "longText":
		return "TEXT"
	case "binary":
		return "BYTEA"
	case "uuid":
		return "UUID"
	case "json":
		return "JSON"
	case "jsonb":
		return "JSONB"
	case "date":
		return "DATE"
	case "dateTime", "timestamp":
		return "TIMESTAMP"
	case "timestampTz":
		return "TIMESTAMP WITH TIME ZONE"
	case "time":
		return "TIME"
	case "enum":
		// Use CHECK constraint via VARCHAR to avoid creating server-side ENUM
		// types (which complicate migrations). Compiler emits the CHECK
		// elsewhere if needed; we restrict to declared values via CHECK.
		quoted := make([]string, len(opts.Allowed))
		for i, a := range opts.Allowed {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		}
		if len(quoted) == 0 {
			return "VARCHAR(255)"
		}
		return "VARCHAR(255) CHECK (VALUE IN (" + strings.Join(quoted, ", ") + "))"
	case "set":
		return "TEXT[]"
	case "ipAddress":
		return "INET"
	case "macAddress":
		return "MACADDR"
	default:
		return "TEXT"
	}
}
