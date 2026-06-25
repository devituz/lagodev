package openapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devituz/lagodev/openapi"
)

// withinTimeout runs fn in a goroutine and fails the test if it does not
// complete within d. It is the guard for the "must not infinite-loop"
// reflection cases: a stack overflow aborts the process, but a non-overflowing
// hang (or a fix that loops without growing the stack) is caught here.
func withinTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer func() {
			// A panic still counts as "did not hang"; re-surface it.
			if r := recover(); r != nil {
				t.Errorf("panic during reflection: %v", r)
			}
			close(done)
		}()
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("reflection did not terminate within %s (suspected infinite recursion)", d)
	}
}

// typeHas reports whether a schema's "type" includes name, reading through a
// JSON round-trip so the test stays in the external package (typeField is
// unexported). type marshals as a bare string or a [string,...] union.
func typeHas(s *openapi.Schema, name string) bool {
	b, err := json.Marshal(s)
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	raw, ok := m["type"]
	if !ok {
		return false
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == name
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, v := range arr {
			if v == name {
				return true
			}
		}
	}
	return false
}

// --- Reflection edge cases ---

type selfRef struct {
	Name string   `json:"name"`
	Next *selfRef `json:"next,omitempty"`
}

type mutualA struct {
	B *mutualB `json:"b,omitempty"`
}

type mutualB struct {
	A *mutualA `json:"a,omitempty"`
}

type selfSlice struct {
	ID       int         `json:"id"`
	Children []selfSlice `json:"children,omitempty"`
}

type selfMap struct {
	ID    int                `json:"id"`
	Links map[string]selfMap `json:"links,omitempty"`
}

// TestSchemaOf_RecursiveInlineBounded asserts the inline SchemaFor path
// terminates on self-referential and mutually-recursive types instead of
// overflowing the stack.
func TestSchemaOf_RecursiveInlineBounded(t *testing.T) {
	cases := []reflect.Type{
		reflect.TypeOf(selfRef{}),
		reflect.TypeOf(mutualA{}),
		reflect.TypeOf(mutualB{}),
		reflect.TypeOf(selfSlice{}),
		reflect.TypeOf(selfMap{}),
	}
	for _, ty := range cases {
		ty := ty
		t.Run(ty.Name(), func(t *testing.T) {
			var s *openapi.Schema
			withinTimeout(t, 3*time.Second, func() {
				s = openapi.SchemaFor(ty)
			})
			if s == nil {
				t.Fatalf("nil schema for %s", ty)
			}
			// Must still marshal to valid JSON (bounded, not a cyclic graph).
			b, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("marshal %s: %v", ty, err)
			}
			if !json.Valid(b) {
				t.Fatalf("schema for %s not valid JSON", ty)
			}
		})
	}
}

// TestSchemaOf_RecursiveRegistryBounded asserts the registry path emits a $ref
// for a self-referential struct rather than looping.
func TestSchemaOf_RecursiveRegistryBounded(t *testing.T) {
	spec := openapi.New("rec", "1")
	var b []byte
	withinTimeout(t, 3*time.Second, func() {
		spec.Operation(http.MethodGet, "/n", openapi.OperationConfig{
			Responses: map[int]openapi.Response{
				200: openapi.JSONResponse[selfRef]("node"),
			},
		})
		var err error
		b, err = spec.JSON()
		if err != nil {
			t.Fatalf("JSON: %v", err)
		}
	})
	if !json.Valid(b) {
		t.Fatalf("recursive registry spec not valid JSON")
	}
	if !strings.Contains(string(b), "#/components/schemas/selfRef") {
		t.Errorf("recursive struct not emitted as a $ref component:\n%s", b)
	}
}

// TestSchemaOf_AnonymousStruct ensures anonymous (unnamed) struct fields are
// handled without panic and produce an object schema.
func TestSchemaOf_AnonymousStruct(t *testing.T) {
	v := struct {
		Inner struct {
			A int `json:"a"`
		} `json:"inner"`
		Items []struct {
			B string `json:"b"`
		} `json:"items"`
	}{}
	var s *openapi.Schema
	withinTimeout(t, 3*time.Second, func() {
		s = openapi.SchemaFor(reflect.TypeOf(v))
	})
	inner := s.Properties["inner"]
	if inner == nil || inner.Properties["a"] == nil {
		t.Fatalf("anonymous inner struct not expanded: %+v", s.Properties)
	}
	items := s.Properties["items"]
	if items == nil || items.Items == nil || items.Items.Properties["b"] == nil {
		t.Fatalf("anonymous struct slice not expanded: %+v", items)
	}
}

// TestSchemaOf_AssortedKinds exercises pointers, slices, maps, interfaces,
// time.Time, []byte and json tag variants in one struct.
func TestSchemaOf_AssortedKinds(t *testing.T) {
	type kitchen struct {
		Ptr       *int           `json:"ptr"`
		Slice     []string       `json:"slice"`
		Map       map[string]int `json:"map"`
		Any       any            `json:"any"`
		Iface     interface{}    `json:"iface"`
		When      time.Time      `json:"when"`
		Bytes     []byte         `json:"bytes"`
		Omit      string         `json:"omit,omitempty"`
		Skipped   string         `json:"-"`
		unexp     string         // unexported, must be ignored
		Renamed   string         `json:"renamed_field"`
		NoTagName string
	}
	var s *openapi.Schema
	withinTimeout(t, 3*time.Second, func() {
		s = openapi.SchemaFor(reflect.TypeOf(kitchen{}))
	})

	if p := s.Properties["ptr"]; p == nil || !typeHas(p, "null") || !typeHas(p, "integer") {
		t.Errorf("ptr should be nullable integer: %+v", p)
	}
	if sl := s.Properties["slice"]; sl == nil || !typeHas(sl, "array") {
		t.Errorf("slice type: %+v", sl)
	}
	if m := s.Properties["map"]; m == nil || m.AdditionalProperties == nil {
		t.Errorf("map should have additionalProperties: %+v", m)
	}
	if a := s.Properties["any"]; a == nil {
		t.Errorf("any field missing")
	}
	if w := s.Properties["when"]; w == nil || w.Format != "date-time" {
		t.Errorf("time.Time should be date-time: %+v", w)
	}
	if by := s.Properties["bytes"]; by == nil || by.Format != "byte" {
		t.Errorf("[]byte should be format byte: %+v", by)
	}
	if o := s.Properties["omit"]; o == nil || !typeHas(o, "null") {
		t.Errorf("omitempty should be nullable: %+v", o)
	}
	if _, ok := s.Properties["Skipped"]; ok {
		t.Errorf(`json:"-" field leaked`)
	}
	if _, ok := s.Properties["unexp"]; ok {
		t.Errorf("unexported field leaked")
	}
	if _, ok := s.Properties["renamed_field"]; !ok {
		t.Errorf("renamed field missing")
	}
	if _, ok := s.Properties["NoTagName"]; !ok {
		t.Errorf("untagged field should use Go name")
	}
}

// TestSchemaOf_EmbeddedRecursive guards the embedded-flatten branch against a
// type that embeds itself through a cycle.
func TestSchemaOf_EmbeddedRecursive(t *testing.T) {
	// embedSelf embeds a pointer to itself anonymously.
	withinTimeout(t, 3*time.Second, func() {
		_ = openapi.SchemaFor(reflect.TypeOf(embedSelf{}))
	})
}

type embedSelf struct {
	*embedSelf
	Value string `json:"value"`
}

// --- Spec validity / duplicate handling ---

func TestSpec_DuplicatePathMethodReplaced(t *testing.T) {
	spec := openapi.New("dup", "1")
	spec.Operation(http.MethodGet, "/x", openapi.OperationConfig{Summary: "first"})
	spec.Operation(http.MethodGet, "/x", openapi.OperationConfig{Summary: "second"})
	b, err := spec.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatal("not valid JSON")
	}
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	get := doc["paths"].(map[string]any)["/x"].(map[string]any)["get"].(map[string]any)
	if get["summary"] != "second" {
		t.Errorf("repeat method+path should replace; summary = %v", get["summary"])
	}
}

func TestSpec_ColonAndBraceCollapseToSamePath(t *testing.T) {
	spec := openapi.New("p", "1")
	spec.Operation(http.MethodGet, "/u/:id", openapi.OperationConfig{Summary: "colon"})
	spec.Operation(http.MethodPost, "/u/{id}", openapi.OperationConfig{Summary: "brace"})
	var doc map[string]any
	b, _ := spec.JSON()
	_ = json.Unmarshal(b, &doc)
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/u/{id}"]; !ok {
		t.Fatalf("colon path not normalised to brace: %v", paths)
	}
	if _, ok := paths["/u/:id"]; ok {
		t.Errorf("colon form should not coexist with brace form")
	}
	methods := paths["/u/{id}"].(map[string]any)
	if _, ok := methods["get"]; !ok {
		t.Errorf("get lost after merge")
	}
	if _, ok := methods["post"]; !ok {
		t.Errorf("post lost after merge")
	}
}

func TestSpec_EmptyMapRoundTrip(t *testing.T) {
	spec := openapi.New("", "")
	b, err := spec.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatal("empty spec not valid JSON")
	}
	m, err := spec.Map()
	if err != nil {
		t.Fatal(err)
	}
	if m["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v", m["openapi"])
	}
	if _, ok := m["paths"]; !ok {
		t.Errorf("paths key must be present even when empty")
	}
}

// --- SwaggerHTML / Handler injection safety ---

func TestSwaggerHTML_EscapesTitle(t *testing.T) {
	html := openapi.SwaggerHTML(`</title><script>alert(1)</script>`, "/openapi.json")
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Fatalf("title not escaped, XSS possible:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("title not HTML-escaped")
	}
}

func TestSwaggerHTML_EscapesSpecURLIntoScript(t *testing.T) {
	// A spec URL that tries to break out of the JS string and close the script.
	html := openapi.SwaggerHTML("t", `"</script><script>evil()</script>`)
	if strings.Contains(html, "</script><script>evil()") {
		t.Fatalf("spec URL broke out of script context:\n%s", html)
	}
	// The < and > inside the JS string must be \u-escaped so no </script>
	// sequence appears verbatim inside the embedded literal.
	if strings.Contains(html, `url: "</script>`) {
		t.Errorf("spec URL not safely encoded into JS string")
	}
}

func TestDocsHandler_ServesEscapedHTML(t *testing.T) {
	spec := openapi.New(`<b>API</b>`, "1")
	rec := httptest.NewRecorder()
	spec.DocsHandler("/openapi.json").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<b>API</b>") {
		t.Errorf("title not escaped in served docs page")
	}
	if !strings.Contains(body, "/openapi.json") {
		t.Errorf("spec url not embedded")
	}
}

func TestHandler_HeadHasNoBody(t *testing.T) {
	spec := openapi.New("h", "1")
	rec := httptest.NewRecorder()
	spec.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD should have empty body, got %d bytes", rec.Body.Len())
	}
}

// --- FuzzSpecJSON: fuzz user-controlled strings flowing into JSON/HTML ---

func FuzzSpecJSON(f *testing.F) {
	f.Add("Get a user", "Returns a user", "users", "/users/{id}")
	f.Add(`</script><script>x</script>`, "hello \"quote\" and \\ slash", "tag,with,comma", `/p/"a"/<b>`)
	f.Add("\x00\x01\x02", "emoji 😀 and 漢字", "", "/")
	f.Add(strings.Repeat("A", 500), "", "t", "/x")

	f.Fuzz(func(t *testing.T, summary, desc, tag, path string) {
		spec := openapi.New(summary, "1.0.0")
		spec.Description = desc
		spec.AddServer("https://h"+path, desc)
		spec.Operation(http.MethodGet, path, openapi.OperationConfig{
			Summary:     summary,
			Description: desc,
			Tags:        []string{tag},
			OperationID: summary,
			Query: []openapi.Param{
				{Name: tag, Description: desc, Required: true},
			},
			Responses: map[int]openapi.Response{
				200: openapi.JSONResponse[User](desc),
			},
		})

		b, err := spec.JSON()
		if err != nil {
			t.Fatalf("JSON error on inputs: %v", err)
		}
		if !json.Valid(b) {
			t.Fatalf("spec.JSON produced invalid JSON for inputs summary=%q desc=%q path=%q", summary, desc, path)
		}
		// Round-trip must preserve the summary string exactly.
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("round-trip unmarshal: %v", err)
		}

		// Swagger HTML must remain a single, non-broken script context: the
		// title and spec URL are user-influenced here via spec.Title/specURL.
		html := openapi.SwaggerHTML(summary, "/openapi.json?d="+desc)
		if strings.Contains(html, "</script><script>") {
			t.Fatalf("script breakout in SwaggerHTML for summary=%q desc=%q", summary, desc)
		}
		if strings.Contains(html, "<script>alert") {
			t.Fatalf("unescaped script injected via title=%q", summary)
		}
	})
}
