// Package sqlite registers the SQLite driver (via mattn/go-sqlite3) and
// provides a Grammar implementation. Importing this package is enough to
// make `sqlite` and `sqlite3` available to database.Manager.
package sqlite

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/devituz/lagodev/database"
)

func init() {
	database.Register("sqlite", open)
	database.Register("sqlite3", open)
}

func open(dsn string) (*sql.DB, database.Grammar, error) {
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, Grammar{}, nil
}

// Grammar implements database.Grammar for SQLite.
type Grammar struct{}

// Name returns the driver name.
func (Grammar) Name() string { return "sqlite" }

// Quote wraps an identifier in double quotes. Double-quoted identifiers are
// the SQL standard and are supported by SQLite.
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

// Placeholder returns "?" for any index — SQLite uses positional placeholders.
func (Grammar) Placeholder(_ int) string { return "?" }

// SupportsReturning indicates SQLite ≥ 3.35 supports RETURNING. We default to
// false to keep ID retrieval via LastInsertId broadly compatible.
func (Grammar) SupportsReturning() bool { return false }

// LastInsertIDStrategy returns the strategy used to retrieve the insert ID.
func (Grammar) LastInsertIDStrategy() database.InsertIDStrategy {
	return database.InsertIDFromResult
}

// CompileType renders a column type for SQLite.
func (Grammar) CompileType(kind string, opts database.ColumnTypeOptions) string {
	switch kind {
	case "bigIncrements", "increments":
		// SQLite uses INTEGER PRIMARY KEY AUTOINCREMENT; the PK clause is
		// emitted by the schema compiler when it sees IsPrimary, so we just
		// return INTEGER here and rely on the compiler to add PRIMARY KEY.
		return "INTEGER"
	case "bigInteger", "unsignedBigInteger":
		return "INTEGER"
	case "integer", "unsignedInteger":
		return "INTEGER"
	case "smallInteger", "tinyInteger":
		return "INTEGER"
	case "float", "double":
		return "REAL"
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
		return "BLOB"
	case "uuid":
		return "VARCHAR(36)"
	case "json", "jsonb":
		return "TEXT"
	case "date":
		return "DATE"
	case "dateTime", "timestamp", "timestampTz":
		return "DATETIME"
	case "time":
		return "TIME"
	case "enum", "set":
		// SQLite has no ENUM/SET; emulate with TEXT. The schema compiler emits a
		// table-level CHECK constraint constraining the column to the declared
		// values.
		return "TEXT"
	case "ipAddress":
		return "VARCHAR(45)"
	case "macAddress":
		return "VARCHAR(17)"
	default:
		return "TEXT"
	}
}
