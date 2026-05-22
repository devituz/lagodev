package lagogin

import (
	"sort"
	"sync"
)

// routeInfo is the lightweight record Resource() leaves in the global registry
// so OpenAPI() and PrintRoutes() can reconstruct the routing table without
// asking the gin engine to introspect itself.
type routeInfo struct {
	Method   string
	Path     string
	Resource string
	Op       string // index | show | store | update | destroy | custom
}

var (
	routesMu sync.Mutex
	routes   []routeInfo
)

func register(r routeInfo) {
	routesMu.Lock()
	defer routesMu.Unlock()
	routes = append(routes, r)
}

// RegisterRoute records a custom route in the lagogin registry so it shows
// up in OpenAPI() output. Call this when you wire a handler outside of
// Resource() and still want it to appear in the generated spec.
//
//	r.GET("/health", lagogin.H(health))
//	lagogin.RegisterRoute("GET", "/health", "health", "index")
func RegisterRoute(method, path, resource, op string) {
	register(routeInfo{Method: method, Path: path, Resource: resource, Op: op})
}

// Routes returns a sorted snapshot of every Resource()-registered route.
// Useful for debug output or test assertions.
func Routes() []RouteEntry {
	routesMu.Lock()
	defer routesMu.Unlock()
	out := make([]RouteEntry, len(routes))
	for i, r := range routes {
		out[i] = RouteEntry(r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// RouteEntry is the public view of an internal routeInfo.
type RouteEntry struct {
	Method   string
	Path     string
	Resource string
	Op       string
}

// ResetRoutes clears the route registry. Intended for tests that want a
// clean slate; production code should never call it.
func ResetRoutes() {
	routesMu.Lock()
	defer routesMu.Unlock()
	routes = nil
}
