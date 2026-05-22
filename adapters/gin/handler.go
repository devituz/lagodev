package lagogin

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Handler is the lagogin-style handler signature: `(any, error)`. The H()
// wrapper converts it into a `gin.HandlerFunc` with automatic status
// mapping. Use it like:
//
//	r.GET("/users/:id", lagogin.H(func(c *lagogin.Ctx) (any, error) {
//	    return svc.Get(c.Ctx(), c.ParamUint("id"))
//	}))
type Handler func(c *Ctx) (any, error)

// H converts a Handler into a gin.HandlerFunc. orm.ErrNotFound is mapped
// to 404, validation errors to 422, any other error to 500. A `(value, nil)`
// return is JSON-encoded with status 200; `(nil, nil)` becomes 204.
func H(h Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := newCtx(c)
		value, err := h(ctx)
		ctx.respond(value, err)
	}
}

// HS converts a series of handlers into gin.HandlerFunc slice for routes
// that take a final handler plus middleware. Equivalent to manually
// calling H on each.
func HS(handlers ...Handler) []gin.HandlerFunc {
	out := make([]gin.HandlerFunc, len(handlers))
	for i, h := range handlers {
		out[i] = H(h)
	}
	return out
}

// ResourceController is the contract for Resource(r, name, ctrl). All 5
// methods must be implemented; leave any of them returning (nil, nil) if
// you don't need the endpoint.
type ResourceController interface {
	Index(c *Ctx) (any, error)
	Show(c *Ctx) (any, error)
	Store(c *Ctx) (any, error)
	Update(c *Ctx) (any, error)
	Destroy(c *Ctx) (any, error)
}

// IRouter is the minimum gin router surface lagogin needs. *gin.Engine and
// *gin.RouterGroup both satisfy it.
type IRouter interface {
	GET(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	POST(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	PUT(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	PATCH(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	DELETE(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
}

// Resource registers 5 canonical RESTful routes for a controller in one call:
//
//	GET    /posts            → ctrl.Index
//	GET    /posts/:id        → ctrl.Show
//	POST   /posts            → ctrl.Store
//	PUT    /posts/:id        → ctrl.Update
//	PATCH  /posts/:id        → ctrl.Update
//	DELETE /posts/:id        → ctrl.Destroy
//
// `name` should be the plural resource name (e.g. "posts", "users"). The
// registered routes are also recorded in the lagogin route registry so
// OpenAPI() and PrintRoutes() can introspect them later.
func Resource(r IRouter, name string, ctrl ResourceController) {
	base := "/" + strings.Trim(name, "/")
	r.GET(base, H(ctrl.Index))
	r.GET(base+"/:id", H(ctrl.Show))
	r.POST(base, H(ctrl.Store))
	r.PUT(base+"/:id", H(ctrl.Update))
	r.PATCH(base+"/:id", H(ctrl.Update))
	r.DELETE(base+"/:id", H(ctrl.Destroy))

	register(routeInfo{Method: "GET", Path: base, Resource: name, Op: "index"})
	register(routeInfo{Method: "GET", Path: base + "/{id}", Resource: name, Op: "show"})
	register(routeInfo{Method: "POST", Path: base, Resource: name, Op: "store"})
	register(routeInfo{Method: "PUT", Path: base + "/{id}", Resource: name, Op: "update"})
	register(routeInfo{Method: "PATCH", Path: base + "/{id}", Resource: name, Op: "update"})
	register(routeInfo{Method: "DELETE", Path: base + "/{id}", Resource: name, Op: "destroy"})
}

// APIResource is an alias for Resource — same behavior, Laravel-flavored name.
func APIResource(r IRouter, name string, ctrl ResourceController) { Resource(r, name, ctrl) }
