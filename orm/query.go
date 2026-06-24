package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strconv"

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

// Raw query builder access.
func (b *Builder[T]) QB() *query.Builder { return b.qb }

// Get executes the query and populates dst (must be *[]T).
func (b *Builder[T]) Get(ctx context.Context, dst *[]T) error {
	rows, err := b.qb.Get(ctx)
	if err != nil {
		return err
	}
	defer rows.Close()
	return hydrateRows[T](ctx, b.conn, rows, b.schema, dst)
}

// First returns the first matching row, or ErrNotFound. It runs against a
// clone of the underlying builder so the receiver keeps its full clause state
// and can still be used for Get/Count afterwards.
func (b *Builder[T]) First(ctx context.Context) (*T, error) {
	rows, err := b.qb.Clone().Limit(1).Get(ctx)
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
	rows, err := b.qb.Clone().Where(b.schema.PrimaryKey.Column, "=", id).Limit(1).Get(ctx)
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

// Count rows matching the query.
func (b *Builder[T]) Count(ctx context.Context) (int64, error) {
	return b.qb.Count(ctx)
}

// Exists reports whether any matching row exists.
func (b *Builder[T]) Exists(ctx context.Context) (bool, error) {
	return b.qb.Exists(ctx)
}

// Pluck extracts a single column as []V.
func Pluck[T any, V any](ctx context.Context, b *Builder[T], col string) ([]V, error) {
	b.qb.Select(col)
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
	}
	return rows.Err()
}

// assignScanned writes a value scanned from the DB (as an any holding the
// driver's concrete type, or nil for NULL) into the destination field. NULL
// coalesces to the field's Go zero value. The actual type conversion is
// delegated to database/sql's convertAssign via a throwaway scan so we inherit
// its full driver-type handling (int64→int, []byte→string, time, etc.).
func assignScanned(fv reflect.Value, raw any) error {
	if raw == nil {
		// NULL → zero value. For pointer fields leave nil; otherwise reset.
		fv.Set(reflect.Zero(fv.Type()))
		return nil
	}
	if fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return convertAssign(fv.Interface(), raw)
	}
	return convertAssign(fv.Addr().Interface(), raw)
}

// convertAssign converts src (a driver value: int64, float64, bool, []byte,
// string or time.Time) into the pointer dest. It mirrors the subset of
// database/sql's convertAssign that the ORM relies on, with a reflection
// fallback for numeric/string kinds so non-default field types (int, uint,
// float32, named string types, ...) all work.
func convertAssign(dest, src any) error {
	dv := reflect.ValueOf(dest).Elem()

	// Fast path: src is directly assignable to the destination type.
	sv := reflect.ValueOf(src)
	if sv.Type().AssignableTo(dv.Type()) {
		dv.Set(sv)
		return nil
	}

	switch dv.Kind() {
	case reflect.String:
		switch s := src.(type) {
		case string:
			dv.SetString(s)
			return nil
		case []byte:
			dv.SetString(string(s))
			return nil
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, ok := toInt64(src); ok {
			dv.SetInt(n)
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, ok := toInt64(src); ok {
			dv.SetUint(uint64(n))
			return nil
		}
	case reflect.Float32, reflect.Float64:
		switch f := src.(type) {
		case float64:
			dv.SetFloat(f)
			return nil
		case int64:
			dv.SetFloat(float64(f))
			return nil
		}
	case reflect.Bool:
		switch v := src.(type) {
		case bool:
			dv.SetBool(v)
			return nil
		case int64:
			dv.SetBool(v != 0)
			return nil
		}
	}

	// Convertible numeric/string kinds (e.g. []byte → string already handled).
	if sv.Type().ConvertibleTo(dv.Type()) {
		dv.Set(sv.Convert(dv.Type()))
		return nil
	}
	return fmt.Errorf("orm: cannot assign %T to %s", src, dv.Type())
}

func toInt64(src any) (int64, bool) {
	switch n := src.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case []byte:
		if v, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			return v, true
		}
	case string:
		if v, err := strconv.ParseInt(n, 10, 64); err == nil {
			return v, true
		}
	}
	return 0, false
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
	values := collectUpdateValues(schema, v)
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

// Delete removes the model's row from the database.
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
	if _, err := query.New(conn, tableName).
		Where(schema.PrimaryKey.Column, "=", pkVal).
		Delete(ctx); err != nil {
		return err
	}
	return dispatchHook(model, "AfterDelete", hctx)
}

func collectValues(schema *reflectutil.Schema, v reflect.Value, forInsert bool) map[string]any {
	out := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.Skip || f.IsRelation {
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

// collectUpdateValues builds the SET map for an UPDATE. It excludes the
// primary key (it is matched in WHERE, never reassigned) and created_at (an
// immutable timestamp that must survive updates); updated_at is included so it
// continues to advance.
func collectUpdateValues(schema *reflectutil.Schema, v reflect.Value) map[string]any {
	out := make(map[string]any, len(schema.Fields))
	for _, f := range schema.Fields {
		if f.Skip || f.IsRelation {
			continue
		}
		if f.IsPrimary || f.IsCreatedAt {
			continue
		}
		fv := v.FieldByIndex(f.Index)
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
