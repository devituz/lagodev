package schema

import "strings"

// ColumnKind enumerates portable column kinds. Drivers translate kinds to
// dialect-specific SQL via database.Grammar.CompileType.
type ColumnKind string

const (
	KindBigIncrements ColumnKind = "bigIncrements"
	KindIncrements    ColumnKind = "increments"
	KindBigInteger    ColumnKind = "bigInteger"
	KindInteger       ColumnKind = "integer"
	KindSmallInteger  ColumnKind = "smallInteger"
	KindTinyInteger   ColumnKind = "tinyInteger"
	KindUnsignedBig   ColumnKind = "unsignedBigInteger"
	KindUnsignedInt   ColumnKind = "unsignedInteger"
	KindFloat         ColumnKind = "float"
	KindDouble        ColumnKind = "double"
	KindDecimal       ColumnKind = "decimal"
	KindBoolean       ColumnKind = "boolean"
	KindString        ColumnKind = "string"
	KindChar          ColumnKind = "char"
	KindText          ColumnKind = "text"
	KindMediumText    ColumnKind = "mediumText"
	KindLongText      ColumnKind = "longText"
	KindBinary        ColumnKind = "binary"
	KindUUID          ColumnKind = "uuid"
	KindJSON          ColumnKind = "json"
	KindJSONB         ColumnKind = "jsonb"
	KindDate          ColumnKind = "date"
	KindDateTime      ColumnKind = "dateTime"
	KindTime          ColumnKind = "time"
	KindTimestamp     ColumnKind = "timestamp"
	KindTimestampTZ   ColumnKind = "timestampTz"
	KindEnum          ColumnKind = "enum"
	KindSet           ColumnKind = "set"
	KindIPAddress     ColumnKind = "ipAddress"
	KindMACAddress    ColumnKind = "macAddress"
)

// Column is the fluent builder returned by Blueprint methods. The fluent
// methods set modifiers and return the same pointer for chaining.
type Column struct {
	Name       string
	Kind       ColumnKind
	Length     int
	Precision  int
	Scale      int
	NullableV  bool
	HasDefault bool
	DefaultV   any
	IsUnique   bool
	IsIndex    bool
	IsPrimary  bool
	UnsignedV  bool
	AutoInc    bool
	CommentV   string
	AfterV     string
	FirstV     bool
	Allowed    []string // for ENUM/SET
	Change     bool     // true when used inside Table() to alter
	Renamed    bool
	OldName    string // when renaming
	Drop       bool
	UseCurrent bool
}

// Nullable marks the column NULLABLE.
func (c *Column) Nullable() *Column { c.NullableV = true; return c }

// NotNull explicitly marks the column NOT NULL (default).
func (c *Column) NotNull() *Column { c.NullableV = false; return c }

// Default sets a literal default value.
func (c *Column) Default(v any) *Column { c.HasDefault = true; c.DefaultV = v; return c }

// UseCurrent uses CURRENT_TIMESTAMP as the default (timestamp columns).
func (c *Column) UseCurrentDefault() *Column { c.UseCurrent = true; c.HasDefault = true; return c }

// Unique adds a UNIQUE constraint.
func (c *Column) Unique() *Column { c.IsUnique = true; return c }

// Index requests a single-column index.
func (c *Column) Index() *Column { c.IsIndex = true; return c }

// Primary marks the column as primary key.
func (c *Column) Primary() *Column { c.IsPrimary = true; return c }

// Unsigned marks an integer column unsigned.
func (c *Column) Unsigned() *Column { c.UnsignedV = true; return c }

// AutoIncrement marks the column auto-incrementing.
func (c *Column) AutoIncrement() *Column { c.AutoInc = true; return c }

// Comment attaches an inline column comment.
func (c *Column) Comment(s string) *Column { c.CommentV = s; return c }

// After positions the column after another (MySQL).
func (c *Column) After(name string) *Column { c.AfterV = name; return c }

// First places the column first (MySQL).
func (c *Column) First() *Column { c.FirstV = true; return c }

// Allow installs allowed values for ENUM/SET.
func (c *Column) Allow(values ...string) *Column {
	c.Allowed = append(c.Allowed[:0], values...)
	return c
}

// Equal performs deep equality between two columns (used by tests).
func (c *Column) Equal(o *Column) bool {
	return c.Name == o.Name && c.Kind == o.Kind &&
		c.Length == o.Length && c.Precision == o.Precision && c.Scale == o.Scale &&
		c.NullableV == o.NullableV && c.HasDefault == o.HasDefault &&
		c.IsUnique == o.IsUnique && c.IsIndex == o.IsIndex && c.IsPrimary == o.IsPrimary &&
		c.UnsignedV == o.UnsignedV && c.AutoInc == o.AutoInc &&
		strings.Join(c.Allowed, ",") == strings.Join(o.Allowed, ",")
}
