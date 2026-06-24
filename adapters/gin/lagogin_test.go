package lagogin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/auth"
	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"

	lagogin "github.com/devituz/lagodev/adapters/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// --- Fixture: minimal in-memory User model + migration -------------------

type User struct {
	orm.Model
	Name  string
	Email string
}

func (User) TableName() string { return "users" }

func newTestConn(t *testing.T) *database.Connection {
	t.Helper()
	migrations.Default.Reset()
	migrations.Register(migrations.Define("0001_users",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("users"))
		},
	))
	mgr := database.NewManager()
	conn, err := mgr.Open(t.Name(), database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared&mode=memory&_fk=1",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

// --- 1. Handler status mapping (orm.ErrNotFound → 404, value → 200) -----

func TestHandlerNotFoundMapsTo404(t *testing.T) {
	r := gin.New()
	r.GET("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return nil, orm.ErrNotFound
	}))
	w := do(r, "GET", "/x", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("body missing error message: %s", w.Body.String())
	}
}

func TestHandlerErrorMapsTo500(t *testing.T) {
	r := gin.New()
	r.GET("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return nil, errors.New("boom")
	}))
	w := do(r, "GET", "/x", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

func TestHandlerValueReturns200(t *testing.T) {
	r := gin.New()
	r.GET("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return map[string]string{"hi": "there"}, nil
	}))
	w := do(r, "GET", "/x", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"hi":"there"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestHandlerNilNilReturns204(t *testing.T) {
	r := gin.New()
	r.GET("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return nil, nil
	}))
	w := do(r, "GET", "/x", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("204 should have empty body, got %q", w.Body.String())
	}
}

func TestHandlerCreatedReturns201(t *testing.T) {
	r := gin.New()
	r.POST("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return c.Created(map[string]int{"id": 7}), nil
	}))
	w := do(r, "POST", "/x", strings.NewReader(`{}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"id":7`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

// --- 2. Resource() registers 5 routes -----------------------------------

type userCtrl struct{ Conn *database.Connection }

func (c *userCtrl) Index(ctx *lagogin.Ctx) (any, error) {
	var users []User
	return users, orm.Query[User](c.Conn).OrderBy("id", "desc").Get(ctx.Ctx(), &users)
}
func (c *userCtrl) Show(ctx *lagogin.Ctx) (any, error) {
	return orm.Query[User](c.Conn).Find(ctx.Ctx(), ctx.ParamUint("id"))
}
func (c *userCtrl) Store(ctx *lagogin.Ctx) (any, error) {
	var u User
	if err := ctx.Bind(&u); err != nil {
		return nil, err
	}
	return ctx.Created(u), orm.Save(ctx.Ctx(), c.Conn, &u)
}
func (c *userCtrl) Update(ctx *lagogin.Ctx) (any, error) {
	u, err := orm.Query[User](c.Conn).Find(ctx.Ctx(), ctx.ParamUint("id"))
	if err != nil {
		return nil, err
	}
	if err := ctx.Bind(u); err != nil {
		return nil, err
	}
	return u, orm.Save(ctx.Ctx(), c.Conn, u)
}
func (c *userCtrl) Destroy(ctx *lagogin.Ctx) (any, error) {
	u, err := orm.Query[User](c.Conn).Find(ctx.Ctx(), ctx.ParamUint("id"))
	if err != nil {
		return nil, err
	}
	return nil, orm.Delete(ctx.Ctx(), c.Conn, u)
}

func TestResourceFullCRUDFlow(t *testing.T) {
	conn := newTestConn(t)
	lagogin.ResetRoutes()

	r := gin.New()
	lagogin.Resource(r, "users", &userCtrl{Conn: conn})

	// store
	w := do(r, "POST", "/users", strings.NewReader(`{"Name":"Ada","Email":"ada@x.com"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("store: want 201, got %d body=%s", w.Code, w.Body.String())
	}

	// index
	w = do(r, "GET", "/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("index: want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ada@x.com") {
		t.Fatalf("index missing record: %s", w.Body.String())
	}

	// show
	w = do(r, "GET", "/users/1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("show: want 200, got %d", w.Code)
	}

	// update
	w = do(r, "PATCH", "/users/1", strings.NewReader(`{"Name":"Ada Lovelace"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("update: want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Ada Lovelace") {
		t.Fatalf("update did not persist name: %s", w.Body.String())
	}

	// destroy
	w = do(r, "DELETE", "/users/1", nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("destroy: want 204, got %d", w.Code)
	}

	// show missing
	w = do(r, "GET", "/users/1", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("show after delete: want 404, got %d", w.Code)
	}

	// route registry sanity
	if got := len(lagogin.Routes()); got < 5 {
		t.Fatalf("route registry: want ≥5 entries, got %d", got)
	}
}

// --- 3. Pagination ------------------------------------------------------

func TestPaginatePagesAndCounts(t *testing.T) {
	conn := newTestConn(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		u := &User{Name: "u", Email: "e"}
		if err := orm.Save(ctx, conn, u); err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	r.GET("/users", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return lagogin.Paginate[User](c, orm.Query[User](conn).OrderBy("id", "desc"))
	}))

	w := do(r, "GET", "/users?page=2&per_page=10", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var page lagogin.Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("parse page: %v", err)
	}
	if page.Total != 25 || page.Page != 2 || page.PerPage != 10 || page.LastPage != 3 {
		t.Fatalf("unexpected page envelope: %+v", page)
	}
}

// --- 4. Validation → 422 -------------------------------------------------

type CreateDTO struct {
	Title string `json:"title" validate:"required,min=3,max=10"`
	Email string `json:"email" validate:"required,email"`
	Role  string `json:"role"  validate:"oneof=admin user"`
}

func TestValidationFailsWith422(t *testing.T) {
	r := gin.New()
	r.POST("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		var dto CreateDTO
		if err := c.BindAndValidate(&dto); err != nil {
			return nil, err
		}
		return dto, nil
	}))

	w := do(r, "POST", "/x", strings.NewReader(`{"title":"a","email":"nope","role":"x"}`))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := resp.Errors["title"]; !ok {
		t.Fatalf("expected title error, got %v", resp.Errors)
	}
}

func TestValidationPasses(t *testing.T) {
	r := gin.New()
	r.POST("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		var dto CreateDTO
		if err := c.BindAndValidate(&dto); err != nil {
			return nil, err
		}
		return dto, nil
	}))
	w := do(r, "POST", "/x", strings.NewReader(`{"title":"hello","email":"a@b.co","role":"admin"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// --- 5. AuthJWT middleware ---------------------------------------------

func TestAuthJWTRejectsMissing(t *testing.T) {
	mgr, _ := auth.New(auth.Config{Secret: "s", AccessTTL: time.Hour})
	r := gin.New()
	r.GET("/me", lagogin.AuthJWT(mgr), lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return c.UserID(), nil
	}))
	w := do(r, "GET", "/me", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAuthJWTAcceptsValid(t *testing.T) {
	mgr, _ := auth.New(auth.Config{Secret: "s", AccessTTL: time.Hour})
	token, _, _ := mgr.IssueAccess(42, "admin")

	r := gin.New()
	r.GET("/me", lagogin.AuthJWT(mgr), lagogin.H(func(c *lagogin.Ctx) (any, error) {
		return map[string]any{"uid": c.UserID(), "role": c.Role()}, nil
	}))
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"uid":42`) {
		t.Fatalf("body missing uid: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatalf("body missing role: %s", w.Body.String())
	}
}

// --- 6. QueryLog header & N+1 instrumentation ---------------------------

func TestQueryLogHeader(t *testing.T) {
	conn := newTestConn(t)
	conn = lagogin.Instrument(conn)

	r := gin.New()
	r.Use(lagogin.QueryLogN(conn, 1000))
	r.GET("/q", lagogin.H(func(c *lagogin.Ctx) (any, error) {
		// Force a couple of queries.
		_, _ = orm.Query[User](conn).Count(c.Ctx())
		var users []User
		_ = orm.Query[User](conn).Limit(1).Get(c.Ctx(), &users)
		// Manually observe — Connection.observe wires to logger only when
		// LogQueries is true, which Instrument sets. Belt-and-braces:
		lagogin.ObserveQuery(conn)
		lagogin.ObserveQuery(conn)
		return "ok", nil
	}))
	w := do(r, "GET", "/q", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-DB-Query-Count"); got == "" || got == "0" {
		t.Fatalf("X-DB-Query-Count missing or zero: %q", got)
	}
}

// --- 7. OpenAPI generation ---------------------------------------------

func TestOpenAPIContainsResourcePaths(t *testing.T) {
	conn := newTestConn(t)
	lagogin.ResetRoutes()

	r := gin.New()
	lagogin.Resource(r, "users", &userCtrl{Conn: conn})

	spec := lagogin.OpenAPI(lagogin.SpecInfo{Title: "t", Version: "v"})
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("no paths in spec: %v", spec)
	}
	if _, ok := paths["/users"]; !ok {
		t.Fatalf("missing /users path: %v", paths)
	}
	if _, ok := paths["/users/{id}"]; !ok {
		t.Fatalf("missing /users/{id} path: %v", paths)
	}
	users := paths["/users"].(map[string]any)
	if _, ok := users["get"]; !ok {
		t.Fatalf("missing GET /users in spec: %v", users)
	}
	if _, ok := users["post"]; !ok {
		t.Fatalf("missing POST /users in spec: %v", users)
	}
}

func TestServeOpenAPIServesJSON(t *testing.T) {
	conn := newTestConn(t)
	lagogin.ResetRoutes()
	r := gin.New()
	lagogin.Resource(r, "users", &userCtrl{Conn: conn})
	lagogin.ServeOpenAPI(r, lagogin.SpecInfo{Title: "t", Version: "v"})

	w := do(r, "GET", "/openapi.json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"openapi"`) {
		t.Fatalf("body missing openapi key: %s", w.Body.String())
	}
}

// --- 8. CORS middleware sets headers ------------------------------------

func TestCORSAddsHeaders(t *testing.T) {
	r := gin.New()
	r.Use(lagogin.CORS("*"))
	r.GET("/x", lagogin.H(func(c *lagogin.Ctx) (any, error) { return "ok", nil }))
	w := do(r, "GET", "/x", nil)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("CORS origin header = %q", got)
	}
}

// --- helpers -----------------------------------------------------------

func do(r http.Handler, method, path string, body interface{ Read([]byte) (int, error) }) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(nil))
	} else {
		req = httptest.NewRequest(method, path, body)
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRegistryRecordsServedPaths asserts the route registry records the
// gin-native path (`/users/:id`) that is actually served — not `/{id}` —
// while OpenAPI() still converts to the spec-canonical `/users/{id}` with
// an `id` path parameter. Regression for the registry/served mismatch.
func TestRegistryRecordsServedPaths(t *testing.T) {
	conn := newTestConn(t)
	lagogin.ResetRoutes()

	r := gin.New()
	lagogin.Resource(r, "users", &userCtrl{Conn: conn})

	foundColon := false
	for _, rt := range lagogin.Routes() {
		if rt.Path == "/users/:id" {
			foundColon = true
		}
		if strings.Contains(rt.Path, "{id}") {
			t.Fatalf("registry should record gin-native :id, got %q", rt.Path)
		}
	}
	if !foundColon {
		t.Fatalf("registry missing served path /users/:id: %+v", lagogin.Routes())
	}

	spec := lagogin.OpenAPI(lagogin.SpecInfo{Title: "t", Version: "v"})
	paths := spec["paths"].(map[string]any)
	idPath, ok := paths["/users/{id}"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI must emit /users/{id}, got: %v", paths)
	}
	if _, ok := paths["/users/:id"]; ok {
		t.Fatalf("OpenAPI leaked gin-native :id path: %v", paths)
	}
	show := idPath["get"].(map[string]any)
	if _, ok := show["parameters"]; !ok {
		t.Fatalf("OpenAPI /users/{id} GET missing id path parameter: %v", show)
	}
}
