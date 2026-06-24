package schema

import (
	"fmt"
	"strings"
)

// Blueprint is the fluent table builder. Migrations construct it via
// schema.Create / schema.Table; users never instantiate it directly. The
// methods mutate the blueprint and return Column / Foreign pointers for
// chaining (Eloquent-style).
type Blueprint struct {
	table     string
	create    bool
	temporary bool
	engine    string
	charset   string
	collation string
	comment   string
	columns   []*Column
	indexes   []*Index
	foreigns  []*Foreign
	commands  []*Command
}

// NewBlueprint returns an empty blueprint targeting the given table.
func NewBlueprint(table string, create bool) *Blueprint {
	return &Blueprint{table: table, create: create}
}

// Table returns the table name.
func (b *Blueprint) Table() string { return b.table }

// IsCreate reports whether this is CREATE TABLE (vs ALTER TABLE).
func (b *Blueprint) IsCreate() bool { return b.create }

// Columns returns all defined columns.
func (b *Blueprint) Columns() []*Column { return b.columns }

// Indexes returns all defined indexes.
func (b *Blueprint) Indexes() []*Index { return b.indexes }

// Foreigns returns all defined foreign keys.
func (b *Blueprint) Foreigns() []*Foreign { return b.foreigns }

// Commands returns deferred commands (drop column, rename column, etc).
func (b *Blueprint) Commands() []*Command { return b.commands }

// Temporary marks the table as TEMPORARY.
func (b *Blueprint) Temporary() *Blueprint { b.temporary = true; return b }

// IsTemporary reports whether the blueprint is temporary.
func (b *Blueprint) IsTemporary() bool { return b.temporary }

// Engine sets the storage engine (MySQL).
func (b *Blueprint) Engine(name string) *Blueprint { b.engine = name; return b }

// EngineName returns the configured engine, if any.
func (b *Blueprint) EngineName() string { return b.engine }

// Charset sets the table charset.
func (b *Blueprint) Charset(name string) *Blueprint { b.charset = name; return b }

// CharsetName returns the configured charset.
func (b *Blueprint) CharsetName() string { return b.charset }

// Collation sets the table collation.
func (b *Blueprint) Collation(name string) *Blueprint { b.collation = name; return b }

// CollationName returns the configured collation.
func (b *Blueprint) CollationName() string { return b.collation }

// Comment attaches a table-level comment.
func (b *Blueprint) Comment(s string) *Blueprint { b.comment = s; return b }

// CommentText returns the configured comment.
func (b *Blueprint) CommentText() string { return b.comment }

func (b *Blueprint) addCol(c *Column) *Column {
	b.columns = append(b.columns, c)
	return c
}

// ---------------------- Numeric primary key sugar ----------------------

// ID adds a big auto-incrementing primary key column. Default name "id".
func (b *Blueprint) ID(name ...string) *Column {
	n := "id"
	if len(name) > 0 && name[0] != "" {
		n = name[0]
	}
	return b.addCol(&Column{Name: n, Kind: KindBigIncrements, IsPrimary: true, AutoInc: true, UnsignedV: true})
}

// BigIncrements adds a big auto-increment primary key.
func (b *Blueprint) BigIncrements(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindBigIncrements, IsPrimary: true, AutoInc: true, UnsignedV: true})
}

// Increments adds a 32-bit auto-increment primary key.
func (b *Blueprint) Increments(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindIncrements, IsPrimary: true, AutoInc: true, UnsignedV: true})
}

// UUID adds a UUID column (no default generator).
func (b *Blueprint) UUID(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindUUID})
}

// ---------------------- Integers ----------------------

// BigInteger adds a 64-bit signed integer column.
func (b *Blueprint) BigInteger(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindBigInteger})
}

// Integer adds a 32-bit signed integer column.
func (b *Blueprint) Integer(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindInteger})
}

// SmallInteger adds a 16-bit signed integer column.
func (b *Blueprint) SmallInteger(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindSmallInteger})
}

// TinyInteger adds an 8-bit signed integer column.
func (b *Blueprint) TinyInteger(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindTinyInteger})
}

// UnsignedBigInteger adds a 64-bit unsigned integer column.
func (b *Blueprint) UnsignedBigInteger(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindUnsignedBig, UnsignedV: true})
}

// UnsignedInteger adds a 32-bit unsigned integer column.
func (b *Blueprint) UnsignedInteger(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindUnsignedInt, UnsignedV: true})
}

// ---------------------- Floats / Decimal ----------------------

// Float adds a single-precision floating-point column.
func (b *Blueprint) Float(name string) *Column { return b.addCol(&Column{Name: name, Kind: KindFloat}) }

// Double adds a double-precision floating-point column.
func (b *Blueprint) Double(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindDouble})
}

// Decimal adds a fixed-point decimal column. Precision and scale default to
// (8, 2) and may be overridden via the optional precisionScale arguments.
func (b *Blueprint) Decimal(name string, precisionScale ...int) *Column {
	c := &Column{Name: name, Kind: KindDecimal, Precision: 8, Scale: 2}
	if len(precisionScale) > 0 {
		c.Precision = precisionScale[0]
	}
	if len(precisionScale) > 1 {
		c.Scale = precisionScale[1]
	}
	return b.addCol(c)
}

// ---------------------- Strings ----------------------

func (b *Blueprint) String(name string, length ...int) *Column {
	c := &Column{Name: name, Kind: KindString, Length: 255}
	if len(length) > 0 && length[0] > 0 {
		c.Length = length[0]
	}
	return b.addCol(c)
}

func (b *Blueprint) Char(name string, length ...int) *Column {
	c := &Column{Name: name, Kind: KindChar, Length: 1}
	if len(length) > 0 && length[0] > 0 {
		c.Length = length[0]
	}
	return b.addCol(c)
}

// Text adds a variable-length text column.
func (b *Blueprint) Text(name string) *Column { return b.addCol(&Column{Name: name, Kind: KindText}) }

// MediumText adds a medium-length text column (MySQL MEDIUMTEXT).
func (b *Blueprint) MediumText(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindMediumText})
}

// LongText adds a long text column (MySQL LONGTEXT).
func (b *Blueprint) LongText(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindLongText})
}

// Binary adds a binary blob column.
func (b *Blueprint) Binary(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindBinary})
}

// ---------------------- Booleans / JSON ----------------------

// Boolean adds a boolean column.
func (b *Blueprint) Boolean(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindBoolean})
}

// JSON adds a JSON column.
func (b *Blueprint) JSON(name string) *Column { return b.addCol(&Column{Name: name, Kind: KindJSON}) }

// JSONB adds a binary JSON column (Postgres JSONB).
func (b *Blueprint) JSONB(name string) *Column { return b.addCol(&Column{Name: name, Kind: KindJSONB}) }

// ---------------------- Dates / times ----------------------

// Date adds a date-only column.
func (b *Blueprint) Date(name string) *Column { return b.addCol(&Column{Name: name, Kind: KindDate}) }

// DateTime adds a date-and-time column.
func (b *Blueprint) DateTime(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindDateTime})
}

// Time adds a time-only column.
func (b *Blueprint) Time(name string) *Column { return b.addCol(&Column{Name: name, Kind: KindTime}) }

// Timestamp adds a timestamp column.
func (b *Blueprint) Timestamp(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindTimestamp})
}

// TimestampTz adds a timestamp-with-time-zone column.
func (b *Blueprint) TimestampTz(name string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindTimestampTZ})
}

// Timestamps adds nullable created_at and updated_at columns.
func (b *Blueprint) Timestamps() {
	b.addCol(&Column{Name: "created_at", Kind: KindTimestamp, NullableV: true})
	b.addCol(&Column{Name: "updated_at", Kind: KindTimestamp, NullableV: true})
}

// SoftDeletes adds a nullable deleted_at timestamp column used by the ORM's
// soft-delete support. Default name "deleted_at".
func (b *Blueprint) SoftDeletes(name ...string) *Column {
	n := "deleted_at"
	if len(name) > 0 && name[0] != "" {
		n = name[0]
	}
	return b.addCol(&Column{Name: n, Kind: KindTimestamp, NullableV: true})
}

// TimestampsTz adds the same as Timestamps but with TZ.
func (b *Blueprint) TimestampsTz() {
	b.addCol(&Column{Name: "created_at", Kind: KindTimestampTZ, NullableV: true})
	b.addCol(&Column{Name: "updated_at", Kind: KindTimestampTZ, NullableV: true})
}

// ---------------------- Enum / Set ----------------------

// Enum adds an enumerated string column constrained to the allowed values.
// MySQL uses a native ENUM; other dialects store a string with a CHECK
// constraint.
func (b *Blueprint) Enum(name string, allowed ...string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindEnum, Allowed: append([]string(nil), allowed...)})
}

// Set adds a set column. MySQL uses a native SET; other dialects store text
// with a CHECK constraint.
func (b *Blueprint) Set(name string, allowed ...string) *Column {
	return b.addCol(&Column{Name: name, Kind: KindSet, Allowed: append([]string(nil), allowed...)})
}

// ---------------------- Foreign keys ----------------------

// ForeignId adds an UNSIGNED BIG INTEGER column intended to reference another
// table's primary key. Use the returned *ForeignId to chain Constrained()/
// References()/OnDelete()/OnUpdate().
func (b *Blueprint) ForeignId(name string) *ForeignId {
	col := b.UnsignedBigInteger(name)
	return &ForeignId{Column: col, bp: b}
}

// Foreign declares a foreign key on the given local columns.
func (b *Blueprint) Foreign(cols ...string) *Foreign {
	fk := &Foreign{Columns: append([]string(nil), cols...)}
	b.foreigns = append(b.foreigns, fk)
	return fk
}

// ForeignId is the fluent chain used in Laravel-style declarations:
//
//	table.ForeignId("user_id").Constrained("users").OnDelete("cascade")
type ForeignId struct {
	*Column
	bp *Blueprint
}

// Constrained creates a FK on this column referencing the given table's "id".
// If table is empty it is inferred from the column name ("user_id" → "users").
func (f *ForeignId) Constrained(table ...string) *Foreign {
	target := ""
	if len(table) > 0 && table[0] != "" {
		target = table[0]
	} else {
		target = inferReferencedTable(f.Column.Name)
	}
	fk := &Foreign{
		Columns:         []string{f.Column.Name},
		ReferencesTable: target,
		ReferencesCols:  []string{"id"},
	}
	f.bp.foreigns = append(f.bp.foreigns, fk)
	return fk
}

// References starts a FK targeting the named referenced columns. Pair with .On().
func (f *ForeignId) References(cols ...string) *Foreign {
	fk := &Foreign{
		Columns:        []string{f.Column.Name},
		ReferencesCols: append([]string(nil), cols...),
	}
	f.bp.foreigns = append(f.bp.foreigns, fk)
	return fk
}

// Cascade is a convenience: Constrained() + OnDelete("cascade").
func (f *ForeignId) Cascade(table ...string) *Foreign {
	return f.Constrained(table...).OnDelete("cascade")
}

// ---------------------- Indexes ----------------------

// Unique declares a multi-column unique index.
func (b *Blueprint) Unique(cols ...string) *Index {
	idx := &Index{Type: IndexUnique, Columns: append([]string(nil), cols...)}
	b.indexes = append(b.indexes, idx)
	return idx
}

// AddIndex declares a multi-column plain index.
func (b *Blueprint) AddIndex(cols ...string) *Index {
	idx := &Index{Type: IndexPlain, Columns: append([]string(nil), cols...)}
	b.indexes = append(b.indexes, idx)
	return idx
}

// Primary declares a composite primary key.
func (b *Blueprint) Primary(cols ...string) *Index {
	idx := &Index{Type: IndexPrimary, Columns: append([]string(nil), cols...)}
	b.indexes = append(b.indexes, idx)
	return idx
}

// ---------------------- Drop / rename commands ----------------------

// CommandType enumerates deferred blueprint commands.
type CommandType string

const (
	CmdDropColumn   CommandType = "dropColumn"
	CmdRenameColumn CommandType = "renameColumn"
	CmdDropIndex    CommandType = "dropIndex"
	CmdDropForeign  CommandType = "dropForeign"
	CmdRenameTable  CommandType = "renameTable"
)

// Command is a deferred ALTER operation.
type Command struct {
	Type CommandType
	Args []string
}

// DropColumn schedules columns for removal.
func (b *Blueprint) DropColumn(cols ...string) {
	b.commands = append(b.commands, &Command{Type: CmdDropColumn, Args: append([]string(nil), cols...)})
}

// RenameColumn renames a column.
func (b *Blueprint) RenameColumn(from, to string) {
	b.commands = append(b.commands, &Command{Type: CmdRenameColumn, Args: []string{from, to}})
}

// DropIndex drops the named index.
func (b *Blueprint) DropIndex(name string) {
	b.commands = append(b.commands, &Command{Type: CmdDropIndex, Args: []string{name}})
}

// DropForeign drops the named foreign key.
func (b *Blueprint) DropForeign(name string) {
	b.commands = append(b.commands, &Command{Type: CmdDropForeign, Args: []string{name}})
}

// RenameTo renames the table.
func (b *Blueprint) RenameTo(name string) {
	b.commands = append(b.commands, &Command{Type: CmdRenameTable, Args: []string{name}})
}

// ---------------------- helpers ----------------------

func inferReferencedTable(col string) string {
	// "user_id" -> "users", "company_id" -> "companies".
	s := strings.TrimSuffix(col, "_id")
	if s == col {
		return col
	}
	return pluralizeSimple(s)
}

func pluralizeSimple(s string) string {
	if s == "" {
		return s
	}
	switch s[len(s)-1] {
	case 'y':
		return s[:len(s)-1] + "ies"
	case 's', 'x', 'z':
		return s + "es"
	}
	return s + "s"
}

// validate checks that the blueprint is internally consistent. Returns the
// first violation; callers (the migrator) surface this as a hard error.
func (b *Blueprint) validate() error {
	seen := map[string]struct{}{}
	for _, c := range b.columns {
		if c.Name == "" {
			return fmt.Errorf("schema: column without name in table %q", b.table)
		}
		if _, dup := seen[c.Name]; dup {
			return fmt.Errorf("schema: duplicate column %q in table %q", c.Name, b.table)
		}
		seen[c.Name] = struct{}{}
	}
	return nil
}
