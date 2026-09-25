// Package relations implements model relationships with eager-loading.
// Relationships are defined as free functions that take the parent and a
// connection and return a *Relation; the relation can then be loaded
// synchronously (Load) or attached to a batch via LoadMany.
package relations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/devituz/lagodev/casts"
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

	// Apply, when set, is invoked on the child query builder before execution.
	// It lets callers layer extra constraints onto an eager load — a soft-delete
	// scope (deleted_at IS NULL), an additional WHERE, an ORDER BY, a LIMIT — on
	// top of the relation's own foreign-key filter. It is applied by the
	// query.Builder-based loaders (HasOne/HasMany/Morph*/BelongsTo); the
	// BelongsToMany loader builds raw SQL and does not honor it.
	Apply func(qb *query.Builder)
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
	tableName := childTableName(childType, childSchema)

	qb := query.New(r.Conn, tableName).WhereIn(r.ForeignKey, pkIDs)
	if r.Kind == MorphOne || r.Kind == MorphMany {
		qb.Where(r.MorphType, "=", r.MorphValue)
	}
	if r.Apply != nil {
		r.Apply(qb)
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
		child, _, err := scanChild(rows, cols, childType, childSchema, "")
		if err != nil {
			return err
		}
		fkField := childSchema.FieldByColumn(r.ForeignKey)
		if fkField == nil {
			return fmt.Errorf("relations: foreign key %q not found on child", r.ForeignKey)
		}
		key := normalizeKey(child.FieldByIndex(fkField.Index).Interface())
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
	tableName := childTableName(childType, childSchema)
	qb := query.New(r.Conn, tableName).WhereIn(r.OwnerKey, keys)
	if r.Apply != nil {
		r.Apply(qb)
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
	ownerField := childSchema.FieldByColumn(r.OwnerKey)
	if ownerField == nil {
		return fmt.Errorf("relations: owner key %q not found on related model", r.OwnerKey)
	}
	results := map[any]reflect.Value{}
	for rows.Next() {
		child, _, err := scanChild(rows, cols, childType, childSchema, "")
		if err != nil {
			return err
		}
		k := normalizeKey(child.FieldByIndex(ownerField.Index).Interface())
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
	childTable := childTableName(childType, childSchema)
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
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	sliceType := reflect.SliceOf(childType)
	buckets := map[any]reflect.Value{}
	for rows.Next() {
		child, rawParentKey, err := scanChild(rows, cols, childType, childSchema, "__parent_fk")
		if err != nil {
			return err
		}
		// The pivot FK arrives as the driver's type (int64, or []byte on
		// MySQL) while parent keys carry the model's field type (uint64 for
		// orm.Model); normalize so they land in the same bucket.
		parentKey := normalizeKey(rawParentKey)
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

// scanChild scans the current row into a fresh value of childType. Columns are
// read through *any holders so SQL NULL coalesces to the field's zero value and
// `cast` tags are honored, matching orm's own hydration; scanning straight into
// the fields failed on any nullable column ("converting NULL to string is
// unsupported"). When extraCol is non-empty, that column's raw value is
// returned instead of being assigned to the child.
func scanChild(rows *sql.Rows, cols []string, childType reflect.Type, schema *reflectutil.Schema, extraCol string) (reflect.Value, any, error) {
	child := reflect.New(childType).Elem()
	holders := make([]any, len(cols))
	for i := range cols {
		holders[i] = new(any)
	}
	if err := rows.Scan(holders...); err != nil {
		return child, nil, err
	}
	var extra any
	for i, c := range cols {
		raw := *(holders[i].(*any))
		if extraCol != "" && c == extraCol {
			extra = raw
			continue
		}
		f := schema.FieldByColumn(c)
		if f == nil {
			continue
		}
		fv := child.FieldByIndex(f.Index)
		if f.Cast != "" {
			if cst := casts.Get(f.Cast); cst != nil {
				if err := cst.FromDB(raw, fv.Addr().Interface()); err != nil {
					return child, nil, fmt.Errorf("relations: cast %s on %s: %w", f.Cast, f.Column, err)
				}
			}
			continue
		}
		if err := reflectutil.AssignScanned(fv, raw); err != nil {
			return child, nil, fmt.Errorf("relations: scan %s: %w", f.Column, err)
		}
	}
	return child, extra, nil
}

// normalizeKey maps a key value to a canonical comparable form so parent keys
// and child/pivot keys match in map lookups regardless of their Go type: a
// uint64 model ID, an int FK field and the int64 (or []byte) a driver returns
// must all compare equal. Integers become int64, numeric strings/bytes are
// parsed, other strings stay strings, and nil pointers become nil.
func normalizeKey(v any) any {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if u := rv.Uint(); u <= math.MaxInt64 {
			return int64(u)
		}
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		if f := rv.Float(); f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
			return int64(f)
		}
		return rv.Float()
	case reflect.String:
		return normalizeStringKey(rv.String())
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return normalizeStringKey(string(rv.Bytes()))
		}
	}
	if rv.IsValid() && rv.Type().Comparable() {
		return rv.Interface()
	}
	return v
}

func normalizeStringKey(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && strconv.FormatInt(n, 10) == s {
		return n
	}
	return s
}

// tabler mirrors orm.Tabler so a related model can override its inferred table
// name without relations importing the orm package (which would invert the
// dependency direction). The override is consulted via a freshly allocated
// zero value of the child type.
type tabler interface{ TableName() string }

// childTableName resolves the table name for a child model type, honoring a
// TableName() override when the type implements it, and falling back to the
// schema's inflected default.
func childTableName(childType reflect.Type, childSchema *reflectutil.Schema) string {
	if t, ok := reflect.New(childType).Interface().(tabler); ok {
		return t.TableName()
	}
	return childSchema.Table
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
		key := normalizeKey(v.FieldByIndex(f.Index).Interface())
		if key == nil {
			// A NULL (nil pointer) key cannot match any related row.
			continue
		}
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
