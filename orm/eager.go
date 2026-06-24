package orm

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/internal/reflectutil"
	"github.com/devituz/lagodev/query"
	"github.com/devituz/lagodev/relations"
)

// RelationKind enumerates the relationship kinds an eager load can resolve. It
// mirrors relations.Kind but is re-exported here so model authors declare
// relations without importing the relations package directly.
type RelationKind int

const (
	// HasOne: one child row whose foreign key points back at this model's key.
	HasOne RelationKind = iota
	// HasMany: many child rows keyed by a foreign key back to this model.
	HasMany
	// BelongsTo: the parent row this model's foreign key points at.
	BelongsTo
	// BelongsToMany: many related rows reached through a pivot table.
	BelongsToMany
	// MorphOne: a single polymorphic child (morph_type/morph_id columns).
	MorphOne
	// MorphMany: many polymorphic children (morph_type/morph_id columns).
	MorphMany
)

// RelationDef declares a single named relationship on a model. A model exposes
// its relations by implementing WithRelations; the ORM resolves a string name
// passed to Builder.With into the matching RelationDef and batch-loads it.
//
// Field is the destination struct field (Go field name) the loaded rows are
// written into — a slice field for the *Many kinds, a struct or *struct field
// for the single-row kinds. Related is a zero value of the related model type
// (e.g. Post{}); its schema supplies the related table and is used to allocate
// the destination rows.
//
// The key fields follow the same convention as the relations package: an empty
// ForeignKey/OwnerKey is not inferred here — declare them explicitly. ForeignKey
// is the child column for Has*/Morph* (e.g. "user_id"), or the parent's own
// column for BelongsTo. OwnerKey defaults to "id" when empty.
type RelationDef struct {
	Kind       RelationKind
	Field      string // destination struct field on the parent model
	Related    any    // zero value of the related model type, e.g. Post{}
	ForeignKey string
	OwnerKey   string

	// Pivot configuration (BelongsToMany only).
	PivotTable     string
	PivotForeignFK string
	PivotRelatedFK string

	// Polymorphic configuration (Morph* only): MorphName yields the
	// "<name>_id" / "<name>_type" columns, MorphValue the type discriminator.
	MorphName  string
	MorphValue string
}

// WithRelations is implemented by models that support eager loading via
// Builder.With. It returns a map from relation name to its declaration.
//
//	func (User) Relations() map[string]orm.RelationDef {
//	    return map[string]orm.RelationDef{
//	        "posts": {Kind: orm.HasMany, Field: "Posts", Related: Post{}, ForeignKey: "user_id"},
//	    }
//	}
type WithRelations interface {
	Relations() map[string]RelationDef
}

// withSpec records one requested top-level eager load plus any nested children
// ("posts.comments" becomes name="posts", nested=["comments"]) and an optional
// typed constraint captured by WithWhere.
type withSpec struct {
	name       string
	nested     []string
	constraint func(qb *query.Builder)
}

// With records relations to eager-load after the base query returns. Names may
// be dotted to load nested relations in one call: With("posts", "posts.comments")
// loads each parent's posts, then loads the comments of every loaded post — one
// query per relation level, never N+1. Duplicate names are de-duplicated; the
// order of first appearance is preserved.
func (b *Builder[T]) With(names ...string) *Builder[T] {
	for _, name := range names {
		head, rest, _ := strings.Cut(name, ".")
		head = strings.TrimSpace(head)
		if head == "" {
			continue
		}
		spec := b.findWith(head)
		if spec == nil {
			b.withs = append(b.withs, &withSpec{name: head})
			spec = b.withs[len(b.withs)-1]
		}
		if rest != "" {
			spec.nested = appendUnique(spec.nested, rest)
		}
	}
	return b
}

// WithWhere is the constrained variant of With: it eager-loads a single
// relation while applying a typed constraint to the relation's query. The
// constraint receives the raw *query.Builder for the related table after the
// foreign-key filter (and soft-delete scope) have been applied, so callers add
// WHERE/ORDER BY/LIMIT clauses on top:
//
//	orm.Query[User](conn).
//	    WithWhere("posts", func(q *query.Builder) { q.Where("published", "=", true).OrderBy("id", "desc") }).
//	    Get(ctx, &users)
//
// Nested constraints are not expressed through this method; combine it with
// plain With for the nested levels.
func (b *Builder[T]) WithWhere(name string, constraint func(qb *query.Builder)) *Builder[T] {
	head, rest, _ := strings.Cut(name, ".")
	head = strings.TrimSpace(head)
	if head == "" {
		return b
	}
	spec := b.findWith(head)
	if spec == nil {
		b.withs = append(b.withs, &withSpec{name: head})
		spec = b.withs[len(b.withs)-1]
	}
	spec.constraint = constraint
	if rest != "" {
		spec.nested = appendUnique(spec.nested, rest)
	}
	return b
}

func (b *Builder[T]) findWith(name string) *withSpec {
	for _, s := range b.withs {
		if s.name == name {
			return s
		}
	}
	return nil
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// eagerLoad resolves every recorded top-level relation against the freshly
// hydrated parent slice. Each relation is loaded in a single batched query
// (collect parent keys → WHERE IN → fan the result back to all parents sharing
// a key) and then recurses into its nested children. dst points at the parent
// []T so the assigned relation fields are visible to the caller.
func (b *Builder[T]) eagerLoad(ctx context.Context, dst *[]T) error {
	if len(b.withs) == 0 || len(*dst) == 0 {
		return nil
	}
	parents := make([]any, len(*dst))
	for i := range *dst {
		parents[i] = &(*dst)[i]
	}
	defs, ok := relationDefs[T]()
	if !ok {
		return fmt.Errorf("orm: model %s does not declare Relations(); cannot eager-load", b.schema.Type.Name())
	}
	for _, spec := range b.withs {
		def, found := defs[spec.name]
		if !found {
			return fmt.Errorf("orm: unknown relation %q on %s", spec.name, b.schema.Type.Name())
		}
		if err := loadRelation(ctx, b.conn, parents, def, spec.constraint, spec.nested); err != nil {
			return err
		}
	}
	return nil
}

// relationDefs returns the declared relations for model type T, if it
// implements WithRelations. The lookup is on a zero value so it works for
// value-receiver and pointer-receiver implementations alike.
func relationDefs[T any]() (map[string]RelationDef, bool) {
	var zero T
	if r, ok := any(zero).(WithRelations); ok {
		return r.Relations(), true
	}
	if r, ok := any(&zero).(WithRelations); ok {
		return r.Relations(), true
	}
	return nil, false
}

// loadRelation batch-loads one relation onto every parent, assigns the result
// into the declared destination field, and recurses into nested relations on
// the freshly loaded children.
func loadRelation(
	ctx context.Context,
	conn *database.Connection,
	parents []any,
	def RelationDef,
	constraint func(qb *query.Builder),
	nested []string,
) error {
	if len(parents) == 0 {
		return nil
	}
	parentType := reflectutil.IndirectValue(reflect.ValueOf(parents[0])).Type()
	parentSchema := reflectutil.ParseType(parentType)
	destField := parentSchema.FieldByName(def.Field)
	if destField == nil {
		return fmt.Errorf("orm: relation field %q not found on %s", def.Field, parentType.Name())
	}

	relatedType := reflect.TypeOf(def.Related)
	relatedSchema := reflectutil.ParseType(relatedType)

	rel, err := buildRelation(conn, def, relatedType)
	if err != nil {
		return err
	}
	rel.Apply = relationScope(relatedSchema, constraint)

	// Record which parent fields received children so we can later recurse into
	// the children IN PLACE — nested relations must populate the very structs
	// stored on the parents' fields, not throwaway copies.
	destFields := make([]reflect.Value, 0, len(parents))

	assign := func(parent any, children any) {
		pv := reflectutil.IndirectValue(reflect.ValueOf(parent))
		fv := pv.FieldByIndex(destField.Index)
		setRelationField(fv, children)
		destFields = append(destFields, fv)
	}
	if err := rel.LoadMany(ctx, parents, assign); err != nil {
		return err
	}

	if len(nested) == 0 {
		return nil
	}

	// Recurse: gather addressable pointers to every child as it lives inside a
	// parent's relation field, then load each nested relation in one batch per
	// level. The downstream loader de-duplicates keys for its WHERE IN, so this
	// stays one query per level even when children repeat across parents.
	childDefs, ok := schemaRelationDefs(relatedType)
	if !ok {
		return fmt.Errorf("orm: nested eager load requires %s to declare Relations()", relatedType.Name())
	}
	childParents := collectChildPointers(destFields, relatedType)
	if len(childParents) == 0 {
		return nil
	}
	for _, n := range nested {
		head, rest, _ := strings.Cut(n, ".")
		cdef, found := childDefs[head]
		if !found {
			return fmt.Errorf("orm: unknown nested relation %q on %s", head, relatedType.Name())
		}
		var childNested []string
		if rest != "" {
			childNested = []string{rest}
		}
		if err := loadRelation(ctx, conn, childParents, cdef, nil, childNested); err != nil {
			return err
		}
	}
	return nil
}

// schemaRelationDefs resolves Relations() for an arbitrary related type via a
// freshly allocated value, mirroring relationDefs but without a type parameter.
func schemaRelationDefs(t reflect.Type) (map[string]RelationDef, bool) {
	ptr := reflect.New(t)
	if r, ok := ptr.Interface().(WithRelations); ok {
		return r.Relations(), true
	}
	if r, ok := ptr.Elem().Interface().(WithRelations); ok {
		return r.Relations(), true
	}
	return nil, false
}

// buildRelation translates a declarative RelationDef into a *relations.Relation
// wired to the connection and a destination slice/pointer of the related type.
func buildRelation(conn *database.Connection, def RelationDef, relatedType reflect.Type) (*relations.Relation, error) {
	ownerKey := def.OwnerKey
	if ownerKey == "" {
		ownerKey = "id"
	}
	r := &relations.Relation{
		Conn:       conn,
		OwnerKey:   ownerKey,
		ForeignKey: def.ForeignKey,
	}
	switch def.Kind {
	case HasOne:
		r.Kind = relations.HasOne
		r.ChildSlicePtr = reflect.New(relatedType).Interface()
	case HasMany:
		r.Kind = relations.HasMany
		r.ChildSlicePtr = reflect.New(reflect.SliceOf(relatedType)).Interface()
	case BelongsTo:
		r.Kind = relations.BelongsTo
		r.ChildSlicePtr = reflect.New(relatedType).Interface()
	case BelongsToMany:
		r.Kind = relations.BelongsToMany
		r.ChildSlicePtr = reflect.New(reflect.SliceOf(relatedType)).Interface()
		r.PivotTable = def.PivotTable
		r.PivotForeignFK = def.PivotForeignFK
		r.PivotRelatedFK = def.PivotRelatedFK
	case MorphOne:
		r.Kind = relations.MorphOne
		r.ChildSlicePtr = reflect.New(relatedType).Interface()
		r.ForeignKey = def.MorphName + "_id"
		r.MorphType = def.MorphName + "_type"
		r.MorphValue = def.MorphValue
	case MorphMany:
		r.Kind = relations.MorphMany
		r.ChildSlicePtr = reflect.New(reflect.SliceOf(relatedType)).Interface()
		r.ForeignKey = def.MorphName + "_id"
		r.MorphType = def.MorphName + "_type"
		r.MorphValue = def.MorphValue
	default:
		return nil, fmt.Errorf("orm: unsupported relation kind %d", def.Kind)
	}
	return r, nil
}

// relationScope composes the soft-delete scope of the related model with an
// optional caller constraint into a single Apply hook. Soft-deleted rows are
// excluded by default (deleted_at IS NULL); there is no per-relation WithTrashed
// yet, so a constraint that needs trashed rows must clear the scope itself.
func relationScope(relatedSchema *reflectutil.Schema, constraint func(qb *query.Builder)) func(*query.Builder) {
	if relatedSchema.DeletedAt == nil && constraint == nil {
		return nil
	}
	return func(qb *query.Builder) {
		if relatedSchema.DeletedAt != nil {
			qb.WhereNull(relatedSchema.DeletedAt.Column)
		}
		if constraint != nil {
			constraint(qb)
		}
	}
}

// setRelationField writes loaded children into a parent's relation field. For a
// slice field the children slice is assigned directly; for a struct or *struct
// field the first child (if any) is assigned, matching the one-row kinds.
func setRelationField(fv reflect.Value, children any) {
	if !fv.CanSet() {
		return
	}
	cv := reflect.ValueOf(children)
	switch fv.Kind() {
	case reflect.Slice:
		if cv.IsValid() && cv.Kind() == reflect.Slice {
			fv.Set(cv)
		}
	case reflect.Ptr:
		switch {
		case cv.Kind() == reflect.Slice && cv.Len() > 0:
			elem := cv.Index(0)
			ptr := reflect.New(elem.Type())
			ptr.Elem().Set(elem)
			fv.Set(ptr)
		case cv.IsValid() && cv.Kind() != reflect.Slice && cv.Type().AssignableTo(fv.Type().Elem()):
			ptr := reflect.New(cv.Type())
			ptr.Elem().Set(cv)
			fv.Set(ptr)
		}
	default:
		switch {
		case cv.Kind() == reflect.Slice && cv.Len() > 0:
			fv.Set(cv.Index(0))
		case cv.IsValid() && cv.Type().AssignableTo(fv.Type()):
			fv.Set(cv)
		}
	}
}

// collectChildPointers returns a *Child pointer for every loaded child as it
// lives inside a parent's relation field, so a subsequent nested load mutates
// the children in place (populating user.Posts[i].Comments, not a copy).
//
// No primary-key de-duplication happens here: a child that several parents
// share (a BelongsTo target copied into each parent's field) appears once per
// field instance, and every instance must receive the nested relation. The
// downstream loader already de-duplicates keys for its WHERE IN and fans the
// result back to all instances sharing a key (see collectParentKeys), so
// passing every instance is both correct and free of extra queries.
func collectChildPointers(destFields []reflect.Value, relatedType reflect.Type) []any {
	out := make([]any, 0, len(destFields))
	for _, fv := range destFields {
		switch fv.Kind() {
		case reflect.Slice:
			for i := 0; i < fv.Len(); i++ {
				out = append(out, fv.Index(i).Addr().Interface())
			}
		case reflect.Ptr:
			if !fv.IsNil() {
				out = append(out, fv.Interface())
			}
		default:
			if fv.CanAddr() {
				out = append(out, fv.Addr().Interface())
			}
		}
	}
	return out
}
