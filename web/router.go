package web

import (
	"net/http"
	"strings"
)

// Handler — har bir route metodining imzosi.
//
// Laravel uslubida ikki qiymat qaytaradi:
//   - any   — JSON sifatida javob beriladi (nil bo'lsa, 204 No Content)
//   - error — nil emas bo'lsa, avtomat xato javobi (orm.ErrNotFound → 404,
//     qolganlari → 500). c.Status(N) chaqirib ham qo'shimcha
//     holat kodini majburlash mumkin.
type Handler func(c *Context) (any, error)

// Middleware — Handler'ni boshqa Handler bilan o'rab oladigan funksiya.
// Birinchi ro'yxatga olingan middleware tashqaridagisi (so'rov birinchi
// uning ichidan o'tadi).
type Middleware func(next Handler) Handler

// ResourceController — Resource() bilan ishlatish uchun interfeys.
// 5 ta RESTful metod (any, error) qaytaradi — Laravel uslubida.
type ResourceController interface {
	Index(c *Context) (any, error)
	Show(c *Context) (any, error)
	Store(c *Context) (any, error)
	Update(c *Context) (any, error)
	Destroy(c *Context) (any, error)
}

// Route — bitta ro'yxatdan o'tgan marshrut.
type Route struct {
	Method  string
	Path    string
	Handler Handler
}

// Router — marshrut, group va middleware'larni e'lon qilish vositasi.
type Router struct {
	prefix      string
	middlewares []Middleware
	routes      []Route
}

// newRouter — App'ning ichki ishlatuvi.
func newRouter() *Router { return &Router{} }

// Use — har bir keyingi marshrutga qo'llaniladigan middleware.
func (r *Router) Use(mw ...Middleware) { r.middlewares = append(r.middlewares, mw...) }

// Group — yangi prefiks va o'rta dasturlar to'plamiga ega bola router.
// Misol: app.Group("/api/v1", func(g *web.Router) { g.Resource("posts", ...) })
func (r *Router) Group(prefix string, fn func(g *Router)) {
	g := &Router{
		prefix:      r.joinPath(prefix),
		middlewares: append([]Middleware(nil), r.middlewares...),
	}
	fn(g)
	r.routes = append(r.routes, g.routes...)
}

// HTTP verb shortcutlar — har biri prefiks va middleware bilan to'liq route ro'yxatga oladi.
func (r *Router) Get(path string, h Handler)     { r.add(http.MethodGet, path, h) }
func (r *Router) Post(path string, h Handler)    { r.add(http.MethodPost, path, h) }
func (r *Router) Put(path string, h Handler)     { r.add(http.MethodPut, path, h) }
func (r *Router) Patch(path string, h Handler)   { r.add(http.MethodPatch, path, h) }
func (r *Router) Delete(path string, h Handler)  { r.add(http.MethodDelete, path, h) }
func (r *Router) Options(path string, h Handler) { r.add(http.MethodOptions, path, h) }
func (r *Router) Head(path string, h Handler)    { r.add(http.MethodHead, path, h) }

// Any — barcha standart HTTP fe'llari uchun bir xil handler.
func (r *Router) Any(path string, h Handler) {
	for _, m := range []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions,
	} {
		r.add(m, path, h)
	}
}

// Resource — RESTful 5 ta marshrutni avtomat e'lon qiladi.
//
//	GET    /posts            → Index
//	GET    /posts/{id}       → Show
//	POST   /posts            → Store
//	PUT    /posts/{id}       → Update
//	PATCH  /posts/{id}       → Update
//	DELETE /posts/{id}       → Destroy
//
// `name` — ko'plik (`posts`, `users`).
func (r *Router) Resource(name string, ctrl ResourceController) {
	base := "/" + strings.Trim(name, "/")
	r.Get(base, ctrl.Index)
	r.Get(base+"/{id}", ctrl.Show)
	r.Post(base, ctrl.Store)
	r.Put(base+"/{id}", ctrl.Update)
	r.Patch(base+"/{id}", ctrl.Update)
	r.Delete(base+"/{id}", ctrl.Destroy)
}

// APIResource — Resource bilan bir xil; nom Laravel konventsiyasi uchun.
func (r *Router) APIResource(name string, ctrl ResourceController) { r.Resource(name, ctrl) }

// Routes — barcha ro'yxatga olingan marshrutlarning snapshot'i.
func (r *Router) Routes() []Route {
	out := make([]Route, len(r.routes))
	copy(out, r.routes)
	return out
}

func (r *Router) add(method, path string, h Handler) {
	full := r.joinPath(path)
	// respond() must run as the INNERMOST layer — before middleware
	// unwinds — so access loggers (Logger) observe the real status code
	// and byte count for the `return value, nil` handler pattern. If
	// respond ran outside the chain (after the outermost middleware
	// returned), Logger would always log status=200 size=0.
	wrapped := withRespond(h)
	// Middleware'larni teskari tartibda o'raymiz — birinchi ro'yxatga
	// olingan eng tashqi qatlam bo'ladi.
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		wrapped = r.middlewares[i](wrapped)
	}
	r.routes = append(r.routes, Route{Method: method, Path: full, Handler: wrapped})
}

// withRespond wraps a leaf handler so its (value, err) result is written
// to the response immediately, then returns (nil, nil) up the middleware
// chain. This makes respond() the innermost layer so middleware wrapping
// it (e.g. Logger) sees the committed status/bytes.
func withRespond(h Handler) Handler {
	return func(c *Context) (any, error) {
		value, err := h(c)
		c.respond(value, err)
		return nil, nil
	}
}

func (r *Router) joinPath(p string) string {
	if p == "" || p == "/" {
		// A root path on a prefix-less router would otherwise yield "",
		// which net/http's ServeMux rejects ("host/path missing /") and
		// panics on registration. Normalise to "/" so Get("/") on the
		// top-level router mounts cleanly.
		if r.prefix == "" {
			return "/"
		}
		return r.prefix
	}
	if r.prefix == "" {
		return "/" + strings.TrimLeft(p, "/")
	}
	return strings.TrimRight(r.prefix, "/") + "/" + strings.TrimLeft(p, "/")
}
