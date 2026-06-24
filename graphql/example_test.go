package graphql_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"

	"github.com/devituz/lagodev/graphql"
)

// user is the in-memory record a real resolver would load from a store.
type user struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// userStore is fixed, in-memory data so the example is deterministic.
var userStore = map[string]*user{
	"1": {ID: "1", Name: "Ann", Role: "admin"},
	"2": {ID: "2", Name: "Bob", Role: "member"},
	"3": {ID: "3", Name: "Cleo", Role: "member"},
}

// postStore maps a user ID to their posts.
var postStore = map[string][]map[string]any{
	"1": {{"id": "10", "title": "Hello"}, {"id": "11", "title": "World"}},
	"2": {{"id": "20", "title": "Notes"}},
	"3": {},
}

// buildBlogSchema wires a small Query -> User -> [Post] schema over the
// in-memory stores above. It is shared by the examples below.
func buildBlogSchema() *graphql.CompiledSchema {
	postType := &graphql.Object{
		Name: "Post",
		Fields: graphql.Fields{
			"id":    &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"title": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	}

	userType := &graphql.Object{
		Name: "User",
		Fields: graphql.Fields{
			"id":   &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},     // default resolution (json tag)
			"name": &graphql.Field{Type: graphql.NewNonNull(graphql.String)}, // default resolution
			"role": &graphql.Field{Type: graphql.NewNonNull(graphql.String)}, // default resolution
			"posts": &graphql.Field{
				Type: graphql.NewList(graphql.NewNonNull(postType)),
				Resolve: func(_ context.Context, parent any, _ graphql.ArgValues) (any, error) {
					u, _ := parent.(*user)
					if u == nil {
						return nil, nil
					}
					out := make([]any, 0, len(postStore[u.ID]))
					for _, p := range postStore[u.ID] {
						out = append(out, p)
					}
					return out, nil
				},
			},
		},
	}

	queryType := &graphql.Object{
		Name: "Query",
		Fields: graphql.Fields{
			// user(id: ID!) — single lookup by argument.
			"user": &graphql.Field{
				Type: userType,
				Args: graphql.Args{"id": {Type: graphql.NewNonNull(graphql.ID)}},
				Resolve: func(_ context.Context, _ any, args graphql.ArgValues) (any, error) {
					return userStore[args.String("id")], nil
				},
			},
			// users(role: String) — filtered, sorted list (deterministic order).
			"users": &graphql.Field{
				Type: graphql.NewList(graphql.NewNonNull(userType)),
				Args: graphql.Args{"role": {Type: graphql.String}},
				Resolve: func(_ context.Context, _ any, args graphql.ArgValues) (any, error) {
					want := args.String("role")
					ids := make([]string, 0, len(userStore))
					for id := range userStore {
						ids = append(ids, id)
					}
					sort.Strings(ids) // stable output independent of map order
					out := make([]any, 0, len(ids))
					for _, id := range ids {
						u := userStore[id]
						if want == "" || u.Role == want {
							out = append(out, u)
						}
					}
					return out, nil
				},
			},
		},
	}

	cs, err := graphql.NewSchema(graphql.Schema{Query: queryType})
	if err != nil {
		panic(err)
	}
	return cs
}

// Example shows the everyday path: build a schema, then Execute a query with a
// nested selection, a literal argument and a variable, and marshal the result.
func Example() {
	cs := buildBlogSchema()

	// Nested selection (user -> posts), an argument on the inner list is not
	// needed here; the variable $role drives the top-level users(role:) field.
	query := `
		query Feed($role: String) {
		  admins: users(role: $role) {
		    name
		    role
		    posts { id title }
		  }
		  one: user(id: "2") {
		    name
		  }
		}`

	res := graphql.Execute(context.Background(), cs, graphql.Request{
		Query:     query,
		Variables: map[string]any{"role": "admin"},
	})

	out, _ := json.Marshal(res)
	fmt.Println(string(out))
	// Output:
	// {"data":{"admins":[{"name":"Ann","posts":[{"id":"10","title":"Hello"},{"id":"11","title":"World"}],"role":"admin"}],"one":{"name":"Bob"}}}
}

// ExampleHandler serves the schema over HTTP and performs a POST round-trip
// with httptest, printing the raw JSON response body the client receives.
func ExampleHandler() {
	cs := buildBlogSchema()
	srv := httptest.NewServer(graphql.Handler(cs))
	defer srv.Close()

	body := `{"query":"{ user(id:\"1\"){ name role posts { title } } }"}`
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var buf strings.Builder
	dec := json.NewDecoder(resp.Body)
	var v any
	if err := dec.Decode(&v); err != nil {
		panic(err)
	}
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}

	fmt.Print(buf.String())
	// Output:
	// {"data":{"user":{"name":"Ann","posts":[{"title":"Hello"},{"title":"World"}],"role":"admin"}}}
}
