package lagogin

import (
	"context"
)

// PaginatedQuery is the contract Paginate() expects — both *orm.Builder[T]
// and *query.Builder satisfy it via a thin adapter.
type PaginatedQuery[T any] interface {
	Count(ctx context.Context) (int64, error)
	Limit(n int) *T
	Offset(n int) *T
}

// Page describes one paginated payload — a Laravel-style envelope so
// clients can render pagination UI without extra round-trips.
type Page struct {
	Data       any   `json:"data"`
	Total      int64 `json:"total"`
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	LastPage   int   `json:"last_page"`
	From       int   `json:"from"`
	To         int   `json:"to"`
}

// Paginate runs a model-aware query through page/per_page query params and
// returns a Page envelope. The query is cloned-by-reference: callers should
// not reuse the builder after calling Paginate.
//
// Example:
//
//	r.GET("/posts", lagogin.H(func(c *lagogin.Ctx) (any, error) {
//	    return lagogin.Paginate[Post](c, orm.Query[Post](conn).OrderBy("id", "desc"))
//	}))
//
// Query params:
//
//	page      — 1-indexed page number (default 1)
//	per_page  — items per page (default 25, max 200)
//
// The function uses orm.Builder[T] generics so the result type is correct
// at compile time and no reflection is needed beyond what the ORM already does.
func Paginate[T any](c *Ctx, q *ormBuilder[T]) (Page, error) {
	page := c.QueryInt("page", 1)
	if page < 1 {
		page = 1
	}
	perPage := c.QueryInt("per_page", 25)
	if perPage < 1 {
		perPage = 25
	}
	if perPage > 200 {
		perPage = 200
	}

	total, err := q.Count(c.Ctx())
	if err != nil {
		return Page{}, err
	}

	var items []T
	if err := q.Limit(perPage).Offset((page - 1) * perPage).Get(c.Ctx(), &items); err != nil {
		return Page{}, err
	}

	last := int((total + int64(perPage) - 1) / int64(perPage))
	if last < 1 {
		last = 1
	}
	from := (page-1)*perPage + 1
	to := from + len(items) - 1
	if len(items) == 0 {
		from, to = 0, 0
	}
	return Page{
		Data:     items,
		Total:    total,
		Page:     page,
		PerPage:  perPage,
		LastPage: last,
		From:     from,
		To:       to,
	}, nil
}
