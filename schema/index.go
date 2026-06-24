package schema

// IndexType enumerates supported index types.
type IndexType string

const (
	IndexPlain   IndexType = "index"
	IndexUnique  IndexType = "unique"
	IndexPrimary IndexType = "primary"
	IndexSpatial IndexType = "spatial"
	IndexFull    IndexType = "fulltext"
)

// Index describes a table index.
type Index struct {
	NameV   string
	Type    IndexType
	Columns []string
	Algo    string // optional: btree/hash/gin/gist (Postgres)
	Drop    bool
}

// Name sets an explicit index name, overriding the generated one. Use this to
// target the index later via Blueprint.DropIndex.
func (i *Index) Name(name string) *Index { i.NameV = name; return i }

// Foreign describes a foreign-key constraint.
type Foreign struct {
	NameV           string
	Columns         []string
	ReferencesTable string
	ReferencesCols  []string
	OnDeleteAction  string
	OnUpdateAction  string
	Drop            bool
}

// Name sets an explicit constraint name, overriding the generated one. Use this
// to target the foreign key later via Blueprint.DropForeign.
func (f *Foreign) Name(name string) *Foreign { f.NameV = name; return f }

// On targets the referenced table.
func (f *Foreign) On(table string) *Foreign { f.ReferencesTable = table; return f }

// References sets the referenced columns.
func (f *Foreign) References(cols ...string) *Foreign {
	f.ReferencesCols = append(f.ReferencesCols[:0], cols...)
	return f
}

// OnDelete sets ON DELETE behavior ("cascade", "restrict", "set null", "no action").
func (f *Foreign) OnDelete(action string) *Foreign { f.OnDeleteAction = action; return f }

// OnUpdate sets ON UPDATE behavior.
func (f *Foreign) OnUpdate(action string) *Foreign { f.OnUpdateAction = action; return f }

// CascadeOnDelete is sugar for OnDelete("cascade").
func (f *Foreign) CascadeOnDelete() *Foreign { return f.OnDelete("cascade") }

// RestrictOnDelete is sugar for OnDelete("restrict").
func (f *Foreign) RestrictOnDelete() *Foreign { return f.OnDelete("restrict") }

// NullOnDelete is sugar for OnDelete("set null").
func (f *Foreign) NullOnDelete() *Foreign { return f.OnDelete("set null") }
