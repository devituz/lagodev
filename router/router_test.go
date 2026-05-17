package router_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/router"
)

type fakeCtrl struct{ calls *[]string }

func (c fakeCtrl) Index(http.ResponseWriter, *http.Request)   { *c.calls = append(*c.calls, "index") }
func (c fakeCtrl) Show(http.ResponseWriter, *http.Request)    { *c.calls = append(*c.calls, "show") }
func (c fakeCtrl) Store(http.ResponseWriter, *http.Request)   { *c.calls = append(*c.calls, "store") }
func (c fakeCtrl) Update(http.ResponseWriter, *http.Request)  { *c.calls = append(*c.calls, "update") }
func (c fakeCtrl) Destroy(http.ResponseWriter, *http.Request) { *c.calls = append(*c.calls, "destroy") }

func TestResource_RegistersFiveRoutes(t *testing.T) {
	r := router.New()
	r.Resource("posts", fakeCtrl{calls: new([]string)})

	rs := r.Routes()
	// GET, GET /{id}, POST, PUT, PATCH, DELETE  = 6
	require.Len(t, rs, 6)
	paths := map[string]string{}
	for _, rt := range rs {
		paths[rt.Method+" "+rt.Path] = "x"
	}
	assert.Contains(t, paths, "GET /posts")
	assert.Contains(t, paths, "GET /posts/{id}")
	assert.Contains(t, paths, "POST /posts")
	assert.Contains(t, paths, "PUT /posts/{id}")
	assert.Contains(t, paths, "PATCH /posts/{id}")
	assert.Contains(t, paths, "DELETE /posts/{id}")
}

func TestGroup_AppliesPrefixAndMiddleware(t *testing.T) {
	hits := []string{}
	mark := func(label string) router.Middleware {
		return func(next router.Handler) router.Handler {
			return func(w http.ResponseWriter, r *http.Request) {
				hits = append(hits, label)
				next(w, r)
			}
		}
	}

	r := router.New()
	r.Use(mark("outer"))
	r.Group("/api/v1", func(g *router.Router) {
		g.Use(mark("inner"))
		g.Get("/users", func(w http.ResponseWriter, _ *http.Request) {
			hits = append(hits, "handler")
			w.WriteHeader(204)
		})
	})

	rs := r.Routes()
	require.Len(t, rs, 1)
	assert.Equal(t, "/api/v1/users", rs[0].Path)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	rs[0].Handler(rec, req)
	assert.Equal(t, 204, rec.Code)
	assert.Equal(t, []string{"outer", "inner", "handler"}, hits)
}

func TestMountStd(t *testing.T) {
	r := router.New()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mux := http.NewServeMux()
	r.MountStd(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/health", nil)
	mux.ServeHTTP(rec, req)
	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestApply_InvokesCallback(t *testing.T) {
	r := router.New()
	r.Get("/a", nil)
	r.Post("/b", nil)

	seen := [][2]string{}
	r.Apply(func(method, path string, _ router.Handler) {
		seen = append(seen, [2]string{method, path})
	})
	assert.Equal(t, [][2]string{{"GET", "/a"}, {"POST", "/b"}}, seen)
}
