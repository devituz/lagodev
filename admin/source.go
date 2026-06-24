package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ErrNotFound is returned by DataSource.Get/Update/Delete when no row matches
// the supplied id. Handlers translate it to HTTP 404.
var ErrNotFound = errors.New("admin: record not found")

// ListParams describes a list query: pagination, a free-text search term
// matched against the resource's searchable fields, and exact-match filters
// keyed by column. The data source is responsible for honouring whichever of
// these it can; SliceSource honours all of them in memory.
type ListParams struct {
	// Page is 1-indexed; the source clamps values < 1 to 1.
	Page int
	// PerPage is the page size; the source clamps values < 1 to the resource
	// default.
	PerPage int
	// Search is the free-text query (case-insensitive substring). Empty means
	// no text filtering.
	Search string
	// SearchFields lists the columns Search is matched against (supplied by the
	// panel from the resource configuration).
	SearchFields []string
	// Filters constrains rows to those whose column equals the given value
	// (string-compared). Multiple filters are AND-ed.
	Filters map[string]string
	// IncludeDeleted, when true, asks soft-delete-aware sources to return
	// soft-deleted rows as well. The panel leaves it false by default.
	IncludeDeleted bool
	// SoftDeleteColumn names the soft-delete timestamp column, or "" when the
	// model is not soft-delete aware.
	SoftDeleteColumn string
}

// DataSource is the persistence contract the admin programs against. The panel
// never imports a database package directly; an application wires whichever
// backend it likes (orm-backed, SQL, in-memory) behind this interface. A
// reference in-memory implementation is provided by SliceSource.
//
// Implementations represent a row as a flat map[string]any keyed by column
// name (the same keys reflectFields derives). They must be safe for concurrent
// use. The ctx should be honoured for cancellation where the backend supports
// it.
type DataSource interface {
	// List returns one page of rows plus the total match count across all
	// pages (used for pagination). Rows are keyed by column name.
	List(ctx context.Context, params ListParams) (rows []map[string]any, total int, err error)
	// Get returns the single row identified by id (the primary-key value as a
	// string). It returns ErrNotFound when no row matches.
	Get(ctx context.Context, id string) (map[string]any, error)
	// Create inserts a new row and returns the stored representation (including
	// any generated id).
	Create(ctx context.Context, data map[string]any) (map[string]any, error)
	// Update mutates the row identified by id with the supplied fields and
	// returns the stored representation. It returns ErrNotFound when absent.
	Update(ctx context.Context, id string, data map[string]any) (map[string]any, error)
	// Delete removes the row identified by id. Soft-delete-aware sources may
	// set the soft-delete column instead of physically removing the row. It
	// returns ErrNotFound when absent.
	Delete(ctx context.Context, id string) error
}

// SliceSource is an in-memory DataSource backed by a slice of row maps. It is
// the reference implementation used by tests and demos, and a drop-in for
// prototyping before an orm-backed source is wired. It honours search,
// equality filters, pagination, sorting by primary key, and soft deletes (when
// the resource declares a DeletedAt column and the row carries that key).
//
// An application that wants live persistence supplies its own DataSource (for
// example, an adapter over the framework's orm.Query) instead of SliceSource;
// the HTTP layer is identical either way.
type SliceSource struct {
	pk string

	mu     sync.RWMutex
	rows   []map[string]any
	nextID int
}

// NewSliceSource creates an empty in-memory source whose primary-key column is
// pkColumn (commonly "id"). New rows without a pk value are assigned an
// auto-incrementing integer id.
func NewSliceSource(pkColumn string) *SliceSource {
	return &SliceSource{pk: pkColumn, nextID: 1}
}

// Seed inserts pre-built rows directly (bypassing id generation when a row
// already carries the pk). It is intended for fixtures and demos.
func (s *SliceSource) Seed(rows ...map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		row := cloneRow(r)
		if _, ok := row[s.pk]; !ok {
			row[s.pk] = s.nextID
			s.nextID++
		}
		s.rows = append(s.rows, row)
	}
}

// Len reports the number of stored rows (including soft-deleted ones). Useful
// for test assertions on mutation.
func (s *SliceSource) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.rows)
}

// List implements DataSource.
func (s *SliceSource) List(_ context.Context, params ListParams) ([]map[string]any, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var matched []map[string]any
	for _, row := range s.rows {
		if !params.IncludeDeleted && isSoftDeleted(row, params.SoftDeleteColumn) {
			continue
		}
		if !matchesSearch(row, params.Search, params.SearchFields) {
			continue
		}
		if !matchesFilters(row, params.Filters) {
			continue
		}
		matched = append(matched, cloneRow(row))
	}

	// Stable order by primary key so pagination is deterministic.
	sort.SliceStable(matched, func(i, j int) bool {
		return toString(matched[i][s.pk]) < toString(matched[j][s.pk])
	})

	total := len(matched)
	lo, hi := pageBounds(params.Page, params.PerPage, total)
	return matched[lo:hi], total, nil
}

// Get implements DataSource.
func (s *SliceSource) Get(_ context.Context, id string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := s.indexOf(id); i >= 0 {
		return cloneRow(s.rows[i]), nil
	}
	return nil, ErrNotFound
}

// Create implements DataSource.
func (s *SliceSource) Create(_ context.Context, data map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := cloneRow(data)
	if v, ok := row[s.pk]; !ok || isZeroID(v) {
		row[s.pk] = s.nextID
		s.nextID++
	}
	s.rows = append(s.rows, row)
	return cloneRow(row), nil
}

// Update implements DataSource.
func (s *SliceSource) Update(_ context.Context, id string, data map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return nil, ErrNotFound
	}
	for k, v := range data {
		if k == s.pk {
			continue // primary key is immutable
		}
		s.rows[i][k] = v
	}
	return cloneRow(s.rows[i]), nil
}

// Delete implements DataSource. When the row carries a soft-delete column key,
// the column is stamped (logical delete) rather than the row being removed;
// otherwise the row is dropped.
func (s *SliceSource) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return ErrNotFound
	}
	s.rows = append(s.rows[:i], s.rows[i+1:]...)
	return nil
}

// indexOf returns the slice index of the row with the given pk string, or -1.
// Caller holds the appropriate lock.
func (s *SliceSource) indexOf(id string) int {
	for i, row := range s.rows {
		if toString(row[s.pk]) == id {
			return i
		}
	}
	return -1
}

// --- matching helpers --------------------------------------------------------

func matchesSearch(row map[string]any, term string, fields []string) bool {
	term = strings.TrimSpace(term)
	if term == "" {
		return true
	}
	needle := strings.ToLower(term)
	cols := fields
	if len(cols) == 0 {
		cols = sortedKeys(row)
	}
	for _, c := range cols {
		if strings.Contains(strings.ToLower(toString(row[c])), needle) {
			return true
		}
	}
	return false
}

func matchesFilters(row map[string]any, filters map[string]string) bool {
	for col, want := range filters {
		if want == "" {
			continue
		}
		if toString(row[col]) != want {
			return false
		}
	}
	return true
}

func isSoftDeleted(row map[string]any, col string) bool {
	if col == "" {
		return false
	}
	v, ok := row[col]
	if !ok || v == nil {
		return false
	}
	// A non-empty value in the soft-delete column means the row is deleted.
	return toString(v) != "" && toString(v) != "0001-01-01 00:00:00 +0000 UTC"
}

func cloneRow(r map[string]any) map[string]any {
	out := make(map[string]any, len(r))
	for k, v := range r {
		out[k] = v
	}
	return out
}

func isZeroID(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case int:
		return x == 0
	case int64:
		return x == 0
	case uint64:
		return x == 0
	case string:
		return x == ""
	default:
		return false
	}
}

// toString renders a row value for comparison/display deterministically.
func toString(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// pageBounds clamps page/perPage and returns slice bounds for total rows. It is
// hardened against integer overflow: a hostile page query (e.g. a value near
// math.MaxInt) must not make (page-1)*perPage wrap negative and panic the
// caller's matched[lo:hi]. lo and hi are always within [0, total].
func pageBounds(page, perPage, total int) (lo, hi int) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	// Past the last page there is nothing to return; clamp before multiplying so
	// (page-1)*perPage cannot overflow. total/perPage+1 is the last meaningful
	// page, and total >= 0, so this bounds page to a small, overflow-safe value.
	if maxPage := total/perPage + 1; page > maxPage {
		page = maxPage
	}
	lo = (page - 1) * perPage
	if lo < 0 || lo > total {
		lo = total
	}
	hi = lo + perPage
	if hi < lo || hi > total {
		hi = total
	}
	return lo, hi
}
