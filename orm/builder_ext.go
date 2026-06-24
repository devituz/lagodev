package orm

import (
	"context"

	"github.com/devituz/lagodev/database"
)

// Paginator is the result of Builder.Paginate. Data holds the current page's
// rows; the remaining fields describe the position within the full result set.
type Paginator[T any] struct {
	Data     []T   `json:"data"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PerPage  int   `json:"per_page"`
	LastPage int   `json:"last_page"`
	HasMore  bool  `json:"has_more"`
}

// Paginate runs a COUNT (respecting the current WHERE and soft-delete scope)
// followed by a LIMIT/OFFSET query, returning the requested page. page and
// perPage are clamped to a minimum of 1.
func (b *Builder[T]) Paginate(ctx context.Context, page, perPage int) (*Paginator[T], error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 1
	}

	total, err := b.Count(ctx)
	if err != nil {
		return nil, err
	}

	offset := (page - 1) * perPage
	qb := b.scopedQB().Limit(perPage).Offset(offset)
	rows, err := qb.Get(ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var data []T
	if err := hydrateRows[T](ctx, b.conn, rows, b.schema, &data); err != nil {
		return nil, err
	}

	lastPage := int((total + int64(perPage) - 1) / int64(perPage))
	if lastPage < 1 {
		lastPage = 1
	}

	return &Paginator[T]{
		Data:     data,
		Total:    total,
		Page:     page,
		PerPage:  perPage,
		LastPage: lastPage,
		HasMore:  page < lastPage,
	}, nil
}

// Chunk iterates the result set in batches of size, invoking fn for each
// non-empty batch. It paginates by ascending primary key (a keyset cursor) so
// the whole set is never loaded into memory at once. Returning a non-nil error
// from fn stops the iteration and propagates the error. size is clamped to a
// minimum of 1.
func (b *Builder[T]) Chunk(ctx context.Context, size int, fn func([]T) error) error {
	if size < 1 {
		size = 1
	}
	if b.schema.PrimaryKey == nil {
		return errChunkNeedsPK
	}
	pkCol := b.schema.PrimaryKey.Column

	var lastID any
	for {
		qb := b.scopedQB().OrderBy(pkCol, "asc").Limit(size)
		if lastID != nil {
			qb.Where(pkCol, ">", lastID)
		}
		rows, err := qb.Get(ctx)
		if err != nil {
			return err
		}
		var batch []T
		err = hydrateRows[T](ctx, b.conn, rows, b.schema, &batch)
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if err := fn(batch); err != nil {
			return err
		}
		if len(batch) < size {
			return nil
		}
		lastID = primaryKeyValue(b.schema, &batch[len(batch)-1])
	}
}

// FirstOrCreate returns the first row matching where, or inserts a new row
// merging where and the optional defaults (defaults override where on key
// collisions). The returned model reflects the persisted row, including a
// generated primary key on the create path.
func FirstOrCreate[T any](ctx context.Context, conn *database.Connection, where map[string]any, defaults ...map[string]any) (*T, error) {
	b := Query[T](conn)
	for col, val := range where {
		b.Where(col, "=", val)
	}
	found, err := b.First(ctx)
	if err == nil {
		return found, nil
	}
	if err != ErrNotFound {
		return nil, err
	}

	model := new(T)
	attrs := mergeMaps(where, defaults...)
	if err := assignColumns(model, attrs); err != nil {
		return nil, err
	}
	if err := Save(ctx, conn, model); err != nil {
		return nil, err
	}
	return model, nil
}

// UpdateOrCreate updates the first row matching where with values, or inserts a
// new row merging where and values when no match exists. The returned model
// reflects the persisted row.
func UpdateOrCreate[T any](ctx context.Context, conn *database.Connection, where map[string]any, values map[string]any) (*T, error) {
	b := Query[T](conn)
	for col, val := range where {
		b.Where(col, "=", val)
	}
	found, err := b.First(ctx)
	if err != nil && err != ErrNotFound {
		return nil, err
	}

	if err == ErrNotFound {
		model := new(T)
		if err := assignColumns(model, mergeMaps(where, values)); err != nil {
			return nil, err
		}
		if err := Save(ctx, conn, model); err != nil {
			return nil, err
		}
		return model, nil
	}

	if err := assignColumns(found, values); err != nil {
		return nil, err
	}
	if err := Save(ctx, conn, found); err != nil {
		return nil, err
	}
	return found, nil
}

func mergeMaps(base map[string]any, extra ...map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for _, m := range extra {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
