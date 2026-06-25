// Package resource is a dependency-free API-resource / serializer layer for
// turning domain models into clean JSON representations — the JSON-API
// equivalent of Laravel's API Resources or Django REST Framework serializers.
//
// The problem it removes is hand-mapping: every endpoint that returns a model
// otherwise repeats the same "copy these fields, rename that one, hide the
// password, add a computed URL" boilerplate. A Resource describes that mapping
// once, and the Item / Collection / Paginated wrappers render it consistently.
//
// # Defining a resource
//
// A resource is anything that maps one Model to an output value (typically a
// Fields map). Implement the interface, or wrap a function with Func:
//
//	type User struct {
//	    ID        int
//	    Name      string
//	    Email     string
//	    Password  string // never serialised
//	    Admin     bool
//	    AvatarURL string
//	}
//
//	var UserResource = resource.Func(func(u User) any {
//	    return resource.Fields{
//	        "id":     u.ID,
//	        "name":   u.Name,
//	        "email":  u.Email,
//	        // computed field
//	        "avatar": u.AvatarURL,
//	    }.
//	        When(u.Admin, "is_admin", true) // conditional: only admins carry it
//	})
//
// # Rendering
//
//	resource.Item(user, UserResource)             // -> single object
//	resource.Collection(users, UserResource)      // -> {"data": [...]}
//	resource.Paginated(paginator, UserResource)   // -> {"data": [...], "meta": {...}}
//
// Each returns a plain value (map / struct) ready for an HTTP layer to encode,
// e.g. c.JSON(200, resource.Item(user, UserResource)). resource is a leaf
// package: it never imports web, so there is no import cycle.
//
// # Web integration
//
//	func (h *Handler) Show(c *web.Context) (any, error) {
//	    u, err := h.users.Find(c.Ctx(), id)
//	    if err != nil {
//	        return nil, err
//	    }
//	    return resource.Item(u, UserResource), nil
//	}
package resource

// Resource maps a single Model to its serialisable output. The output is
// usually a Fields map, but may be any value encoding/json can marshal
// (including a typed DTO struct).
type Resource[Model any] interface {
	Transform(Model) any
}

// Func adapts an ordinary func(Model) any into a Resource. It is the common way
// to define a resource inline without declaring a named type.
type Func[Model any] func(Model) any

// Transform implements Resource by calling the wrapped function.
func (f Func[Model]) Transform(m Model) any { return f(m) }

// transform is the single place that invokes a resource, so a nil resource
// degrades to passing the model through unchanged rather than panicking.
func transform[Model any](m Model, r Resource[Model]) any {
	if r == nil {
		return m
	}
	return r.Transform(m)
}
