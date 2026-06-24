package resource

import "github.com/devituz/lagodev/orm"

// Item renders a single model through its resource. The result is the
// transformed value itself (no envelope), ready for c.JSON(200, ...).
//
//	c.JSON(200, resource.Item(user, UserResource))
func Item[Model any](m Model, r Resource[Model]) any {
	return transform(m, r)
}

// CollectionResponse is the envelope returned by Collection: a "data" array of
// transformed models, plus optional top-level "meta". meta is omitted from the
// JSON when empty.
type CollectionResponse struct {
	Data []any          `json:"data"`
	Meta map[string]any `json:"meta,omitempty"`
}

// Collection renders a slice of models through their resource, wrapping the
// results in {"data": [...]}. A nil slice renders as {"data": []}, never null.
//
//	c.JSON(200, resource.Collection(users, UserResource))
func Collection[Model any](models []Model, r Resource[Model]) CollectionResponse {
	return CollectionResponse{Data: transformAll(models, r)}
}

// WithMeta attaches top-level meta to a CollectionResponse and returns it for
// chaining. Repeated calls merge.
func (c CollectionResponse) WithMeta(meta map[string]any) CollectionResponse {
	if c.Meta == nil {
		c.Meta = make(map[string]any, len(meta))
	}
	for k, v := range meta {
		c.Meta[k] = v
	}
	return c
}

// Meta describes a paginated result set. The keys mirror orm.Paginator so a
// client can drive a pager directly from the response.
type Meta struct {
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PerPage  int   `json:"per_page"`
	LastPage int   `json:"last_page"`
	HasMore  bool  `json:"has_more"`
}

// Links carries the URL helpers a paginated client typically needs. All fields
// are omitempty, so an unset link is absent rather than null. They are only
// populated when a caller provides a base URL via PaginatedLinks.
type Links struct {
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
	Prev  string `json:"prev,omitempty"`
	Next  string `json:"next,omitempty"`
}

// PaginatedResponse is the envelope returned by Paginated: the page's "data",
// derived "meta", and optional "links". Both meta and links always render
// (meta is always meaningful for a page); extra top-level data merges into meta.
type PaginatedResponse struct {
	Data  []any          `json:"data"`
	Meta  Meta           `json:"meta"`
	Links *Links         `json:"links,omitempty"`
	With  map[string]any `json:"with,omitempty"`
}

// Paginated renders an orm.Paginator page through a resource, deriving the meta
// block (total, page, per_page, last_page, has_more) straight from the
// paginator. A nil paginator yields an empty page with zero-value meta.
//
//	page, _ := orm.Query[User](conn).Paginate(ctx, 2, 15)
//	c.JSON(200, resource.Paginated(page, UserResource))
//
// Importing orm here is cycle-free: orm imports only database, never resource.
func Paginated[Model any](p *orm.Paginator[Model], r Resource[Model]) PaginatedResponse {
	if p == nil {
		return PaginatedResponse{Data: []any{}}
	}
	return PaginatedResponse{
		Data: transformAll(p.Data, r),
		Meta: Meta{
			Total:    p.Total,
			Page:     p.Page,
			PerPage:  p.PerPage,
			LastPage: p.LastPage,
			HasMore:  p.HasMore,
		},
	}
}

// WithLinks builds first/last/prev/next links from base (e.g. the request path)
// using a "?page=N" query and attaches them. base should already include any
// other query parameters or end clean; the page param is appended with the
// appropriate separator.
func (p PaginatedResponse) WithLinks(base string) PaginatedResponse {
	page := p.Meta.Page
	last := p.Meta.LastPage
	l := &Links{
		First: pageURL(base, 1),
		Last:  pageURL(base, last),
	}
	if page > 1 {
		l.Prev = pageURL(base, page-1)
	}
	if p.Meta.HasMore {
		l.Next = pageURL(base, page+1)
	}
	p.Links = l
	return p
}

// WithMeta merges extra top-level data into the "with" block and returns the
// response for chaining.
func (p PaginatedResponse) WithMeta(extra map[string]any) PaginatedResponse {
	if p.With == nil {
		p.With = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		p.With[k] = v
	}
	return p
}

// transformAll maps every model in models through r, always returning a
// non-nil slice (so empty input renders as [] not null).
func transformAll[Model any](models []Model, r Resource[Model]) []any {
	out := make([]any, 0, len(models))
	for i := range models {
		out = append(out, transform(models[i], r))
	}
	return out
}

// pageURL appends a page query parameter to base, choosing ? or & as needed.
func pageURL(base string, page int) string {
	sep := "?"
	for i := 0; i < len(base); i++ {
		if base[i] == '?' {
			sep = "&"
			break
		}
	}
	return base + sep + "page=" + itoa(page)
}

// itoa is a tiny dependency-free int-to-string for non-negative page numbers
// (and a leading '-' for the rare negative input).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
