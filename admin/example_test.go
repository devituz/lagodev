package admin_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/devituz/lagodev/admin"
)

// Example shows a real user registering a model with the admin Panel against
// the shipped in-memory SliceSource, mounting Handler(), and driving it over
// HTTP with httptest: it fetches the resource index and a list page and proves
// the resource is listed by asserting on stable rendered substrings and the
// HTTP status codes (never the volatile CSRF token or full HTML).
func Example() {
	// A domain model: its struct tags drive column/field discovery.
	type User struct {
		ID    uint64 `column:"id" orm:"primary"`
		Name  string `column:"name"`
		Email string `column:"email"`
	}

	// In-memory data source seeded with two rows.
	src := admin.NewSliceSource("id")
	src.Seed(
		map[string]any{"id": 1, "name": "Ada", "email": "ada@example.com"},
		map[string]any{"id": 2, "name": "Linus", "email": "linus@example.com"},
	)

	// Register the model and mount the panel under /admin. The panel is
	// fail-closed: with no Authorizer it denies every request, so this example
	// opts out of the gate with WithInsecureAllowAll for a self-contained demo.
	// Production code installs WithAuthorizer (and/or upstream auth middleware)
	// instead.
	p := admin.New(admin.WithTitle("Acme Admin"), admin.WithInsecureAllowAll())
	p.Register(User{}, admin.Resource{
		Source: src,
		Search: []string{"name", "email"},
	})
	mux := http.NewServeMux()
	mux.Handle("/admin/", http.StripPrefix("/admin", p.Handler()))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// Index page: lists the registered resource and links to its list view.
	idxStatus, idxBody := get("/admin/")
	fmt.Println("index status:", idxStatus)
	fmt.Println("index lists User:", strings.Contains(idxBody, "<td>User</td>"))
	fmt.Println("index links to list:", strings.Contains(idxBody, `href="/admin/user"`))

	// List page: shows both seeded rows with the deterministic total counter.
	listStatus, listBody := get("/admin/user")
	fmt.Println("list status:", listStatus)
	fmt.Println("list shows Ada:", strings.Contains(listBody, "<td>Ada</td>"))
	fmt.Println("list shows Linus:", strings.Contains(listBody, "<td>linus@example.com</td>"))
	fmt.Println("list total:", strings.Contains(listBody, "2 total"))

	// Output:
	// index status: 200
	// index lists User: true
	// index links to list: true
	// list status: 200
	// list shows Ada: true
	// list shows Linus: true
	// list total: true
}
