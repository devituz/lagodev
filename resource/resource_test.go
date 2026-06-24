package resource_test

import (
	"encoding/json"
	"testing"

	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/resource"
)

// --- test models & resources -------------------------------------------------

type post struct {
	ID    int
	Title string
}

type user struct {
	ID       int
	Name     string
	Email    string
	Password string // must never appear in output
	Admin    bool
	Posts    []post
}

// postResource: rename, plain mapping.
var postResource = resource.Func[post](func(p post) any {
	return resource.Fields{
		"id":      p.ID,
		"heading": p.Title, // renamed from "title"
	}
})

// userResource exercises rename + computed + conditional + embedded relation.
var userResource = resource.Func[user](func(u user) any {
	return resource.Fields{
		"id":           u.ID,
		"name":         u.Name,
		"display_name": "@" + u.Name, // computed
		"posts":        resource.EmbedMany(u.Posts, postResource),
	}.
		When(u.Admin, "is_admin", true) // conditional
})

// marshal is a helper to compare JSON shape via round-trip into map.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func assertJSONEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("unmarshal got: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("unmarshal want: %v (%s)", err, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gb, wb)
	}
}

// --- tests -------------------------------------------------------------------

func TestItem_RenameComputedConditional_Admin(t *testing.T) {
	u := user{ID: 1, Name: "neo", Email: "neo@example.com", Password: "secret", Admin: true}
	out := resource.Item(u, userResource)

	assertJSONEqual(t, mustJSON(t, out), `{
		"id": 1,
		"name": "neo",
		"display_name": "@neo",
		"posts": [],
		"is_admin": true
	}`)
}

func TestItem_ConditionalExcludedForNonAdmin(t *testing.T) {
	u := user{ID: 2, Name: "trinity", Admin: false}
	got := mustJSON(t, resource.Item(u, userResource))

	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["is_admin"]; ok {
		t.Fatalf("is_admin must be absent for non-admin, got: %s", got)
	}
	if _, ok := m["password"]; ok {
		t.Fatalf("password must never be serialised, got: %s", got)
	}
}

func TestItem_EmbeddedRelation(t *testing.T) {
	u := user{
		ID:    3,
		Name:  "morpheus",
		Posts: []post{{ID: 10, Title: "Red Pill"}, {ID: 11, Title: "Blue Pill"}},
	}
	assertJSONEqual(t, mustJSON(t, resource.Item(u, userResource)), `{
		"id": 3,
		"name": "morpheus",
		"display_name": "@morpheus",
		"posts": [
			{"id": 10, "heading": "Red Pill"},
			{"id": 11, "heading": "Blue Pill"}
		]
	}`)
}

func TestCollection_Wrapping(t *testing.T) {
	posts := []post{{ID: 1, Title: "A"}, {ID: 2, Title: "B"}}
	assertJSONEqual(t, mustJSON(t, resource.Collection(posts, postResource)), `{
		"data": [
			{"id": 1, "heading": "A"},
			{"id": 2, "heading": "B"}
		]
	}`)
}

func TestCollection_EmptyRendersEmptyArray(t *testing.T) {
	got := mustJSON(t, resource.Collection([]post(nil), postResource))
	assertJSONEqual(t, got, `{"data": []}`)
}

func TestCollection_WithMeta(t *testing.T) {
	posts := []post{{ID: 1, Title: "A"}}
	out := resource.Collection(posts, postResource).WithMeta(map[string]any{"count": 1})
	assertJSONEqual(t, mustJSON(t, out), `{
		"data": [{"id": 1, "heading": "A"}],
		"meta": {"count": 1}
	}`)
}

func TestPaginated_DerivesMetaFromPaginator(t *testing.T) {
	p := &orm.Paginator[post]{
		Data:     []post{{ID: 5, Title: "X"}, {ID: 6, Title: "Y"}},
		Total:    42,
		Page:     2,
		PerPage:  2,
		LastPage: 21,
		HasMore:  true,
	}
	out := resource.Paginated(p, postResource)

	assertJSONEqual(t, mustJSON(t, out), `{
		"data": [
			{"id": 5, "heading": "X"},
			{"id": 6, "heading": "Y"}
		],
		"meta": {
			"total": 42,
			"page": 2,
			"per_page": 2,
			"last_page": 21,
			"has_more": true
		}
	}`)
}

func TestPaginated_WithLinksAndMeta(t *testing.T) {
	p := &orm.Paginator[post]{
		Data:     []post{{ID: 1, Title: "X"}},
		Total:    30,
		Page:     2,
		PerPage:  10,
		LastPage: 3,
		HasMore:  true,
	}
	out := resource.Paginated(p, postResource).
		WithLinks("/api/posts").
		WithMeta(map[string]any{"server_time": "now"})

	assertJSONEqual(t, mustJSON(t, out), `{
		"data": [{"id": 1, "heading": "X"}],
		"meta": {"total": 30, "page": 2, "per_page": 10, "last_page": 3, "has_more": true},
		"links": {
			"first": "/api/posts?page=1",
			"last":  "/api/posts?page=3",
			"prev":  "/api/posts?page=1",
			"next":  "/api/posts?page=3"
		},
		"with": {"server_time": "now"}
	}`)
}

func TestPaginated_NilPaginator(t *testing.T) {
	out := resource.Paginated[post](nil, postResource)
	assertJSONEqual(t, mustJSON(t, out), `{"data": [], "meta": {"total":0,"page":0,"per_page":0,"last_page":0,"has_more":false}}`)
}

func TestPaginated_LinksAppendWithExistingQuery(t *testing.T) {
	p := &orm.Paginator[post]{Page: 1, LastPage: 2, HasMore: true}
	out := resource.Paginated(p, postResource).WithLinks("/api/posts?sort=id")
	if out.Links.Next != "/api/posts?sort=id&page=2" {
		t.Fatalf("expected & separator, got %q", out.Links.Next)
	}
	if out.Links.Prev != "" {
		t.Fatalf("page 1 must have no prev link, got %q", out.Links.Prev)
	}
}

// --- Fields helpers ----------------------------------------------------------

func TestFields_OnlyExceptRename(t *testing.T) {
	f := resource.Fields{"a": 1, "b": 2, "c": 3}

	if got := mustJSON(t, f.Only("a", "c")); got != `{"a":1,"c":3}` {
		t.Fatalf("Only: %s", got)
	}
	if got := mustJSON(t, resource.Fields{"a": 1, "b": 2}.Except("b")); got != `{"a":1}` {
		t.Fatalf("Except: %s", got)
	}
	if got := mustJSON(t, resource.Fields{"old": 7}.Rename("old", "new")); got != `{"new":7}` {
		t.Fatalf("Rename: %s", got)
	}
}

func TestFields_OmitEmpty(t *testing.T) {
	f := resource.Fields{
		"name":  "set",
		"empty": "",
		"zero":  0,
		"nilp":  ([]int)(nil),
	}.OmitEmpty()
	assertJSONEqual(t, mustJSON(t, f), `{"name":"set"}`)
}

func TestFields_WhenUnlessNilSafe(t *testing.T) {
	var f resource.Fields // nil
	f = f.When(true, "a", 1).Unless(true, "b", 2).WhenFunc(false, "c", func() any { panic("must not run") })
	assertJSONEqual(t, mustJSON(t, f), `{"a":1}`)
}

func TestEmbed_NilModelIsNull(t *testing.T) {
	var p *post
	embedResource := resource.Func[*post](func(p *post) any {
		return resource.Fields{"id": p.ID}
	})
	got := mustJSON(t, resource.Fields{"author": resource.Embed(p, embedResource)})
	assertJSONEqual(t, got, `{"author": null}`)
}

// TestResource_InterfaceImplementation verifies a named-type Resource works
// alongside the Func adapter.
type namedPostResource struct{}

func (namedPostResource) Transform(p post) any {
	return resource.Fields{"id": p.ID, "heading": p.Title}
}

func TestResource_NamedInterface(t *testing.T) {
	out := resource.Item(post{ID: 9, Title: "Z"}, namedPostResource{})
	assertJSONEqual(t, mustJSON(t, out), `{"id":9,"heading":"Z"}`)
}
