// Package mysql registers the MySQL/MariaDB driver and grammar.
package mysql

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"github.com/devituz/lagodev/database"
)

func init() {
	database.Register("mysql", open)
	database.Register("mariadb", open)
}

func open(dsn string) (*sql.DB, database.Grammar, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, nil, err
	}
	return db, Grammar{}, nil
}

// Grammar implements database.Grammar for MySQL/MariaDB.
type Grammar struct{}

// Name returns the driver name.
func (Grammar) Name() string { return "mysql" }

// Quote wraps an identifier in backticks.
func (Grammar) Quote(ident string) string {
	if ident == "*" {
		return "*"
	}
	parts := strings.Split(ident, ".")
	for i, p := range parts {
		parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return strings.Join(parts, ".")
}

// Placeholder returns "?" — MySQL uses positional ? placeholders.
func (Grammar) Placeholder(_ int) string { return "?" }

// SupportsReturning is false in MySQL (MariaDB 10.5+ does, but we use
// LastInsertId for portability).
func (Grammar) SupportsReturning() bool { return false }

// LastInsertIDStrategy returns FromResult.
func (Grammar) LastInsertIDStrategy() database.InsertIDStrategy {
	return database.InsertIDFromResult
}

// CompileType renders a MySQL column type.
func (Grammar) CompileType(kind string, opts database.ColumnTypeOptions) string {
	suffix := ""
	if opts.Unsigned {
		switch kind {
		case "bigInteger", "integer", "smallInteger", "tinyInteger",
			"unsignedBigInteger", "unsignedInteger", "bigIncrements", "increments":
			suffix = " UNSIGNED"
		}
	}
	auto := ""
	if opts.AutoIncrement {
		auto = " AUTO_INCREMENT"
	}
	switch kind {
	case "bigIncrements", "unsignedBigInteger":
		return "BIGINT" + suffix + auto
	case "increments", "unsignedInteger":
		return "INT" + suffix + auto
	case "bigInteger":
		return "BIGINT" + suffix
	case "integer":
		return "INT" + suffix
	case "smallInteger":
		return "SMALLINT" + suffix
	case "tinyInteger":
		return "TINYINT" + suffix
	case "float":
		return "FLOAT"
	case "double":
		return "DOUBLE"
	case "decimal":
		p, s := opts.Precision, opts.Scale
		if p == 0 {
			p = 8
		}
		return fmt.Sprintf("DECIMAL(%d,%d)", p, s)
	case "boolean":
		return "TINYINT(1)"
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
	case "text":
		return "TEXT"
	case "mediumText":
		return "MEDIUMTEXT"
	case "longText":
		return "LONGTEXT"
	case "binary":
		return "BLOB"
	case "uuid":
		return "CHAR(36)"
	case "json", "jsonb":
		return "JSON"
	case "date":
		return "DATE"
	case "dateTime":
		return "DATETIME"
	case "timestamp", "timestampTz":
		return "TIMESTAMP"
	case "time":
		return "TIME"
	case "enum":
		return "ENUM(" + quotedList(opts.Allowed) + ")"
	case "set":
		return "SET(" + quotedList(opts.Allowed) + ")"
	case "ipAddress":
		return "VARCHAR(45)"
	case "macAddress":
		return "VARCHAR(17)"
	default:
		return "TEXT"
	}
}

func quotedList(values []string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(out, ", ")
}
