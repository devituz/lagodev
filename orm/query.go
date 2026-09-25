package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/devituz/lagodev/casts"
	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/internal/reflectutil"
	"github.com/devituz/lagodev/query"
)

// ErrNotFound is returned by First/FirstOrFail when no row matches.
var ErrNotFound = errors.New("orm: record not found")

// trashedMode controls how the soft-delete scope is applied at execution time.
type trashedMode int

const (
	// trashedDefault excludes soft-deleted rows (deleted_at IS NULL).
	trashedDefault trashedMode = iota
	// trashedWith includes soft-deleted rows (no scope).
	trashedWith
	// trashedOnly returns only soft-deleted rows (deleted_at IS NOT NULL).
	trashedOnly
)

// Builder is a model-aware wrapper around query.Builder.
type Builder[T any] struct {
	conn     *database.Connection
	executor database.Executor
	schema   *reflectutil.Schema
	qb       *query.Builder
	trashed  trashedMode
	withs    []*withSpec
}

// Query returns a fresh model builder.
func Query[T any](conn *database.Connection) *Builder[T] {
	var zero T
	schema := reflectutil.Parse(&zero)
	tableName := schema.Table
	if tbl, ok := any(&zero).(Tabler); ok {
		tableName = tbl.TableName()
	}
	qb := query.New(conn, tableName)
	return &Builder[T]{
		conn:     conn,
		executor: conn,
		schema:   schema,
		qb:       qb,
	}
}

// WithTx pins the builder to a transaction.
func (b *Builder[T]) WithTx(tx *database.Tx) *Builder[T] {
	b.executor = tx
	b.qb.SetExecutor(tx)
	return b
}

// Where appends a WHERE clause.
func (b *Builder[T]) Where(args ...any) *Builder[T] { b.qb.Where(args...); return b }

// OrWhere appends an OR WHERE.
func (b *Builder[T]) OrWhere(args ...any) *Builder[T] { b.qb.OrWhere(args...); return b }

// WhereIn appends an IN constraint.
func (b *Builder[T]) WhereIn(col string, values any) *Builder[T] {
	b.qb.WhereIn(col, values)
	return b
}

// WhereNull appends an IS NULL constraint.
func (b *Builder[T]) WhereNull(col string) *Builder[T] { b.qb.WhereNull(col); return b }

// WhereNotNull appends IS NOT NULL.
func (b *Builder[T]) WhereNotNull(col string) *Builder[T] { b.qb.WhereNotNull(col); return b }

// OrderBy appends ORDER BY.
func (b *Builder[T]) OrderBy(col, dir string) *Builder[T] { b.qb.OrderBy(col, dir); return b }

// Limit applies a LIMIT.
func (b *Builder[T]) Limit(n int) *Builder[T] { b.qb.Limit(n); return b }

// Offset applies an OFFSET.
func (b *Builder[T]) Offset(n int) *Builder[T] { b.qb.Offset(n); return b }

// WithTrashed includes soft-deleted rows in the results. No-op on models that
// are not soft-deletable.
func (b *Builder[T]) WithTrashed() *Builder[T] { b.trashed = trashedWith; return b }

// OnlyTrashed restricts the results to soft-deleted rows only. No-op on models
// that are not soft-deletable.
func (b *Builder[T]) OnlyTrashed() *Builder[T] { b.trashed = trashedOnly; return b }

// Scope applies a reusable constraint to the builder and returns it, so common
// query fragments can be factored into a named function:
//
//	active := func(q *orm.Builder[User]) *orm.Builder[User] { return q.Where("active", true) }
//	orm.Query[User](conn).Scope(active).Get(ctx, &users)
func (b *Builder[T]) Scope(fn func(*Builder[T]) *Builder[T]) *Builder[T] {
	if fn == nil {
		return b
	}
	return fn(b)
}

// Raw query builder access.
func (b *Builder[T]) QB() *query.Builder { return b.qb }

// scopedQB returns a clone of the underlying query builder with the soft-delete
// scope applied according to the current trashed mode. Non-soft-deletable
// models are returned unchanged. The clone keeps the receiver reusable across
// terminal calls.
func (b *Builder[T]) scopedQB() *query.Builder {
	// Group the caller's conditions first: otherwise the scope (and any
	// cursor/key condition appended later) binds only to the last OR branch —
	// Where(a).OrWhere(b) + scope compiled to "a OR b AND deleted_at IS NULL",
	// leaking soft-deleted rows that match a.
	qb := b.qb.Clone().WrapWheres()
	if b.schema.DeletedAt == nil {
		return qb
	}
	col := b.schema.DeletedAt.Column
	switch b.trashed {
	case trashedDefault:
		qb.WhereNull(col)
	case trashedOnly:
		qb.WhereNotNull(col)
	case trashedWith:
		// include all rows: no scope
	}
	return qb
}

// Get executes the query and populates dst (must be *[]T).
func (b *Builder[T]) Get(ctx context.Context, dst *[]T) error {
	rows, err := b.scopedQB().Get(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()
	if err := hydrateRows[T](ctx, b.conn, rows, b.schema, dst); err != nil {
		return err
	}
	rows.Close()
	return b.eagerLoad(ctx, dst)
}

// First returns the first matching row, or ErrNotFound. It runs against a
// clone of the underlying builder so the receiver keeps its full clause state
// and can still be used for Get/Count afterwards.
func (b *Builder[T]) First(ctx context.Context) (*T, error) {
	rows, err := b.scopedQB().Limit(1).Get(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	if err := hydrateRows[T](ctx, b.conn, rows, b.schema, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	rows.Close()
	if err := b.eagerLoad(ctx, &out); err != nil {
		return nil, err
	}
	return &out[0], nil
}

// FirstOrFail is an alias for First: it returns the first matching row or
// ErrNotFound. Provided for call-site readability when the absence of a row is
// an error condition.
func (b *Builder[T]) FirstOrFail(ctx context.Context) (*T, error) { return b.First(ctx) }

// Find by primary key. It clones the underlying builder so repeated Find calls
// on the same receiver (Find(id1) then Find(id2)) do not accumulate WHERE
// clauses.
func (b *Builder[T]) Find(ctx context.Context, id any) (*T, error) {
	if b.schema.PrimaryKey == nil {
		return nil, errors.New("orm: no primary key on model")
	}
	rows, err := b.scopedQB().Where(b.schema.PrimaryKey.Column, "=", id).Limit(1).Get(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	if err := hydrateRows[T](ctx, b.conn, rows, b.schema, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	rows.Close()
	if err := b.eagerLoad(ctx, &out); err != nil {
		return nil, err
	}
	return &out[0], nil
}

// Count rows matching the query (honoring the soft-delete scope).
func (b *Builder[T]) Count(ctx context.Context) (int64, error) {
	return b.scopedQB().Count(ctx)
}

// Exists reports whether any matching row exists (honoring the soft-delete scope).
func (b *Builder[T]) Exists(ctx context.Context) (bool, error) {
	return b.scopedQB().Exists(ctx)
}

// Pluck extracts a single column as []V.
func Pluck[T any, V any](ctx context.Context, b *Builder[T], col string) ([]V, error) {
	qb := b.scopedQB()
	qb.Select(col)
	rows, err := qb.Get(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V
	for rows.Next() {
		var v V
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// hydrateRows populates dst from rows, applying casts and AfterFind.
func hydrateRows[T any](ctx context.Context, conn *database.Connection, rows *sql.Rows, schema *reflectutil.Schema, dst *[]T) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	// Scan every column through an *any intermediary. This lets us coalesce a
	// SQL NULL (which arrives as a nil any) to the Go zero value before
	// assigning into a non-pointer field — scanning NULL directly into e.g. a
	// *string errors with "converting NULL to string is unsupported".
	scanTargets := make([]any, len(cols))
	holders := make([]any, len(cols))
	for rows.Next() {
		var row T
		v := reflect.ValueOf(&row).Elem()
		for i := range cols {
			holders[i] = new(any)
			scanTargets[i] = holders[i]
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return err
		}
		for i, c := range cols {
			f := schema.FieldByColumn(c)
			if f == nil {
				continue
			}
			raw := *(holders[i].(*any))
			fv := v.FieldByIndex(f.Index)
			if f.Cast != "" {
				if cst := casts.Get(f.Cast); cst != nil {
					if err := cst.FromDB(raw, fv.Addr().Interface()); err != nil {
						return fmt.Errorf("orm: cast %s on %s: %w", f.Cast, f.Column, err)
					}
				}
				continue
			}
			if err := assignScanned(fv, raw); err != nil {
				return fmt.Errorf("orm: scan %s: %w", f.Column, err)
			}
		}
		*dst = append(*dst, row)
		// AfterFind runs on the stored element so mutations made by the hook
		// are visible to the caller.
		if err := dispatchHook(&(*dst)[len(*dst)-1], "AfterFind", &HookContext{Ctx: ctx, Conn: conn}); err != nil {
			return err
		}
	}
	return rows.Err()
}

// assignScanned writes a value scanned from the DB into the destination
// field; see reflectutil.AssignScanned.
func assignScanned(fv reflect.Value, raw any) error {
	return reflectutil.AssignScanned(fv, raw)
}

// Save persists a model: it inserts when the primary key is zero, otherwise
// updates. Hooks are dispatched around the operation. Timestamps are set
// automatically.
func Save[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	v := reflect.ValueOf(model).Elem()
	hctx := &HookContext{Ctx: ctx, Conn: conn}

	tableName := tableNameFor(model, schema)

	// Decide whether this is a create or an update. A zero key means create.
	// A caller-assigned key on a non-auto-increment primary key (UUID,
	// natural key) says nothing about whether the row exists yet, so look it
	// up; treating it as an update silently dropped every new row. A model
	// without a primary key can only be inserted.
	pk := schema.PrimaryKey
	isCreate := pk == nil
	if pk != nil {
		fv := v.FieldByIndex(pk.Index)
		switch {
		case fv.IsZero():
			isCreate = true
		case !pk.IsAutoIncrement:
			exists, err := query.New(conn, tableName).Where(pk.Column, "=", fv.Interface()).Exists(ctx)
			if err != nil {
				return err
			}
			isCreate = !exists
		}
	}

	if isCreate {
		now := conn.Now()
		if schema.CreatedAt != nil {
			cv := v.FieldByIndex(schema.CreatedAt.Index)
			if cv.IsZero() {
				cv.Set(reflect.ValueOf(now))
			}
		}
		if schema.UpdatedAt != nil {
			uv := v.FieldByIndex(schema.UpdatedAt.Index)
			if uv.IsZero() {
				uv.Set(reflect.ValueOf(now))
			}
		}
		if err := dispatchHook(model, "BeforeSave", hctx); err != nil {
			return err
		}
		if err := dispatchHook(model, "BeforeCreate", hctx); err != nil {
			return err
		}
		values, err := collectValues(schema, v, true)
		if err != nil {
			return err
		}
		if pk != nil && v.FieldByIndex(pk.Index).IsZero() && isIntegerKind(pk.Type.Kind()) {
			id, err := query.New(conn, tableName).InsertGetID(ctx, values, pk.Column)
			if err != nil {
				return err
			}
			if pkVal := v.FieldByIndex(pk.Index); pkVal.CanSet() {
				pkVal.Set(reflect.ValueOf(id).Convert(pkVal.Type()))
			}
		} else if _, err := query.New(conn, tableName).Insert(ctx, values); err != nil {
			// Caller-assigned (or absent) key: nothing to read back.
			return err
		}
		if err := dispatchHook(model, "AfterCreate", hctx); err != nil {
			return err
		}
		return dispatchHook(model, "AfterSave", hctx)
	}

	if schema.UpdatedAt != nil {
		v.FieldByIndex(schema.UpdatedAt.Index).Set(reflect.ValueOf(conn.Now()))
	}
	if err := dispatchHook(model, "BeforeSave", hctx); err != nil {
		return err
	}
	if err := dispatchHook(model, "BeforeUpdate", hctx); err != nil {
		return err
	}
	values, err := collectUpdateValues(schema, v)
	if err != nil {
		return err
	}
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	if _, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Update(ctx, values); err != nil {
		return err
	}
	if err := dispatchHook(model, "AfterUpdate", hctx); err != nil {
		return err
	}
	return dispatchHook(model, "AfterSave", hctx)
}

// Delete removes the model's row from the database. For soft-deletable models
// (those carrying a deleted_at column) it issues an UPDATE that stamps
// deleted_at instead of a real DELETE; use ForceDelete for an unconditional
// removal.
func Delete[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	v := reflect.ValueOf(model).Elem()
	if schema.PrimaryKey == nil {
		return errors.New("orm: cannot delete model without a primary key")
	}
	hctx := &HookContext{Ctx: ctx, Conn: conn}
	if err := dispatchHook(model, "BeforeDelete", hctx); err != nil {
		return err
	}
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	tableName := tableNameFor(model, schema)

	if schema.DeletedAt != nil {
		now := conn.Now()
		if _, err := query.New(conn, tableName).
			Where(schema.PrimaryKey.Column, "=", pkVal).
			Update(ctx, map[string]any{schema.DeletedAt.Column: now}); err != nil {
			return err
		}
		// Reflect the change back onto the in-memory model.
		dv := v.FieldByIndex(schema.DeletedAt.Index)
		setDeletedAt(dv, &now)
		return dispatchHook(model, "AfterDelete", hctx)
	}

	if _, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Delete(ctx); err != nil {
		return err
	}
	return dispatchHook(model, "AfterDelete", hctx)
}

// ForceDelete permanently removes the model's row, bypassing soft deletes.
func ForceDelete[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	v := reflect.ValueOf(model).Elem()
	if schema.PrimaryKey == nil {
		return errors.New("orm: cannot delete model without a primary key")
	}
	hctx := &HookContext{Ctx: ctx, Conn: conn}
	if err := dispatchHook(model, "BeforeDelete", hctx); err != nil {
		return err
	}
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	tableName := tableNameFor(model, schema)
	if _, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Delete(ctx); err != nil {
		return err
	}
	return dispatchHook(model, "AfterDelete", hctx)
}

// Restore clears the deleted_at column of a soft-deleted model, bringing the
// row back into the default query scope. It is an error to call Restore on a
// model that is not soft-deletable.
func Restore[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	v := reflect.ValueOf(model).Elem()
	if schema.PrimaryKey == nil {
		return errors.New("orm: cannot restore model without a primary key")
	}
	if schema.DeletedAt == nil {
		return errors.New("orm: Restore called on a model without soft deletes")
	}
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	tableName := tableNameFor(model, schema)
	if _, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Update(ctx, map[string]any{schema.DeletedAt.Column: nil}); err != nil {
		return err
	}
	setDeletedAt(v.FieldByIndex(schema.DeletedAt.Index), nil)
	return nil
}

// setDeletedAt writes a *time.Time into the deleted_at field, supporting both
// pointer (*time.Time) and value (time.Time) field declarations.
func setDeletedAt(fv reflect.Value, t *time.Time) {
	if fv.Kind() == reflect.Ptr {
		if t == nil {
			fv.Set(reflect.Zero(fv.Type()))
			return
		}
		fv.Set(reflect.ValueOf(t))
		return
	}
	if t == nil {
		fv.Set(reflect.Zero(fv.Type()))
		return
	}
	fv.Set(reflect.ValueOf(*t))
}

func collectValues(schema *reflectutil.Schema, v reflect.Value, forInsert bool) (map[string]any, error) {
	out := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.Skip || f.IsRelation {
			continue
		}
		fv := v.FieldByIndex(f.Index)
		if forInsert && f.IsAutoIncrement && fv.IsZero() {
			continue
		}
		val, err := castToDB(f, fv.Interface())
		if err != nil {
			return nil, err
		}
		out[f.Column] = val
	}
	return out, nil
}

// castToDB applies the field's registered cast, if any. A failing cast is an
// error: silently falling back to the raw Go value wrote unconverted data (or
// failed later with an unrelated driver error).
func castToDB(f *reflectutil.Field, val any) (any, error) {
	if f.Cast == "" {
		return val, nil
	}
	c := casts.Get(f.Cast)
	if c == nil {
		return val, nil
	}
	conv, err := c.ToDB(val)
	if err != nil {
		return nil, fmt.Errorf("orm: cast %s on %s: %w", f.Cast, f.Column, err)
	}
	return conv, nil
}

func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// collectUpdateValues builds the SET map for an UPDATE. It excludes the
// primary key (it is matched in WHERE, never reassigned) and created_at (an
// immutable timestamp that must survive updates); updated_at is included so it
// continues to advance.
func collectUpdateValues(schema *reflectutil.Schema, v reflect.Value) (map[string]any, error) {
	out := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.Skip || f.IsRelation {
			continue
		}
		if f.IsPrimary || f.IsCreatedAt {
			continue
		}
		val, err := castToDB(f, v.FieldByIndex(f.Index).Interface())
		if err != nil {
			return nil, err
		}
		out[f.Column] = val
	}
	return out, nil
}

// errChunkNeedsPK is returned by Chunk when the model has no primary key to
// page by.
var errChunkNeedsPK = errors.New("orm: Chunk requires a primary key on the model")

// primaryKeyValue returns the primary-key value of model, or nil if the schema
// has no primary key.
func primaryKeyValue[T any](schema *reflectutil.Schema, model *T) any {
	if schema.PrimaryKey == nil {
		return nil
	}
	return reflect.ValueOf(model).Elem().FieldByIndex(schema.PrimaryKey.Index).Interface()
}

// assignColumns writes the column-keyed attrs map onto the model's matching
// fields, applying registered casts where declared. Unknown columns are
// ignored. It mirrors the value handling used by hydrateRows so values supplied
// to FirstOrCreate/UpdateOrCreate convert the same way as scanned rows.
func assignColumns[T any](model *T, attrs map[string]any) error {
	schema := reflectutil.Parse(model)
	v := reflect.ValueOf(model).Elem()
	for col, raw := range attrs {
		f := schema.FieldByColumn(col)
		if f == nil || f.Skip || f.IsRelation {
			continue
		}
		fv := v.FieldByIndex(f.Index)
		if !fv.CanSet() {
			continue
		}
		if raw == nil {
			fv.Set(reflect.Zero(fv.Type()))
			continue
		}
		rv := reflect.ValueOf(raw)
		if rv.Type().AssignableTo(fv.Type()) {
			fv.Set(rv)
			continue
		}
		if err := assignScanned(fv, raw); err != nil {
			return fmt.Errorf("orm: assign %s: %w", col, err)
		}
	}
	return nil
}

func tableNameFor(model any, schema *reflectutil.Schema) string {
	if t, ok := model.(Tabler); ok {
		return t.TableName()
	}
	return schema.Table
}
