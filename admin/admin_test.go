package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// SampleUser exercises tag-driven reflection: an explicit column override, the
// orm:"primary" flag, a managed timestamp, and a soft-delete column.
type SampleUser struct {
	ID        uint64     `column:"id" orm:"primary"`
	Name      string     `column:"name"`
	Email     string     `column:"email_addr"`
	Age       int        `column:"age"`
	Active    bool       `column:"active"`
	CreatedAt time.Time  `column:"created_at"`
	DeletedAt *time.Time `column:"deleted_at"`
}

// fakeStore is an in-memory DataSource recording calls, independent of
// SliceSource so the handler is tested against a minimal fake.
type fakeStore struct {
	mu        sync.Mutex
	rows      []map[string]any
	pk        string
	nextID    int
	softCol   string
	listCalls int
}

func newFakeStore(pk, softCol string) *fakeStore {
	return &fakeStore{pk: pk, nextID: 1, softCol: softCol}
}

func (f *fakeStore) seed(rows ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range rows {
		cp := cloneRow(r)
		if _, ok := cp[f.pk]; !ok {
			cp[f.pk] = f.nextID
			f.nextID++
		} else if n, err := strconv.Atoi(toString(cp[f.pk])); err == nil && n >= f.nextID {
			f.nextID = n + 1 // keep generated ids ahead of explicit ones
		}
		f.rows = append(f.rows, cp)
	}
}

func (f *fakeStore) List(_ context.Context, p ListParams) ([]map[string]any, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	var matched []map[string]any
	for _, r := range f.rows {
		if !p.IncludeDeleted && isSoftDeleted(r, p.SoftDeleteColumn) {
			continue
		}
		if !matchesSearch(r, p.Search, p.SearchFields) {
			continue
		}
		if !matchesFilters(r, p.Filters) {
			continue
		}
		matched = append(matched, cloneRow(r))
	}
	total := len(matched)
	lo, hi := pageBounds(p.Page, p.PerPage, total)
	return matched[lo:hi], total, nil
}

func (f *fakeStore) Get(_ context.Context, id string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if toString(r[f.pk]) == id {
			return cloneRow(r), nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) Create(_ context.Context, data map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := cloneRow(data)
	if v, ok := row[f.pk]; !ok || isZeroID(v) {
		row[f.pk] = f.nextID
		f.nextID++
	}
	f.rows = append(f.rows, row)
	return cloneRow(row), nil
}

func (f *fakeStore) Update(_ context.Context, id string, data map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if toString(r[f.pk]) == id {
			for k, v := range data {
				if k == f.pk {
					continue
				}
				r[k] = v
			}
			return cloneRow(r), nil
		}
	}
	return nil, ErrNotFound
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rows {
		if toString(r[f.pk]) == id {
			if f.softCol != "" {
				r[f.softCol] = time.Now()
				return nil
			}
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeStore) deleted(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if toString(r[f.pk]) == id {
			return isSoftDeleted(r, f.softCol)
		}
	}
	return false // physically gone
}

func (f *fakeStore) exists(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if toString(r[f.pk]) == id {
			return true
		}
	}
	return false
}

// newPanel builds a panel + fake store with a few seeded rows.
func newPanel(t *testing.T, opts ...Option) (*Panel, *fakeStore) {
	t.Helper()
	store := newFakeStore("id", "deleted_at")
	store.seed(
		map[string]any{"id": 1, "name": "Ada", "email_addr": "ada@example.com", "age": 36, "active": true},
		map[string]any{"id": 2, "name": "Linus", "email_addr": "linus@kernel.org", "age": 54, "active": true},
	)
	// Default to the insecure allow-all gate so the functional round-trip tests
	// exercise the handlers; tests that pass their own WithAuthorizer override
	// this (a later Option wins).
	p := New(append([]Option{WithInsecureAllowAll()}, opts...)...)
	p.Register(SampleUser{}, Resource{
		Source:  store,
		Search:  []string{"name", "email_addr"},
		Filters: []string{"active"},
		PerPage: 1, // force pagination across the two seeded rows
	})
	return p, store
}

// do runs a request against the panel handler and returns the recorder. It
// carries a CSRF cookie across the round-trip when provided.
func do(t *testing.T, p *Panel, method, target string, form url.Values, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if csrf != "" {
		req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
	}
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	return rec
}

// tokenFrom issues a CSRF token by hitting a GET page and reading the cookie.
func tokenFrom(t *testing.T, p *Panel, target string) string {
	t.Helper()
	rec := do(t, p, http.MethodGet, target, nil, "")
	for _, c := range rec.Result().Cookies() {
		if c.Name == csrfCookie {
			return c.Value
		}
	}
	t.Fatalf("no csrf cookie issued at %s", target)
	return ""
}

func TestReflectionDerivesFieldsFromTags(t *testing.T) {
	p, _ := newPanel(t)
	reg, ok := p.resource("sample_user")
	if !ok {
		t.Fatalf("resource not registered under derived slug")
	}
	if reg.pkColumn != "id" {
		t.Errorf("pk column = %q, want id", reg.pkColumn)
	}
	if reg.softCol != "deleted_at" {
		t.Errorf("soft-delete column = %q, want deleted_at", reg.softCol)
	}
	// column: tag override must win over the snake-case default.
	f, ok := reg.fieldByColumn("email_addr")
	if !ok {
		t.Fatalf("email_addr column not derived from tag")
	}
	if f.Label != "Email" {
		t.Errorf("label = %q, want Email", f.Label)
	}
	if f.Name != "Email" {
		t.Errorf("name = %q, want Email", f.Name)
	}
	// Kinds.
	if k, _ := reg.fieldByColumn("age"); k.Kind != "number" {
		t.Errorf("age kind = %q, want number", k.Kind)
	}
	if k, _ := reg.fieldByColumn("active"); k.Kind != "bool" {
		t.Errorf("active kind = %q, want bool", k.Kind)
	}
	// PK and CreatedAt must be excluded from editable; name must be included.
	edit := make(map[string]bool)
	for _, ec := range reg.editColumns() {
		edit[ec.Column] = true
	}
	if edit["id"] || edit["created_at"] || edit["deleted_at"] {
		t.Errorf("pk/timestamps must not be editable: %v", edit)
	}
	if !edit["name"] || !edit["email_addr"] {
		t.Errorf("name/email must be editable: %v", edit)
	}
}

func TestIndexListsResources(t *testing.T) {
	p, _ := newPanel(t)
	rec := do(t, p, http.MethodGet, "/", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Sample User") {
		t.Errorf("index does not list the registered model label; body:\n%s", body)
	}
	if !strings.Contains(body, "/admin/sample_user") {
		t.Errorf("index missing link to resource list")
	}
}

func TestListRendersRowsPaginationAndSearch(t *testing.T) {
	p, store := newPanel(t)

	// Page 1 (PerPage=1) shows the first row only, with a Next link.
	rec := do(t, p, http.MethodGet, "/sample_user?page=1", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Ada") {
		t.Errorf("page 1 missing first row; body:\n%s", body)
	}
	if strings.Contains(body, "Linus") {
		t.Errorf("page 1 should not contain page-2 row")
	}
	if !strings.Contains(body, "Page 1 of 2") {
		t.Errorf("pagination summary missing; body:\n%s", body)
	}
	if !strings.Contains(body, "Next") {
		t.Errorf("missing Next pager link")
	}
	if !strings.Contains(body, "Email") {
		t.Errorf("column header missing")
	}

	// Search narrows to one match.
	rec = do(t, p, http.MethodGet, "/sample_user?q=linus", nil, "")
	body = rec.Body.String()
	if !strings.Contains(body, "Linus") || strings.Contains(body, "Ada") {
		t.Errorf("search did not narrow results; body:\n%s", body)
	}
	if store.listCalls == 0 {
		t.Errorf("store.List was never called")
	}
}

func TestNewCreateRoundTrip(t *testing.T) {
	p, store := newPanel(t)

	// GET the form renders inputs derived from editable fields.
	rec := do(t, p, http.MethodGet, "/sample_user/new", nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="name"`) {
		t.Fatalf("new form missing name input; status %d body:\n%s", rec.Code, rec.Body.String())
	}

	csrf := tokenFrom(t, p, "/sample_user/new")
	form := url.Values{"csrf": {csrf}, "name": {"Grace"}, "email_addr": {"grace@navy.mil"}, "age": {"85"}, "active": {"true"}}
	rec = do(t, p, http.MethodPost, "/sample_user/new", form, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}

	// New row is searchable.
	rec = do(t, p, http.MethodGet, "/sample_user?q=grace", nil, "")
	if !strings.Contains(rec.Body.String(), "Grace") {
		t.Errorf("created row not found via search")
	}
	if !store.exists("3") {
		t.Errorf("created row id 3 absent from store")
	}
}

func TestEditUpdateRoundTrip(t *testing.T) {
	p, store := newPanel(t)

	rec := do(t, p, http.MethodGet, "/sample_user/1", nil, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="Ada"`) {
		t.Fatalf("edit form missing current value; status %d body:\n%s", rec.Code, rec.Body.String())
	}

	csrf := tokenFrom(t, p, "/sample_user/1")
	form := url.Values{"csrf": {csrf}, "name": {"Ada Lovelace"}, "email_addr": {"ada@example.com"}, "age": {"36"}}
	rec = do(t, p, http.MethodPost, "/sample_user/1", form, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}
	got, _ := store.Get(context.Background(), "1")
	if toString(got["name"]) != "Ada Lovelace" {
		t.Errorf("name not updated: %v", got["name"])
	}
}

func TestDeleteSoftDeletes(t *testing.T) {
	p, store := newPanel(t)
	csrf := tokenFrom(t, p, "/sample_user")
	rec := do(t, p, http.MethodPost, "/sample_user/1/delete", url.Values{"csrf": {csrf}}, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}
	if !store.deleted("1") {
		t.Errorf("row 1 was not soft-deleted")
	}
	// Soft-deleted row is excluded from the default list.
	rec = do(t, p, http.MethodGet, "/sample_user", nil, "")
	if strings.Contains(rec.Body.String(), "Ada") {
		t.Errorf("soft-deleted row still listed")
	}
}

func TestRBACDenialReturns403(t *testing.T) {
	deny := WithAuthorizer(func(_ *http.Request, action, _ string) bool {
		return action != ActionDelete // allow everything except delete
	})
	p, _ := newPanel(t, deny)
	csrf := tokenFrom(t, p, "/sample_user")
	rec := do(t, p, http.MethodPost, "/sample_user/1/delete", url.Values{"csrf": {csrf}}, csrf)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("RBAC delete status = %d, want 403", rec.Code)
	}
	// List remains allowed.
	rec = do(t, p, http.MethodGet, "/sample_user", nil, "")
	if rec.Code != http.StatusOK {
		t.Errorf("list status = %d, want 200", rec.Code)
	}
}

func TestCSRFRejectionWithoutToken(t *testing.T) {
	p, store := newPanel(t)

	// POST create without any CSRF cookie/field -> 403.
	form := url.Values{"name": {"Mallory"}, "email_addr": {"m@evil.test"}}
	rec := do(t, p, http.MethodPost, "/sample_user/new", form, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create without csrf = %d, want 403", rec.Code)
	}

	// Cookie present but form field missing -> still rejected.
	rec = do(t, p, http.MethodPost, "/sample_user/new", form, "sometoken")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create with cookie but no field = %d, want 403", rec.Code)
	}

	// Delete without token -> 403, row untouched.
	rec = do(t, p, http.MethodPost, "/sample_user/1/delete", url.Values{}, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("delete without csrf = %d, want 403", rec.Code)
	}
	if store.deleted("1") {
		t.Errorf("row deleted despite missing csrf")
	}
}

func TestMutatingRoutesRejectGET(t *testing.T) {
	p, _ := newPanel(t)
	rec := do(t, p, http.MethodGet, "/sample_user/1/delete", nil, "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET delete = %d, want 405", rec.Code)
	}
}

func TestUnknownResource404(t *testing.T) {
	p, _ := newPanel(t)
	rec := do(t, p, http.MethodGet, "/nope", nil, "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown resource = %d, want 404", rec.Code)
	}
}

// TestPageBoundsOverflow guards against a remotely-triggerable panic: a hostile
// ?page= value near math.MaxInt must not make (page-1)*perPage wrap negative and
// drive matched[lo:hi] out of range. lo/hi must stay within [0, total].
func TestPageBoundsOverflow(t *testing.T) {
	cases := []struct{ page, perPage, total int }{
		{1<<62 + 1, 8, 5},
		{1<<63 - 1, 2, 4},
		{1 << 60, 100, 3},
		{1<<63 - 1, 20, 0},
		{2, 20, 5},
	}
	for _, c := range cases {
		lo, hi := pageBounds(c.page, c.perPage, c.total)
		if lo < 0 || hi < 0 || lo > c.total || hi > c.total || lo > hi {
			t.Errorf("pageBounds(%d,%d,%d) = (%d,%d): out of [0,%d] bounds",
				c.page, c.perPage, c.total, lo, hi, c.total)
		}
		// The slice op the sources perform must not panic.
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("pageBounds(%d,%d,%d) -> matched[%d:%d] panicked: %v",
						c.page, c.perPage, c.total, lo, hi, rec)
				}
			}()
			s := make([]int, c.total)
			_ = s[lo:hi]
		}()
	}
}

// TestListSurvivesOverflowPage drives the panic end-to-end through the HTTP
// handler: a crafted page query must yield a normal 200, not crash the server.
func TestListSurvivesOverflowPage(t *testing.T) {
	store := newFakeStore("id", "")
	store.seed(map[string]any{"id": 1, "name": "a", "email_addr": "x"})
	p := New(WithInsecureAllowAll())
	p.Register(SampleUser{}, Resource{Source: store}) // default perPage
	rec := do(t, p, http.MethodGet, "/sample_user?page=4611686018427387904", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("overflow page status = %d, want 200", rec.Code)
	}
}

func TestHTMLAutoEscaping(t *testing.T) {
	store := newFakeStore("id", "")
	store.seed(map[string]any{"id": 1, "name": "<script>alert(1)</script>", "email_addr": "x@x"})
	p := New(WithInsecureAllowAll())
	p.Register(SampleUser{}, Resource{Source: store, Search: []string{"name"}})
	rec := do(t, p, http.MethodGet, "/sample_user", nil, "")
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("row data was not auto-escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag in output")
	}
}
