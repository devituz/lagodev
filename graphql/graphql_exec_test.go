package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test fixture: a small users -> posts schema backed by in-memory data, mixing
// resolver functions, default struct resolution and default map resolution.
// ---------------------------------------------------------------------------

// post is resolved by default resolution (struct fields, json tags).
type post struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// testUser mixes a tagged field, a plain field and posts resolved by a field
// resolver, exercising both default and explicit resolution on one object.
type testUser struct {
	ID   string `json:"id"`
	Name string
	age  int // unexported: must be ignored by default resolution
}

var execUsers = map[string]*testUser{
	"1": {ID: "1", Name: "Ann", age: 30},
	"2": {ID: "2", Name: "Bob", age: 41},
}

var execPosts = map[string][]post{
	"1": {{ID: "10", Title: "Hello"}, {ID: "11", Title: "World"}},
	"2": {},
}

func buildExecSchema(t *testing.T) *CompiledSchema {
	t.Helper()

	postType := &Object{
		Name: "Post",
		Fields: Fields{
			"id":    &Field{Type: NewNonNull(ID)},     // default: struct json tag
			"title": &Field{Type: NewNonNull(String)}, // default: struct json tag
		},
	}

	userType := &Object{
		Name: "User",
		Fields: Fields{
			"id":   &Field{Type: NewNonNull(ID)},     // default: struct json tag
			"name": &Field{Type: NewNonNull(String)}, // default: struct field name
			"posts": &Field{
				Type: NewList(NewNonNull(postType)),
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					u, _ := parent.(*testUser)
					if u == nil {
						return nil, nil
					}
					return execPosts[u.ID], nil
				},
			},
			// nonNullName always returns nil, to force null-bubbling.
			"nonNullName": &Field{
				Type: NewNonNull(String),
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return nil, nil
				},
			},
			// boom always errors, to exercise per-field error collection.
			"boom": &Field{
				Type: String,
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return nil, errors.New("graphql: boom")
				},
			},
		},
	}

	queryType := &Object{
		Name: "Query",
		Fields: Fields{
			"user": &Field{
				Type: userType,
				Args: Args{"id": {Type: NewNonNull(ID)}},
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return execUsers[args.String("id")], nil
				},
			},
			// echo returns its String arg, exercising variable + arg coercion.
			"echo": &Field{
				Type: String,
				Args: Args{"msg": {Type: NewNonNull(String)}},
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return args.String("msg"), nil
				},
			},
			// add sums two Int args, exercising Int coercion.
			"add": &Field{
				Type: Int,
				Args: Args{
					"a": {Type: NewNonNull(Int)},
					"b": {Type: NewNonNull(Int)},
				},
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return args.Int("a") + args.Int("b"), nil
				},
			},
			// config returns a plain map, exercising default map-key resolution.
			"config": &Field{
				Type: &Object{
					Name: "Config",
					Fields: Fields{
						"version": &Field{Type: NewNonNull(String)},
						"debug":   &Field{Type: NewNonNull(Boolean)},
					},
				},
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return map[string]any{"version": "1.0", "debug": true}, nil
				},
			},
		},
	}

	mutationType := &Object{
		Name: "Mutation",
		Fields: Fields{
			"rename": &Field{
				Type: NewNonNull(String),
				Args: Args{
					"id":   {Type: NewNonNull(ID)},
					"name": {Type: NewNonNull(String)},
				},
				Resolve: func(ctx context.Context, parent any, args ArgValues) (any, error) {
					return args.String("name"), nil
				},
			},
		},
	}

	cs, err := NewSchema(Schema{Query: queryType, Mutation: mutationType})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return cs
}

// execJSON runs a query and returns the response marshalled+unmarshalled to a
// generic map, the shape clients actually observe.
func execJSON(t *testing.T, cs *CompiledSchema, req Request) map[string]any {
	t.Helper()
	res := Execute(context.Background(), cs, req)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Execution tests
// ---------------------------------------------------------------------------

func TestExecuteNestedSelection(t *testing.T) {
	cs := buildExecSchema(t)
	out := execJSON(t, cs, Request{Query: `{ user(id:"1"){ name posts { title } } }`})
	if _, ok := out["errors"]; ok {
		t.Fatalf("unexpected errors: %v", out["errors"])
	}
	user := out["data"].(map[string]any)["user"].(map[string]any)
	if user["name"] != "Ann" {
		t.Fatalf("name = %v, want Ann", user["name"])
	}
	posts := user["posts"].([]any)
	if len(posts) != 2 {
		t.Fatalf("len posts = %d, want 2", len(posts))
	}
	if posts[0].(map[string]any)["title"] != "Hello" {
		t.Fatalf("post[0].title = %v", posts[0])
	}
}

func TestExecuteAliases(t *testing.T) {
	cs := buildExecSchema(t)
	out := execJSON(t, cs, Request{Query: `{ a: user(id:"1"){ who: name } b: user(id:"2"){ who: name } }`})
	data := out["data"].(map[string]any)
	if data["a"].(map[string]any)["who"] != "Ann" {
		t.Fatalf("a.who = %v, want Ann", data["a"])
	}
	if data["b"].(map[string]any)["who"] != "Bob" {
		t.Fatalf("b.who = %v, want Bob", data["b"])
	}
}

func TestExecuteArguments(t *testing.T) {
	cs := buildExecSchema(t)
	out := execJSON(t, cs, Request{Query: `{ add(a: 2, b: 40) echo(msg: "hi") }`})
	data := out["data"].(map[string]any)
	if data["add"].(float64) != 42 {
		t.Fatalf("add = %v, want 42", data["add"])
	}
	if data["echo"] != "hi" {
		t.Fatalf("echo = %v, want hi", data["echo"])
	}
}

func TestExecuteVariables(t *testing.T) {
	cs := buildExecSchema(t)
	q := `query Q($uid: ID!, $n: Int!) { user(id: $uid){ name } add(a: $n, b: 1) }`
	out := execJSON(t, cs, Request{
		Query:     q,
		Variables: map[string]any{"uid": "2", "n": float64(7)}, // n arrives as JSON float64
	})
	if _, ok := out["errors"]; ok {
		t.Fatalf("unexpected errors: %v", out["errors"])
	}
	data := out["data"].(map[string]any)
	if data["user"].(map[string]any)["name"] != "Bob" {
		t.Fatalf("user.name = %v, want Bob", data["user"])
	}
	if data["add"].(float64) != 8 {
		t.Fatalf("add = %v, want 8", data["add"])
	}
}

func TestExecuteVariableDefault(t *testing.T) {
	cs := buildExecSchema(t)
	q := `query Q($uid: ID = "1") { user(id: $uid){ name } }`
	out := execJSON(t, cs, Request{Query: q}) // no variables: default applies
	data := out["data"].(map[string]any)
	if data["user"].(map[string]any)["name"] != "Ann" {
		t.Fatalf("user.name = %v, want Ann", data["user"])
	}
}

func TestExecuteDefaultResolverStruct(t *testing.T) {
	cs := buildExecSchema(t)
	// id (json tag), name (field name) both via default resolution; age is
	// unexported and not selectable.
	out := execJSON(t, cs, Request{Query: `{ user(id:"1"){ id name } }`})
	user := out["data"].(map[string]any)["user"].(map[string]any)
	if user["id"] != "1" || user["name"] != "Ann" {
		t.Fatalf("default struct resolution = %v", user)
	}
}

func TestExecuteDefaultResolverMap(t *testing.T) {
	cs := buildExecSchema(t)
	out := execJSON(t, cs, Request{Query: `{ config { version debug } }`})
	cfg := out["data"].(map[string]any)["config"].(map[string]any)
	if cfg["version"] != "1.0" || cfg["debug"] != true {
		t.Fatalf("default map resolution = %v", cfg)
	}
}

func TestExecuteErrorCollectionWithPath(t *testing.T) {
	cs := buildExecSchema(t)
	res := Execute(context.Background(), cs, Request{Query: `{ user(id:"1"){ name boom } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly 1", res.Errors)
	}
	e := res.Errors[0]
	if !strings.Contains(e.Message, "boom") {
		t.Fatalf("error message = %q, want to contain boom", e.Message)
	}
	wantPath := []any{"user", "boom"}
	if len(e.Path) != len(wantPath) || e.Path[0] != "user" || e.Path[1] != "boom" {
		t.Fatalf("error path = %v, want %v", e.Path, wantPath)
	}
	// Sibling field still resolved; boom is null.
	user := res.Data.(map[string]any)["user"].(map[string]any)
	if user["name"] != "Ann" {
		t.Fatalf("sibling name = %v, want Ann (sibling must survive)", user["name"])
	}
	if user["boom"] != nil {
		t.Fatalf("boom = %v, want nil", user["boom"])
	}
}

func TestExecuteNonNullNullBubbling(t *testing.T) {
	cs := buildExecSchema(t)
	// nonNullName returns nil for a NonNull String: the field errors and the
	// null bubbles up to the parent user object (which is nullable -> null).
	res := Execute(context.Background(), cs, Request{Query: `{ user(id:"1"){ nonNullName } }`})
	if len(res.Errors) == 0 {
		t.Fatalf("expected a non-null error, got none")
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "non-nullable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %v, want a non-nullable message", res.Errors)
	}
	// user bubbles to null because its non-null child could not be provided.
	if got := res.Data.(map[string]any)["user"]; got != nil {
		t.Fatalf("user = %v, want nil (null bubbled from non-null child)", got)
	}
}

func TestExecuteMutation(t *testing.T) {
	cs := buildExecSchema(t)
	out := execJSON(t, cs, Request{Query: `mutation { rename(id:"1", name:"Zed") }`})
	data := out["data"].(map[string]any)
	if data["rename"] != "Zed" {
		t.Fatalf("rename = %v, want Zed", data["rename"])
	}
}

func TestExecuteInlineAndNamedFragments(t *testing.T) {
	cs := buildExecSchema(t)
	q := `
		{
			user(id:"1") {
				... on User { name }
				...userIDs
			}
		}
		fragment userIDs on User { id }
	`
	out := execJSON(t, cs, Request{Query: q})
	user := out["data"].(map[string]any)["user"].(map[string]any)
	if user["name"] != "Ann" || user["id"] != "1" {
		t.Fatalf("fragment resolution = %v", user)
	}
}

func TestExecuteTypename(t *testing.T) {
	cs := buildExecSchema(t)
	out := execJSON(t, cs, Request{Query: `{ user(id:"1"){ __typename } }`})
	user := out["data"].(map[string]any)["user"].(map[string]any)
	if user["__typename"] != "User" {
		t.Fatalf("__typename = %v, want User", user["__typename"])
	}
}

func TestExecuteParseError(t *testing.T) {
	cs := buildExecSchema(t)
	res := Execute(context.Background(), cs, Request{Query: `{ user(id:"1" }`})
	if res.Data != nil {
		t.Fatalf("data = %v, want nil on parse error", res.Data)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want 1 parse error", res.Errors)
	}
}

func TestExecuteUnknownField(t *testing.T) {
	cs := buildExecSchema(t)
	res := Execute(context.Background(), cs, Request{Query: `{ user(id:"1"){ nope } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want 1", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "Cannot query field") {
		t.Fatalf("error = %q, want 'Cannot query field'", res.Errors[0].Message)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests
// ---------------------------------------------------------------------------

func TestHandlerPostRoundTrip(t *testing.T) {
	cs := buildExecSchema(t)
	h := Handler(cs)

	body, _ := json.Marshal(httpRequest{
		Query:     `query Q($uid: ID!){ user(id:$uid){ name posts { title } } }`,
		Variables: map[string]any{"uid": "1"},
	})
	r := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Errors) != 0 {
		t.Fatalf("errors = %v, want none", resp.Errors)
	}
	user := resp.Data.(map[string]any)["user"].(map[string]any)
	if user["name"] != "Ann" {
		t.Fatalf("user.name = %v, want Ann", user["name"])
	}
}

func TestHandlerGetRoundTrip(t *testing.T) {
	cs := buildExecSchema(t)
	h := Handler(cs)

	u := "/graphql?query=" + url.QueryEscape(`{ echo(msg:"hi") }`)
	r := httptest.NewRequest(http.MethodGet, u, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Data.(map[string]any)["echo"] != "hi" {
		t.Fatalf("echo = %v, want hi", resp.Data)
	}
}

func TestHandlerGetWithVariables(t *testing.T) {
	cs := buildExecSchema(t)
	h := Handler(cs)

	q := url.QueryEscape(`query Q($uid: ID!){ user(id:$uid){ name } }`)
	vars := url.QueryEscape(`{"uid":"2"}`)
	r := httptest.NewRequest(http.MethodGet, "/graphql?query="+q+"&variables="+vars, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Data.(map[string]any)["user"].(map[string]any)["name"] != "Bob" {
		t.Fatalf("user.name = %v, want Bob", resp.Data)
	}
}

func TestHandlerMethodNotAllowed(t *testing.T) {
	cs := buildExecSchema(t)
	h := Handler(cs)
	r := httptest.NewRequest(http.MethodDelete, "/graphql", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow == "" {
		t.Fatalf("missing Allow header")
	}
}

func TestHandlerBadJSON(t *testing.T) {
	cs := buildExecSchema(t)
	h := Handler(cs)
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader("{not json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Errors) == 0 {
		t.Fatalf("want an error envelope")
	}
}

// TestFragmentCycleRejected guards against a stack-overflow DoS: a fragment
// that spreads itself (directly or transitively) must be rejected with a normal
// error envelope rather than driving execution into unbounded recursion.
func TestFragmentCycleRejected(t *testing.T) {
	cs := buildExecSchema(t)

	cases := map[string]string{
		"self": `query { user(id:"1") { ...F } }
			fragment F on User { id ...F }`,
		"mutual": `query { user(id:"1") { ...A } }
			fragment A on User { id ...B }
			fragment B on User { id ...A }`,
		"nested-field": `query { user(id:"1") { ...F } }
			fragment F on User { posts { id } ...F }`,
	}

	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			res := Execute(context.Background(), cs, Request{Query: q})
			if res.Data != nil {
				t.Fatalf("expected nil data on cyclic fragment, got %#v", res.Data)
			}
			if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "cycle") {
				t.Fatalf("expected a cycle error, got %#v", res.Errors)
			}
		})
	}
}

// TestFragmentDiamondAllowed ensures the cycle detector does not reject a valid
// diamond where two fragments both spread a common third fragment.
func TestFragmentDiamondAllowed(t *testing.T) {
	cs := buildExecSchema(t)
	q := `query { user(id:"1") { ...A ...B } }
		fragment A on User { ...C }
		fragment B on User { ...C }
		fragment C on User { id name }`
	res := Execute(context.Background(), cs, Request{Query: q})
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "cycle") {
			t.Fatalf("diamond wrongly flagged as cycle: %v", res.Errors)
		}
	}
	data, _ := res.Data.(map[string]any)
	user, _ := data["user"].(map[string]any)
	if user["name"] != "Ann" {
		t.Fatalf("expected resolved diamond user, got %#v", res.Data)
	}
}
