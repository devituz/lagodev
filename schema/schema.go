// Package schema provides a fluent table-builder DSL (Blueprint) and a
// driver-agnostic Builder that compiles blueprints into SQL via a Grammar
// implementation provided by the database driver.
//
// Use the package as follows:
//
//	schema.Create("users", func(t *schema.Blueprint) {
//	    t.ID()
//	    t.String("name")
//	    t.String("email").Unique()
//	    t.Timestamps()
//	})
//
// schema.Create returns a *Definition that the migration engine compiles to
// SQL using the connection's Grammar.
package schema

// Definition pairs a blueprint with the kind of statement it represents.
type Definition struct {
	Blueprint *Blueprint
	Op        Operation
}

// Operation enumerates schema operations.
type Operation int

const (
	OpCreate Operation = iota
	OpCreateIfNotExists
	OpAlter
	OpDrop
	OpDropIfExists
	OpRename
)

// Create builds a CREATE TABLE definition.
func Create(table string, fn func(t *Blueprint)) *Definition {
	bp := NewBlueprint(table, true)
	if fn != nil {
		fn(bp)
	}
	return &Definition{Blueprint: bp, Op: OpCreate}
}

// CreateIfNotExists is the IF NOT EXISTS variant.
func CreateIfNotExists(table string, fn func(t *Blueprint)) *Definition {
	d := Create(table, fn)
	d.Op = OpCreateIfNotExists
	return d
}

// Table builds an ALTER TABLE definition.
func Table(table string, fn func(t *Blueprint)) *Definition {
	bp := NewBlueprint(table, false)
	if fn != nil {
		fn(bp)
	}
	return &Definition{Blueprint: bp, Op: OpAlter}
}

// Drop schedules DROP TABLE.
func Drop(table string) *Definition {
	return &Definition{Blueprint: NewBlueprint(table, false), Op: OpDrop}
}

// DropIfExists schedules DROP TABLE IF EXISTS.
func DropIfExists(table string) *Definition {
	return &Definition{Blueprint: NewBlueprint(table, false), Op: OpDropIfExists}
}

// Rename schedules RENAME TABLE from → to.
func Rename(from, to string) *Definition {
	bp := NewBlueprint(from, false)
	bp.RenameTo(to)
	return &Definition{Blueprint: bp, Op: OpRename}
}
