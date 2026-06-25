package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// securePanel builds a panel + fake store with two seeded rows and applies the
// supplied options verbatim — unlike newPanel it adds NO default allow-all gate,
// so it is the right harness for exercising the fail-closed default and explicit
// Authorizer behaviour.
func securePanel(t *testing.T, opts ...Option) (*Panel, *fakeStore) {
	t.Helper()
	store := newFakeStore("id", "deleted_at")
	store.seed(
		map[string]any{"id": 1, "name": "Ada", "email_addr": "ada@example.com", "age": 36, "active": true},
		map[string]any{"id": 2, "name": "Linus", "email_addr": "linus@kernel.org", "age": 54, "active": true},
	)
	p := New(opts...)
	p.Register(SampleUser{}, Resource{
		Source:  store,
		Search:  []string{"name", "email_addr"},
		Filters: []string{"active"},
		PerPage: 10,
	})
	return p, store
}

// secureRoutes enumerates every reachable route with the method that reaches its
// handler past dispatch. Mutating routes carry a (separately validated) CSRF
// token so that an authz failure — not a CSRF failure — is what we observe.
func secureRoutes() []struct {
	name, method, target string
	mutating             bool
} {
	return []struct {
		name, method, target string
		mutating             bool
	}{
		{"index", http.MethodGet, "/", false},
		{"list", http.MethodGet, "/sample_user", false},
		{"view", http.MethodGet, "/sample_user/1", false},
		{"new_form", http.MethodGet, "/sample_user/new", false},
		{"create", http.MethodPost, "/sample_user/new", true},
		{"update", http.MethodPost, "/sample_user/1", true},
		{"delete", http.MethodPost, "/sample_user/1/delete", true},
	}
}

// TestFailClosedByDefault is the core production-safety guarantee: a panel built
// with no Authorizer and no explicit opt-in must DENY every route with 403 and
// must not leak any record data into the response body.
func TestFailClosedByDefault(t *testing.T) {
	p, store := securePanel(t) // no WithAuthorizer, no WithInsecureAllowAll

	for _, rt := range secureRoutes() {
		var form url.Values
		var csrf string
		if rt.mutating {
			// A valid CSRF pair, so the only thing standing between the request
			// and the handler body is authorization.
			csrf = tokenFrom(t, mustAllowAll(t), "/sample_user")
			form = url.Values{"csrf": {csrf}, "name": {"x"}, "email_addr": {"x@x"}}
		}
		rec := do(t, p, rt.method, rt.target, form, csrf)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 (fail-closed default)", rt.name, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "Ada") || strings.Contains(body, "ada@example.com") ||
			strings.Contains(body, "Linus") || strings.Contains(body, "kernel.org") {
			t.Errorf("%s: denied response leaked record data:\n%s", rt.name, body)
		}
	}

	// No mutation may have reached the store.
	if !store.exists("1") || !store.exists("2") {
		t.Errorf("seeded rows mutated despite fail-closed denial")
	}
	if store.deleted("1") {
		t.Errorf("row deleted despite fail-closed denial")
	}
}

// mustAllowAll returns a separate allow-all panel used only to mint a valid CSRF
// token in the fail-closed test (the denied panel never renders a form).
func mustAllowAll(t *testing.T) *Panel {
	t.Helper()
	p, _ := securePanel(t, WithInsecureAllowAll())
	return p
}

// TestDenyingAuthorizerBlocksEveryRoute asserts that an Authorizer returning
// false for everything yields 403 on every route and leaks no data — the same
// guarantee as the default, but reached through the explicit hook.
func TestDenyingAuthorizerBlocksEveryRoute(t *testing.T) {
	deny := WithAuthorizer(func(_ *http.Request, _, _ string) bool { return false })
	p, store := securePanel(t, deny)

	for _, rt := range secureRoutes() {
		var form url.Values
		var csrf string
		if rt.mutating {
			csrf = tokenFrom(t, mustAllowAll(t), "/sample_user")
			form = url.Values{"csrf": {csrf}, "name": {"x"}, "email_addr": {"x@x"}}
		}
		rec := do(t, p, rt.method, rt.target, form, csrf)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403 (deny authorizer)", rt.name, rec.Code)
		}
		if b := rec.Body.String(); strings.Contains(b, "Ada") || strings.Contains(b, "Linus") {
			t.Errorf("%s: denied response leaked record data:\n%s", rt.name, b)
		}
	}
	if store.deleted("1") || !store.exists("1") {
		t.Errorf("store mutated despite deny authorizer")
	}
}

// TestAllowingAuthorizerPermitsRoutes asserts that an Authorizer returning true
// lets read routes render normally (no opt-in needed).
func TestAllowingAuthorizerPermitsRoutes(t *testing.T) {
	allow := WithAuthorizer(func(_ *http.Request, _, _ string) bool { return true })
	p, _ := securePanel(t, allow)

	for _, c := range []struct{ name, target, want string }{
		{"index", "/", "Sample User"},
		{"list", "/sample_user", "Ada"},
		{"view", "/sample_user/1", `value="Ada"`},
		{"new_form", "/sample_user/new", `name="name"`},
	} {
		rec := do(t, p, http.MethodGet, c.target, nil, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (allow authorizer)", c.name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: body missing %q:\n%s", c.name, c.want, rec.Body.String())
		}
	}

	// A mutating route also succeeds when authorized + CSRF-valid.
	csrf := tokenFrom(t, p, "/sample_user")
	form := url.Values{"csrf": {csrf}, "name": {"Grace"}, "email_addr": {"grace@navy.mil"}, "age": {"85"}}
	rec := do(t, p, http.MethodPost, "/sample_user/new", form, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("authorized create status = %d, want 303", rec.Code)
	}
}

// TestStoredXSSEscapedEverywhere seeds adversarial record fields containing
// <script> and breakout sequences and asserts they are HTML-escaped in every
// rendered surface (list cells, edit-form input values, search echo), i.e. no
// stored XSS reaches the browser raw.
func TestStoredXSSEscapedEverywhere(t *testing.T) {
	const payload = `<script>alert('xss')</script>`
	const attrBreak = `"><img src=x onerror=alert(1)>`

	store := newFakeStore("id", "")
	store.seed(map[string]any{
		"id":         1,
		"name":       payload,
		"email_addr": attrBreak,
		"age":        7,
	})
	p := New(WithInsecureAllowAll())
	p.Register(SampleUser{}, Resource{
		Source:  store,
		Search:  []string{"name"},
		Filters: []string{"name"},
	})

	// 1. List view: cell rendering of the payload.
	rec := do(t, p, http.MethodGet, "/sample_user", nil, "")
	body := rec.Body.String()
	if strings.Contains(body, payload) {
		t.Errorf("list: raw <script> payload present (stored XSS):\n%s", body)
	}
	if strings.Contains(body, `onerror=alert(1)>`) && !strings.Contains(body, "&gt;") {
		t.Errorf("list: attribute-breakout payload not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("list: expected escaped &lt;script&gt; in output:\n%s", body)
	}

	// 2. Edit form: the payload flows into a value="" attribute and must be
	// attribute-escaped (no premature quote close).
	rec = do(t, p, http.MethodGet, "/sample_user/1", nil, "")
	body = rec.Body.String()
	if strings.Contains(body, payload) {
		t.Errorf("form: raw <script> payload present in input value:\n%s", body)
	}
	if strings.Contains(body, `value="">`) {
		t.Errorf("form: attribute breakout closed the value attribute:\n%s", body)
	}

	// 3. Search echo: the search box reflects the query back into a value="".
	rec = do(t, p, http.MethodGet, "/sample_user?q="+url.QueryEscape(payload), nil, "")
	body = rec.Body.String()
	if strings.Contains(body, payload) {
		t.Errorf("search echo: raw <script> reflected (reflected XSS):\n%s", body)
	}
}

// TestParamInjectionAndTraversalContained asserts that hostile slug/id/page/
// filter params cannot traverse paths, panic the handler, or escape the gate.
func TestParamInjectionAndTraversalContained(t *testing.T) {
	p, store := securePanel(t, WithInsecureAllowAll())

	cases := []struct {
		name, target string
		wantCode     int
	}{
		// Path traversal in the slug segment resolves to an unknown resource.
		{"traversal_slug", "/..%2f..%2fetc%2fpasswd", http.StatusNotFound},
		{"dotdot_slug", "/../../admin.go", http.StatusNotFound},
		{"unknown_slug", "/no_such_model", http.StatusNotFound},
		// Hostile id: non-existent row -> 404, never a leak or panic.
		{"injection_id", "/sample_user/1%20OR%201=1", http.StatusNotFound},
		{"path_in_id", "/sample_user/..%2f1", http.StatusNotFound},
		// Pagination overflow (the earlier integer-overflow fix must hold): a
		// page near math.MaxInt must yield a normal 200, not a panic.
		{"overflow_page", "/sample_user?page=9223372036854775807", http.StatusOK},
		{"overflow_page2", "/sample_user?page=4611686018427387905", http.StatusOK},
		{"negative_page", "/sample_user?page=-5", http.StatusOK},
		{"garbage_page", "/sample_user?page=NaN", http.StatusOK},
		// Filter injection: an unknown filter key is ignored, not executed.
		{"filter_injection", "/sample_user?filter_active=1%27%20OR%20%271", http.StatusOK},
	}
	for _, c := range cases {
		rec := do(t, p, http.MethodGet, c.target, nil, "")
		if rec.Code != c.wantCode {
			t.Errorf("%s: %s -> %d, want %d\nbody:\n%s", c.name, c.target, rec.Code, c.wantCode, rec.Body.String())
		}
	}

	// The store's rows are untouched by any of the probes.
	if !store.exists("1") || !store.exists("2") || store.deleted("1") {
		t.Errorf("injection probes mutated the store")
	}
}
