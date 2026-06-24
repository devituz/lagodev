package openapi

import (
	"net/http"
	"strings"

	"github.com/devituz/lagodev/web"
)

// PathParams extracts the parameter names from a route path, accepting both the
// OpenAPI/stdlib brace form ("/users/{id}") and the gin/colon form
// ("/users/:id"). It returns the names without delimiters, in order.
func PathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		switch {
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2:
			out = append(out, seg[1:len(seg)-1])
		case strings.HasPrefix(seg, ":") && len(seg) > 1:
			out = append(out, seg[1:])
		}
	}
	return out
}

// NormalizePath rewrites a route path into OpenAPI's brace form. Colon params
// ("/users/:id") become brace params ("/users/{id}"); brace paths pass through.
func NormalizePath(path string) string {
	if !strings.Contains(path, ":") {
		return path
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, ":") && len(s) > 1 {
			segs[i] = "{" + s[1:] + "}"
		}
	}
	return strings.Join(segs, "/")
}

// RouteInfo is one harvested route: its HTTP method and OpenAPI-normalised path
// plus the derived path-parameter names. It is the skeleton a caller enriches
// with request/response types via Spec.Operation.
type RouteInfo struct {
	Method     string
	Path       string
	PathParams []string
}

// Harvest reads the routes registered on a *web.App (or any value exposing
// Routes() []web.Route) and returns their method/path/params skeleton. web does
// not carry request/response type metadata, so Harvest fills only what the
// router knows; callers supply bodies and responses when declaring operations.
//
//	for _, r := range openapi.Harvest(app) {
//	    spec.Operation(r.Method, r.Path, openapi.OperationConfig{...})
//	}
func Harvest(app *web.App) []RouteInfo {
	return harvestRoutes(app.Routes())
}

// HarvestRouter is Harvest for a *web.Router (e.g. a sub-group) or anything else
// returning []web.Route.
func HarvestRouter(routes []web.Route) []RouteInfo {
	return harvestRoutes(routes)
}

func harvestRoutes(routes []web.Route) []RouteInfo {
	out := make([]RouteInfo, 0, len(routes))
	for _, r := range routes {
		path := NormalizePath(r.Path)
		out = append(out, RouteInfo{
			Method:     r.Method,
			Path:       path,
			PathParams: PathParams(path),
		})
	}
	return out
}

// FromApp builds a Spec pre-populated with a bare operation for every route on
// app: method, path and path parameters are filled, each with a single default
// 200 response. It is the fastest path to a serveable document; callers can
// then refine individual operations by calling Spec.Operation again (a repeat
// method+path replaces the prior operation).
func FromApp(app *web.App, title, version string) *Spec {
	spec := New(title, version)
	for _, r := range Harvest(app) {
		spec.Operation(r.Method, r.Path, OperationConfig{
			Summary: defaultSummary(r.Method, r.Path),
		})
	}
	return spec
}

// defaultSummary produces a readable summary for an auto-harvested route, e.g.
// "GET /users/{id}".
func defaultSummary(method, path string) string {
	m := method
	if m == "" {
		m = http.MethodGet
	}
	return strings.ToUpper(m) + " " + path
}
