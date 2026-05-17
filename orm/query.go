package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/devituz/lagodev/casts"
	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/internal/reflectutil"
	"github.com/devituz/lagodev/query"
)

// ErrNotFound is returned by First/FirstOrFail when no row matches.
var ErrNotFound = errors.New("orm: record not found")

// Builder is a model-aware wrapper around query.Builder.
type Builder[T any] struct {
	conn     *database.Connection
	executor database.Executor
	schema   *reflectutil.Schema
	qb       *query.Builder
	withTrashed bool
	onlyTrashed bool
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

// WithTrashed includes soft-deleted rows in the result.
func (b *Builder[T]) WithTrashed() *Builder[T] { b.withTrashed = true; return b }

// OnlyTrashed restricts to soft-deleted rows.
func (b *Builder[T]) OnlyTrashed() *Builder[T] { b.onlyTrashed = true; return b }

// Raw query builder access.
func (b *Builder[T]) QB() *query.Builder { return b.qb }

func (b *Builder[T]) applySoftDeleteScope() {
	if !b.schema.SoftDeletes {
		return
	}
	if b.withTrashed {
		return
	}
	if b.onlyTrashed {
		b.qb.WhereNotNull(b.schema.DeletedAt.Column)
		return
	}
	b.qb.WhereNull(b.schema.DeletedAt.Column)
}

// Get executes the query and populates dst (must be *[]T).
func (b *Builder[T]) Get(ctx context.Context, dst *[]T) error {
	b.applySoftDeleteScope()
	rows, err := b.qb.Get(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()
	return hydrateRows[T](ctx, b.conn, rows, b.schema, dst)
}

// First returns the first matching row, or ErrNotFound.
func (b *Builder[T]) First(ctx context.Context) (*T, error) {
	b.qb.Limit(1)
	b.applySoftDeleteScope()
	rows, err := b.qb.Get(ctx)
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
	return &out[0], nil
}

// Find by primary key.
func (b *Builder[T]) Find(ctx context.Context, id any) (*T, error) {
	if b.schema.PrimaryKey == nil {
		return nil, errors.New("orm: no primary key on model")
	}
	b.qb.Where(b.schema.PrimaryKey.Column, "=", id)
	return b.First(ctx)
}

// Count rows matching the query.
func (b *Builder[T]) Count(ctx context.Context) (int64, error) {
	b.applySoftDeleteScope()
	return b.qb.Count(ctx)
}

// Exists reports whether any matching row exists.
func (b *Builder[T]) Exists(ctx context.Context) (bool, error) {
	b.applySoftDeleteScope()
	return b.qb.Exists(ctx)
}

// Pluck extracts a single column as []V.
func Pluck[T any, V any](ctx context.Context, b *Builder[T], col string) ([]V, error) {
	b.qb.Select(col)
	b.applySoftDeleteScope()
	rows, err := b.qb.Get(ctx)
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
func hydrateRows[T any](_ context.Context, _ *database.Connection, rows *sql.Rows, schema *reflectutil.Schema, dst *[]T) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	scanTargets := make([]any, len(cols))
	holders := make([]any, len(cols))
	for rows.Next() {
		var row T
		v := reflect.ValueOf(&row).Elem()
		for i, c := range cols {
			f := schema.FieldByColumn(c)
			if f == nil {
				var raw any
				holders[i] = &raw
				scanTargets[i] = holders[i]
				continue
			}
			fv := v.FieldByIndex(f.Index)
			if f.Cast != "" {
				// Scan raw, then cast.
				var raw any
				holders[i] = &raw
				scanTargets[i] = holders[i]
				continue
			}
			scanTargets[i] = ptrTo(fv)
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return err
		}
		for i, c := range cols {
			f := schema.FieldByColumn(c)
			if f == nil || f.Cast == "" {
				continue
			}
			rawPtr := holders[i].(*any)
			fv := v.FieldByIndex(f.Index)
			if c := casts.Get(f.Cast); c != nil {
				if err := c.FromDB(*rawPtr, fv.Addr().Interface()); err != nil {
					return fmt.Errorf("orm: cast %s on %s: %w", f.Cast, f.Column, err)
				}
			}
		}
		*dst = append(*dst, row)
	}
	return rows.Err()
}

// ptrTo returns a scan-target pointer for a reflect.Value, allocating one
// where needed (the zero value of a non-pointer field is already addressable).
func ptrTo(v reflect.Value) any {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return v.Interface()
	}
	if !v.CanAddr() {
		// Should not happen because we obtain v via FieldByIndex on an
		// addressable struct value, but guard anyway.
		nv := reflect.New(v.Type()).Elem()
		return nv.Addr().Interface()
	}
	return v.Addr().Interface()
}

// Save persists a model: it inserts when the primary key is zero, otherwise
// updates. Hooks are dispatched around the operation. Timestamps are set
// automatically.
func Save[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	v := reflect.ValueOf(model).Elem()
	hctx := &HookContext{Ctx: ctx, Conn: conn}

	// Decide whether this is a create or an update.
	isCreate := false
	if pk := schema.PrimaryKey; pk != nil {
		fv := v.FieldByIndex(pk.Index)
		if fv.IsZero() {
			isCreate = true
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
		values := collectValues(schema, v, true)
		tableName := tableNameFor(model, schema)
		id, err := query.New(conn, tableName).
			InsertGetID(ctx, values, schema.PrimaryKey.Column)
		if err != nil {
			return err
		}
		pkVal := v.FieldByIndex(schema.PrimaryKey.Index)
		if pkVal.CanSet() {
			pkVal.Set(reflect.ValueOf(id).Convert(pkVal.Type()))
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
	values := collectValues(schema, v, false)
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	tableName := tableNameFor(model, schema)
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

// Delete deletes the model — soft-delete when the schema supports it, hard
// delete otherwise.
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
	if schema.SoftDeletes {
		now := conn.Now()
		v.FieldByIndex(schema.DeletedAt.Index).Set(reflect.ValueOf(sql.NullTime{Time: now, Valid: true}))
		_, err := query.New(conn, tableName).
			Where(schema.PrimaryKey.Column, "=", pkVal).
			Update(ctx, map[string]any{schema.DeletedAt.Column: now})
		if err != nil {
			return err
		}
	} else {
		if _, err := query.New(conn, tableName).
			Where(schema.PrimaryKey.Column, "=", pkVal).
			Delete(ctx); err != nil {
			return err
		}
	}
	return dispatchHook(model, "AfterDelete", hctx)
}

// ForceDelete bypasses soft-delete and removes the row.
func ForceDelete[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	if schema.PrimaryKey == nil {
		return errors.New("orm: cannot delete model without a primary key")
	}
	v := reflect.ValueOf(model).Elem()
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	tableName := tableNameFor(model, schema)
	_, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Delete(ctx)
	return err
}

// Restore reverts a soft-delete.
func Restore[T any](ctx context.Context, conn *database.Connection, model *T) error {
	schema := reflectutil.Parse(model)
	if !schema.SoftDeletes {
		return errors.New("orm: model does not support soft deletes")
	}
	v := reflect.ValueOf(model).Elem()
	pkVal := v.FieldByIndex(schema.PrimaryKey.Index).Interface()
	v.FieldByIndex(schema.DeletedAt.Index).Set(reflect.ValueOf(sql.NullTime{}))
	tableName := tableNameFor(model, schema)
	_, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Update(ctx, map[string]any{schema.DeletedAt.Column: nil})
	return err
}

func collectValues(schema *reflectutil.Schema, v reflect.Value, forInsert bool) map[string]any {
	out := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.Skip || f.IsRelation || f.IsDeletedAt {
			continue
		}
		fv := v.FieldByIndex(f.Index)
		if forInsert && f.IsAutoIncrement && fv.IsZero() {
			continue
		}
		val := fv.Interface()
		if f.Cast != "" {
			if c := casts.Get(f.Cast); c != nil {
				if conv, err := c.ToDB(val); err == nil {
					val = conv
				}
			}
		}
		out[f.Column] = val
	}
	return out
}

func tableNameFor(model any, schema *reflectutil.Schema) string {
	if t, ok := model.(Tabler); ok {
		return t.TableName()
	}
	return schema.Table
}
