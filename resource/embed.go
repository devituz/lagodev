package resource

import "reflect"

// Embed renders a single related model through its own resource, returning the
// transformed value for nesting under a relation key. A nil related pointer
// yields nil, so the JSON renders as null rather than panicking.
//
//	resource.Fields{
//	    "id":      post.ID,
//	    "title":   post.Title,
//	    "author":  resource.Embed(post.Author, UserResource),
//	}
func Embed[Model any](m Model, r Resource[Model]) any {
	if isNilModel(m) {
		return nil
	}
	return transform(m, r)
}

// EmbedMany renders a slice of related models through their resource, returning
// a []any suitable for nesting under a relation key (e.g. a User embedding its
// Posts). A nil slice yields an empty, non-nil slice so the JSON is [] not null.
//
//	resource.Fields{
//	    "id":    user.ID,
//	    "posts": resource.EmbedMany(user.Posts, PostResource),
//	}
func EmbedMany[Model any](models []Model, r Resource[Model]) []any {
	out := make([]any, 0, len(models))
	for i := range models {
		out = append(out, transform(models[i], r))
	}
	return out
}

// isNilModel reports whether m is a nil pointer/interface/slice/map. Non-nilable
// kinds (structs, ints, ...) are never nil.
func isNilModel[Model any](m Model) bool {
	v := reflect.ValueOf(m)
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

// isEmpty reports whether v is a Go zero value, used by Fields.OmitEmpty.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	case reflect.Slice, reflect.Map, reflect.Array:
		return rv.Len() == 0
	default:
		return rv.IsZero()
	}
}
