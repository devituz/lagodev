package search

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// --- fake querier ---------------------------------------------------------

// fakeDB records the SQL/args of the last call and returns canned rows/errors,
// so the engine's SQL generation and result mapping are tested without a live
// Postgres.
type fakeDB struct {
	lastQuery string
	lastArgs  []any

	queryRows *fakeRows
	queryErr  error
	execErr   error
}

func (f *fakeDB) QueryContext(_ context.Context, query string, args ...any) (rows, error) {
	f.lastQuery = query
	f.lastArgs = args
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.queryRows, nil
}

func (f *fakeDB) ExecContext(_ context.Context, query string, args ...any) (result, error) {
	f.lastQuery = query
	f.lastArgs = args
	if f.execErr != nil {
		return nil, f.execErr
	}
	return fakeResult{}, nil
}

type fakeResult struct{}

func (fakeResult) RowsAffected() (int64, error) { return 0, nil }

// fakeRows replays a fixed set of column tuples through Scan.
type fakeRows struct {
	data   [][]any
	pos    int
	closed bool
	err    error
	// scanErr, when set, makes the next Scan fail.
	scanErr error
}

func (r *fakeRows) Next() bool {
	if r.pos >= len(r.data) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.data[r.pos-1]
	if len(row) != len(dest) {
		return errors.New("fakeRows: column count mismatch")
	}
	for i, src := range row {
		switch d := dest[i].(type) {
		case *string:
			*d = src.(string)
		case *float64:
			*d = src.(float64)
		case *int:
			*d = src.(int)
		case *fieldsScanner:
			if err := d.Scan(src); err != nil {
				return err
			}
		default:
			return errors.New("fakeRows: unsupported dest type")
		}
	}
	return nil
}

func (r *fakeRows) Err() error   { return r.err }
func (r *fakeRows) Close() error { r.closed = true; return nil }

// --- tests ----------------------------------------------------------------

func TestPostgresSearchSQLPlain(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{data: [][]any{
		{"7", 0.9, 2},
		{"3", 0.4, 2},
	}}}
	pg := NewPostgres(db, PgConfig{})

	res, err := pg.Search(context.Background(), "posts", "hello world", Options{Page: 2, PerPage: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	q := db.lastQuery
	for _, want := range []string{
		`FROM "posts"`,
		`plainto_tsquery('english', $1)`,
		`ts_rank("search", plainto_tsquery('english', $1)) AS rank`,
		`"search" @@ plainto_tsquery('english', $1)`,
		`count(*) OVER () AS total`,
		`ORDER BY rank DESC, "id" ASC`,
		`LIMIT $2 OFFSET $3`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("SQL missing %q\nfull: %s", want, q)
		}
	}

	// Args: tsquery text, then LIMIT (perPage), then OFFSET ((page-1)*perPage).
	wantArgs := []any{"hello world", 5, 5}
	if !reflect.DeepEqual(db.lastArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", db.lastArgs, wantArgs)
	}

	if res.Total != 2 || len(res.Hits) != 2 {
		t.Fatalf("result total=%d hits=%d, want 2/2", res.Total, len(res.Hits))
	}
	if res.Hits[0].ID != "7" || res.Hits[0].Score != 0.9 {
		t.Fatalf("hit[0] = %+v, want id 7 score 0.9", res.Hits[0])
	}
}

func TestPostgresSearchPrefix(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{}}
	pg := NewPostgres(db, PgConfig{})

	_, err := pg.Search(context.Background(), "posts", "hel wor", Options{Prefix: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(db.lastQuery, `to_tsquery('english', $1)`) ||
		strings.Contains(db.lastQuery, "plainto_tsquery") {
		t.Fatalf("prefix must use to_tsquery, got: %s", db.lastQuery)
	}
	// First arg must be the AND-joined :* prefix expression.
	if db.lastArgs[0] != "hel:* & wor:*" {
		t.Fatalf("prefix tsquery arg = %v, want 'hel:* & wor:*'", db.lastArgs[0])
	}
}

func TestPostgresSearchWebSearch(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{}}
	pg := NewPostgres(db, PgConfig{WebSearch: true, Config: "simple"})

	_, _ = pg.Search(context.Background(), "docs", "go lang", Options{})
	if !strings.Contains(db.lastQuery, `websearch_to_tsquery('simple', $1)`) {
		t.Fatalf("expected websearch_to_tsquery with simple config, got: %s", db.lastQuery)
	}
}

func TestPostgresSearchFilters(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{}}
	pg := NewPostgres(db, PgConfig{})

	_, err := pg.Search(context.Background(), "posts", "hello", Options{
		Filters: map[string]any{"lang": "en", "author": "ann"},
		Page:    1, PerPage: 10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// Filters are bound in sorted-key order: author ($2), lang ($3), then
	// LIMIT ($4), OFFSET ($5).
	for _, want := range []string{`"author" = $2`, `"lang" = $3`, `LIMIT $4 OFFSET $5`} {
		if !strings.Contains(db.lastQuery, want) {
			t.Errorf("SQL missing %q\nfull: %s", want, db.lastQuery)
		}
	}
	wantArgs := []any{"hello", "ann", "en", 10, 0}
	if !reflect.DeepEqual(db.lastArgs, wantArgs) {
		t.Fatalf("args = %#v, want %#v", db.lastArgs, wantArgs)
	}
}

func TestPostgresSearchFieldsColumn(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{data: [][]any{
		{"1", 0.5, []byte(`{"title":"Hello","n":3}`), 1},
	}}}
	pg := NewPostgres(db, PgConfig{FieldsColumn: "fields"})

	res, err := pg.Search(context.Background(), "posts", "hello", Options{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(db.lastQuery, `"fields"`) {
		t.Fatalf("SQL missing fields column: %s", db.lastQuery)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits=%d, want 1", len(res.Hits))
	}
	got := res.Hits[0].Fields
	if got["title"] != "Hello" {
		t.Fatalf("decoded fields = %v, want title Hello", got)
	}
}

func TestPostgresSearchBlankQuery(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{}}
	pg := NewPostgres(db, PgConfig{})

	res, err := pg.Search(context.Background(), "posts", "the and or", Options{})
	if err != nil {
		t.Fatalf("blank: %v", err)
	}
	if res.Total != 0 || len(res.Hits) != 0 {
		t.Fatalf("blank result = %+v, want empty", res)
	}
	if db.lastQuery != "" {
		t.Fatalf("blank query should not hit DB, ran: %s", db.lastQuery)
	}
}

func TestPostgresSearchQueryError(t *testing.T) {
	sentinel := errors.New("boom")
	db := &fakeDB{queryErr: sentinel}
	pg := NewPostgres(db, PgConfig{})

	_, err := pg.Search(context.Background(), "posts", "hello", Options{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped sentinel", err)
	}
}

func TestPostgresSearchRowsError(t *testing.T) {
	sentinel := errors.New("rowfail")
	db := &fakeDB{queryRows: &fakeRows{err: sentinel}}
	pg := NewPostgres(db, PgConfig{})

	_, err := pg.Search(context.Background(), "posts", "hello", Options{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped rows error", err)
	}
}

func TestPostgresSearchScanError(t *testing.T) {
	sentinel := errors.New("scanfail")
	db := &fakeDB{queryRows: &fakeRows{data: [][]any{{"1", 0.1, 1}}, scanErr: sentinel}}
	pg := NewPostgres(db, PgConfig{})

	_, err := pg.Search(context.Background(), "posts", "hello", Options{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped scan error", err)
	}
}

func TestPostgresDelete(t *testing.T) {
	db := &fakeDB{}
	pg := NewPostgres(db, PgConfig{})

	if err := pg.Delete(context.Background(), "posts", "1", "2", "3"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(db.lastQuery, `DELETE FROM "posts" WHERE "id" IN ($1, $2, $3)`) {
		t.Fatalf("delete SQL = %s", db.lastQuery)
	}
	if !reflect.DeepEqual(db.lastArgs, []any{"1", "2", "3"}) {
		t.Fatalf("delete args = %v", db.lastArgs)
	}

	// Empty id list is a no-op.
	db2 := &fakeDB{}
	pg2 := NewPostgres(db2, PgConfig{})
	if err := pg2.Delete(context.Background(), "posts"); err != nil {
		t.Fatalf("empty delete: %v", err)
	}
	if db2.lastQuery != "" {
		t.Fatalf("empty delete should not hit DB, ran: %s", db2.lastQuery)
	}
}

func TestPostgresDeleteError(t *testing.T) {
	sentinel := errors.New("delfail")
	db := &fakeDB{execErr: sentinel}
	pg := NewPostgres(db, PgConfig{})

	err := pg.Delete(context.Background(), "posts", "1")
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped delete error", err)
	}
}

func TestPostgresCustomColumns(t *testing.T) {
	db := &fakeDB{queryRows: &fakeRows{}}
	pg := NewPostgres(db, PgConfig{IDColumn: "uuid", VectorColumn: "tsv"})

	_, _ = pg.Search(context.Background(), "items", "hello", Options{})
	for _, want := range []string{`"uuid"`, `ts_rank("tsv"`, `"tsv" @@`, `ORDER BY rank DESC, "uuid" ASC`} {
		if !strings.Contains(db.lastQuery, want) {
			t.Errorf("SQL missing custom column %q\nfull: %s", want, db.lastQuery)
		}
	}
}

func TestPostgresIndexNoOp(t *testing.T) {
	db := &fakeDB{}
	pg := NewPostgres(db, PgConfig{})
	if err := pg.Index(context.Background(), "posts", Doc("1", map[string]any{"x": "y"})); err != nil {
		t.Fatalf("index: %v", err)
	}
	if db.lastQuery != "" {
		t.Fatalf("Index should be a no-op, ran: %s", db.lastQuery)
	}
}
