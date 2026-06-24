package openapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/devituz/lagodev/openapi"
	"github.com/devituz/lagodev/web"
)

type CreateUser struct {
	Name  string `json:"name"  validate:"required,max=64"`
	Email string `json:"email" validate:"required,email"`
	Role  string `json:"role"  validate:"required,in=admin|user"`
}

type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func buildSpec() *openapi.Spec {
	spec := openapi.New("Users API", "1.0.0")
	spec.AddServer("https://api.example.com", "production")
	spec.Operation(http.MethodGet, "/users/{id}", openapi.OperationConfig{
		Summary: "Get a user",
		Tags:    []string{"users"},
		Responses: map[int]openapi.Response{
			200: openapi.JSONResponse[User]("the user"),
			404: openapi.TextResponse("not found"),
		},
	})
	spec.Operation(http.MethodPost, "/users", openapi.OperationConfig{
		Summary:     "Create a user",
		Tags:        []string{"users"},
		RequestBody: openapi.BodyOf[CreateUser](),
		Responses: map[int]openapi.Response{
			201: openapi.JSONResponse[User]("created"),
			422: openapi.TextResponse("validation failed"),
		},
	})
	return spec
}

func TestSpec_JSONValidStructure(t *testing.T) {
	b, err := buildSpec().JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("spec.JSON() is not valid JSON")
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", doc["openapi"])
	}
	if _, ok := doc["info"]; !ok {
		t.Errorf("missing info")
	}
	if _, ok := doc["paths"]; !ok {
		t.Errorf("missing paths")
	}
	if _, ok := doc["components"]; !ok {
		t.Errorf("missing components (DTOs should be registered)")
	}
	info := doc["info"].(map[string]any)
	if info["title"] != "Users API" || info["version"] != "1.0.0" {
		t.Errorf("info = %v", info)
	}
}

func TestSpec_OperationWithBodyAndResponses(t *testing.T) {
	doc := unmarshal(t, buildSpec())
	paths := doc["paths"].(map[string]any)

	post := dig(t, paths, "/users", "post")
	// request body $ref to component.
	ref := dig(t, post, "requestBody", "content", "application/json", "schema")["$ref"]
	if ref != "#/components/schemas/CreateUser" {
		t.Errorf("requestBody $ref = %v, want CreateUser ref", ref)
	}
	// 201 response $ref to User.
	resp := dig(t, post, "responses", "201", "content", "application/json", "schema")["$ref"]
	if resp != "#/components/schemas/User" {
		t.Errorf("201 schema $ref = %v, want User ref", resp)
	}
	if _, ok := post["responses"].(map[string]any)["422"]; !ok {
		t.Errorf("missing 422 response")
	}

	// components carry the referenced schemas.
	comps := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if _, ok := comps["CreateUser"]; !ok {
		t.Errorf("CreateUser component missing")
	}
	if _, ok := comps["User"]; !ok {
		t.Errorf("User component missing")
	}
}

func TestSpec_PathParamAutoDerived(t *testing.T) {
	doc := unmarshal(t, buildSpec())
	get := dig(t, doc["paths"].(map[string]any), "/users/{id}", "get")
	params := get["parameters"].([]any)
	if len(params) != 1 {
		t.Fatalf("want 1 path param, got %d: %v", len(params), params)
	}
	p := params[0].(map[string]any)
	if p["name"] != "id" || p["in"] != "path" || p["required"] != true {
		t.Errorf("path param = %v", p)
	}
}

func TestSpec_ExplicitPathParamNotDuplicated(t *testing.T) {
	spec := openapi.New("X", "1")
	spec.Operation(http.MethodGet, "/items/{id}", openapi.OperationConfig{
		PathParams: []openapi.Param{{
			Name: "id", Description: "item id", Required: true,
			Schema: &openapi.Schema{Type: nil},
		}},
	})
	doc := unmarshal(t, spec)
	get := dig(t, doc["paths"].(map[string]any), "/items/{id}", "get")
	if n := len(get["parameters"].([]any)); n != 1 {
		t.Errorf("explicit param should not be duplicated by auto-derive, got %d", n)
	}
	p := get["parameters"].([]any)[0].(map[string]any)
	if p["description"] != "item id" {
		t.Errorf("explicit param doc lost: %v", p)
	}
}

func TestSpec_RoundTrip(t *testing.T) {
	b, err := buildSpec().JSON()
	if err != nil {
		t.Fatal(err)
	}
	// Round-trip through a typed-ish structure to confirm shape integrity.
	var doc struct {
		OpenAPI    string                                `json:"openapi"`
		Info       map[string]any                        `json:"info"`
		Servers    []map[string]any                      `json:"servers"`
		Paths      map[string]map[string]any             `json:"paths"`
		Components map[string]map[string]json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q", doc.OpenAPI)
	}
	if len(doc.Servers) != 1 || doc.Servers[0]["url"] != "https://api.example.com" {
		t.Errorf("servers = %v", doc.Servers)
	}
	if _, ok := doc.Paths["/users"]; !ok {
		t.Errorf("paths missing /users")
	}
	if _, ok := doc.Components["schemas"]["User"]; !ok {
		t.Errorf("components.schemas.User missing")
	}
}

func TestPathParams_BraceAndColon(t *testing.T) {
	cases := map[string][]string{
		"/users/{id}":         {"id"},
		"/users/:id":          {"id"},
		"/a/{x}/b/{y}":        {"x", "y"},
		"/a/:x/b/:y":          {"x", "y"},
		"/static/path":        nil,
		"/mix/{id}/sub/:slug": {"id", "slug"},
	}
	for path, want := range cases {
		got := openapi.PathParams(path)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("PathParams(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestNormalizePath_ColonToBrace(t *testing.T) {
	if got := openapi.NormalizePath("/users/:id/posts/:pid"); got != "/users/{id}/posts/{pid}" {
		t.Errorf("NormalizePath = %q", got)
	}
	if got := openapi.NormalizePath("/users/{id}"); got != "/users/{id}" {
		t.Errorf("brace path changed: %q", got)
	}
}

func TestHarvest_FromWebApp(t *testing.T) {
	app := web.New(web.WithoutDefaultMiddleware())
	app.Get("/posts", func(c *web.Context) (any, error) { return nil, nil })
	app.Get("/posts/{id}", func(c *web.Context) (any, error) { return nil, nil })
	app.Post("/posts", func(c *web.Context) (any, error) { return nil, nil })

	routes := openapi.Harvest(app)
	if len(routes) != 3 {
		t.Fatalf("harvested %d routes, want 3", len(routes))
	}
	var sawShow bool
	for _, r := range routes {
		if r.Path == "/posts/{id}" {
			sawShow = true
			if len(r.PathParams) != 1 || r.PathParams[0] != "id" {
				t.Errorf("show route params = %v", r.PathParams)
			}
		}
	}
	if !sawShow {
		t.Errorf("missing /posts/{id} route in %v", routes)
	}

	// FromApp produces a serveable skeleton.
	spec := openapi.FromApp(app, "Blog", "1.0")
	doc := unmarshal(t, spec)
	if _, ok := doc["paths"].(map[string]any)["/posts/{id}"]; !ok {
		t.Errorf("FromApp did not register /posts/{id}")
	}
}

func TestServe_Handlers(t *testing.T) {
	spec := buildSpec()

	rec := httptest.NewRecorder()
	spec.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("Handler status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	if !json.Valid(rec.Body.Bytes()) {
		t.Errorf("handler body not valid JSON")
	}

	// Method not allowed.
	rec = httptest.NewRecorder()
	spec.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/openapi.json", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}

	// Docs page.
	rec = httptest.NewRecorder()
	spec.DocsHandler("/openapi.json").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("DocsHandler status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "swagger-ui") || !strings.Contains(body, "/openapi.json") {
		t.Errorf("docs page missing swagger-ui/spec url")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("docs content-type = %q", ct)
	}
}

// --- helpers ---

func unmarshal(t *testing.T, s *openapi.Spec) map[string]any {
	t.Helper()
	b, err := s.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// dig walks nested map[string]any by keys, failing the test on a missing or
// wrongly-typed level.
func dig(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := m
	for i, k := range keys {
		v, ok := cur[k]
		if !ok {
			t.Fatalf("missing key %q in path %v", k, keys[:i+1])
		}
		if i == len(keys)-1 {
			out, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("key %q is %T, want object", k, v)
			}
			return out
		}
		next, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("key %q is %T, want object", k, v)
		}
		cur = next
	}
	return cur
}
