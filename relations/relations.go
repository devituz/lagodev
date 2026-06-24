// Package relations implements model relationships with eager-loading.
// Relationships are defined as free functions that take the parent and a
// connection and return a *Relation; the relation can then be loaded
// synchronously (Load) or attached to a batch via LoadMany.
package relations

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/internal/inflect"
	"github.com/devituz/lagodev/internal/reflectutil"
	"github.com/devituz/lagodev/query"
)

// Kind enumerates relationship kinds.
type Kind int

const (
	HasOne Kind = iota
	HasMany
	BelongsTo
	BelongsToMany
	MorphOne
	MorphMany
)

// Relation describes a relationship from a parent model to one or more children.
type Relation struct {
	Kind           Kind
	Conn           *database.Connection
	ParentModel    any
	ChildSlicePtr  any // *[]Child (HasMany / BelongsToMany / MorphMany) OR *Child for one-row kinds.
	ForeignKey     string
	OwnerKey       string
	PivotTable     string // for BelongsToMany
	PivotForeignFK string // pivot FK to parent
	PivotRelatedFK string // pivot FK to child
	MorphType      string // for polymorphic: column holding child model type
	MorphID        string // for polymorphic: column holding child model id
	MorphValue     string // expected type string in MorphType column
}

// Load executes the relation against a single parent and writes the result
// into the relation's destination pointer.
func (r *Relation) Load(ctx context.Context) error {
	if r.ChildSlicePtr == nil {
		return errors.New("relations: destination pointer required")
	}
	return r.load(ctx, []any{r.ParentModel})
}

// LoadMany loads the relation for a batch of parents in one query and
// distributes the result back via the provided assignFn callback. assignFn
// receives the parent pointer and the matching children slice (any).
func (r *Relation) LoadMany(ctx context.Context, parents []any, assignFn func(parent any, children any)) error {
	if assignFn == nil {
		return errors.New("relations: assignFn required for LoadMany")
	}
	return r.loadMany(ctx, parents, assignFn)
}

func (r *Relation) load(ctx context.Context, parents []any) error {
	// Single-parent loading simply delegates to loadMany with an inline
	// assignment captured in the destination pointer.
	dst := reflect.ValueOf(r.ChildSlicePtr).Elem()
	return r.loadMany(ctx, parents, func(_ any, children any) {
		cv := reflect.ValueOf(children)
		switch {
		case dst.Kind() == reflect.Slice:
			dst.Set(cv)
		case cv.Kind() == reflect.Slice && cv.Len() > 0:
			dst.Set(cv.Index(0))
		case cv.IsValid() && cv.Type().AssignableTo(dst.Type()):
			// Single child returned (BelongsTo with a scalar destination).
			dst.Set(cv)
		}
	})
}

func (r *Relation) loadMany(ctx context.Context, parents []any, assign func(parent any, children any)) error {
	if len(parents) == 0 {
		return nil
	}
	switch r.Kind {
	case HasOne, HasMany, MorphOne, MorphMany:
		return r.loadHasOrMorph(ctx, parents, assign)
	case BelongsTo:
		return r.loadBelongsTo(ctx, parents, assign)
	case BelongsToMany:
		return r.loadBelongsToMany(ctx, parents, assign)
	}
	return fmt.Errorf("relations: unsupported kind %d", r.Kind)
}

func (r *Relation) loadHasOrMorph(ctx context.Context, parents []any, assign func(any, any)) error {
	if r.OwnerKey == "" {
		r.OwnerKey = "id"
	}
	pkIDs, parentLookup := collectParentKeys(parents, r.OwnerKey)
	if len(pkIDs) == 0 {
		return nil
	}
	childType := childElemType(r.ChildSlicePtr)
	childSchema := reflectutil.ParseType(childType)
	tableName := childSchema.Table

	qb := query.New(r.Conn, tableName).WhereIn(r.ForeignKey, pkIDs)
	if r.Kind == MorphOne || r.Kind == MorphMany {
		qb.Where(r.MorphType, "=", r.MorphValue)
	}
	rows, err := qb.Get(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	// Allocate a buckets map keyed by FK value.
	buckets := map[any]reflect.Value{} // []Child per parent
	sliceType := reflect.SliceOf(childType)
	for rows.Next() {
		child := reflect.New(childType).Elem()
		scanTargets := make([]any, len(cols))
		for i, c := range cols {
			f := childSchema.FieldByColumn(c)
			if f == nil {
				var raw any
				scanTargets[i] = &raw
				continue
			}
			scanTargets[i] = child.FieldByIndex(f.Index).Addr().Interface()
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return err
		}
		fkField := childSchema.FieldByColumn(r.ForeignKey)
		if fkField == nil {
			return fmt.Errorf("relations: foreign key %q not found on child", r.ForeignKey)
		}
		key := child.FieldByIndex(fkField.Index).Interface()
		bucket, ok := buckets[key]
		if !ok {
			bucket = reflect.MakeSlice(sliceType, 0, 1)
		}
		bucket = reflect.Append(bucket, child)
		buckets[key] = bucket
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for parentKey, group := range parentLookup {
		bucket, ok := buckets[parentKey]
		if !ok {
			bucket = reflect.MakeSlice(sliceType, 0, 0)
		}
		for _, parent := range group {
			assign(parent, bucket.Interface())
		}
	}
	return nil
}

func (r *Relation) loadBelongsTo(ctx context.Context, parents []any, assign func(any, any)) error {
	if r.OwnerKey == "" {
		r.OwnerKey = "id"
	}
	keys, parentLookup := collectParentKeys(parents, r.ForeignKey)
	if len(keys) == 0 {
		return nil
	}
	childType := childElemType(r.ChildSlicePtr)
	childSchema := reflectutil.ParseType(childType)
	tableName := childSchema.Table
	rows, err := query.New(r.Conn, tableName).WhereIn(r.OwnerKey, keys).Get(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	results := map[any]reflect.Value{}
	for rows.Next() {
		child := reflect.New(childType).Elem()
		scanTargets := make([]any, len(cols))
		for i, c := range cols {
			f := childSchema.FieldByColumn(c)
			if f == nil {
				var raw any
				scanTargets[i] = &raw
				continue
			}
			scanTargets[i] = child.FieldByIndex(f.Index).Addr().Interface()
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return err
		}
		ownerField := childSchema.FieldByColumn(r.OwnerKey)
		k := child.FieldByIndex(ownerField.Index).Interface()
		results[k] = child
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for key, group := range parentLookup {
		if c, ok := results[key]; ok {
			for _, parent := range group {
				assign(parent, c.Interface())
			}
		}
	}
	return nil
}

func (r *Relation) loadBelongsToMany(ctx context.Context, parents []any, assign func(any, any)) error {
	if r.OwnerKey == "" {
		r.OwnerKey = "id"
	}
	keys, parentLookup := collectParentKeys(parents, r.OwnerKey)
	if len(keys) == 0 {
		return nil
	}
	childType := childElemType(r.ChildSlicePtr)
	childSchema := reflectutil.ParseType(childType)
	g := r.Conn.Grammar
	childTable := childSchema.Table
	q := fmt.Sprintf(
		"SELECT %s.*, %s.%s AS __parent_fk FROM %s INNER JOIN %s ON %s.%s = %s.%s WHERE %s.%s IN (",
		g.Quote(childTable),
		g.Quote(r.PivotTable), g.Quote(r.PivotForeignFK),
		g.Quote(childTable),
		g.Quote(r.PivotTable),
		g.Quote(r.PivotTable), g.Quote(r.PivotRelatedFK),
		g.Quote(childTable), g.Quote("id"),
		g.Quote(r.PivotTable), g.Quote(r.PivotForeignFK),
	)
	placeholders := make([]string, len(keys))
	for i := range keys {
		placeholders[i] = g.Placeholder(i + 1)
	}
	q += strings.Join(placeholders, ", ") + ")"
	rows, err := r.Conn.QueryContext(ctx, q, keys...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	sliceType := reflect.SliceOf(childType)
	buckets := map[any]reflect.Value{}
	for rows.Next() {
		child := reflect.New(childType).Elem()
		var parentKey any
		scanTargets := make([]any, len(cols))
		for i, c := range cols {
			if c == "__parent_fk" {
				scanTargets[i] = &parentKey
				continue
			}
			f := childSchema.FieldByColumn(c)
			if f == nil {
				var raw any
				scanTargets[i] = &raw
				continue
			}
			scanTargets[i] = child.FieldByIndex(f.Index).Addr().Interface()
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return err
		}
		bucket, ok := buckets[parentKey]
		if !ok {
			bucket = reflect.MakeSlice(sliceType, 0, 1)
		}
		bucket = reflect.Append(bucket, child)
		buckets[parentKey] = bucket
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for key, group := range parentLookup {
		bucket, ok := buckets[key]
		if !ok {
			bucket = reflect.MakeSlice(sliceType, 0, 0)
		}
		for _, parent := range group {
			assign(parent, bucket.Interface())
		}
	}
	return nil
}

// childElemType returns the element type of the destination slice/pointer.
func childElemType(dst any) reflect.Type {
	t := reflect.TypeOf(dst)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// collectParentKeys returns the distinct key values to query for and a lookup
// from each key to ALL parents carrying that key. The lookup is a slice per
// key because the key column may be non-unique across parents — most notably a
// BelongsTo keyed by a shared foreign key, where several children point at the
// same parent. Keying by a single parent would silently drop all but the last.
func collectParentKeys(parents []any, col string) ([]any, map[any][]any) {
	keys := make([]any, 0, len(parents))
	seen := make(map[any]struct{}, len(parents))
	lookup := make(map[any][]any, len(parents))
	for _, p := range parents {
		v := reflectutil.IndirectValue(reflect.ValueOf(p))
		s := reflectutil.ParseType(v.Type())
		f := s.FieldByColumn(col)
		if f == nil {
			continue
		}
		key := v.FieldByIndex(f.Index).Interface()
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		lookup[key] = append(lookup[key], p)
	}
	return keys, lookup
}

// HasManyOf builds a HasMany relation: parent.id == children.<parentFK>.
func HasManyOf(conn *database.Connection, parent any, children any, parentFK string) *Relation {
	if parentFK == "" {
		parentFK = inflect.ForeignKey(reflectutil.ParseType(reflect.TypeOf(parent)).Type.Name())
	}
	return &Relation{
		Kind:          HasMany,
		Conn:          conn,
		ParentModel:   parent,
		ChildSlicePtr: children,
		ForeignKey:    parentFK,
		OwnerKey:      "id",
	}
}

// HasOneOf builds a HasOne relation.
func HasOneOf(conn *database.Connection, parent any, child any, parentFK string) *Relation {
	if parentFK == "" {
		parentFK = inflect.ForeignKey(reflectutil.ParseType(reflect.TypeOf(parent)).Type.Name())
	}
	return &Relation{
		Kind:          HasOne,
		Conn:          conn,
		ParentModel:   parent,
		ChildSlicePtr: child,
		ForeignKey:    parentFK,
		OwnerKey:      "id",
	}
}

// BelongsToOf builds a BelongsTo relation.
func BelongsToOf(conn *database.Connection, parent any, owner any, foreignKey string) *Relation {
	if foreignKey == "" {
		foreignKey = inflect.ForeignKey(reflectutil.ParseType(reflect.TypeOf(owner)).Type.Name())
	}
	return &Relation{
		Kind:          BelongsTo,
		Conn:          conn,
		ParentModel:   parent,
		ChildSlicePtr: owner,
		ForeignKey:    foreignKey,
		OwnerKey:      "id",
	}
}

// BelongsToManyOf builds a many-to-many relation through pivot table.
//
// NOTE: the related (child) key is currently hardcoded to "id" in the pivot
// JOIN (see loadBelongsToMany); models whose primary key column is not "id"
// are not yet supported by this relation.
func BelongsToManyOf(conn *database.Connection, parent, children any, pivot, parentFK, relatedFK string) *Relation {
	return &Relation{
		Kind:           BelongsToMany,
		Conn:           conn,
		ParentModel:    parent,
		ChildSlicePtr:  children,
		PivotTable:     pivot,
		PivotForeignFK: parentFK,
		PivotRelatedFK: relatedFK,
		OwnerKey:       "id",
	}
}

// MorphManyOf builds a polymorphic HasMany relation; the child rows must have
// `morph_type` and `morph_id` columns.
func MorphManyOf(conn *database.Connection, parent, children any, morphName, morphValue string) *Relation {
	return &Relation{
		Kind:          MorphMany,
		Conn:          conn,
		ParentModel:   parent,
		ChildSlicePtr: children,
		ForeignKey:    morphName + "_id",
		MorphType:     morphName + "_type",
		MorphValue:    morphValue,
		OwnerKey:      "id",
	}
}
