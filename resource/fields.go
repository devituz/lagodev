package resource

// Fields is the working output of a resource: an ordered-by-key, JSON-object
// mapping of response keys to values. encoding/json marshals it like any
// map[string]any (keys sorted), so output is deterministic.
//
// The methods are chainable and return Fields so a resource body can read as a
// single expression. Every method is nil-safe: calling on a nil Fields lazily
// allocates, so the zero value is usable.
type Fields map[string]any

// ensure returns f, allocating a fresh map if f is nil.
func (f Fields) ensure() Fields {
	if f == nil {
		return Fields{}
	}
	return f
}

// Set adds or overwrites key with val and returns the (possibly newly
// allocated) Fields for chaining.
func (f Fields) Set(key string, val any) Fields {
	f = f.ensure()
	f[key] = val
	return f
}

// When sets key to val only when cond is true. It is the conditional-field
// helper: omit a field entirely (not null) unless a predicate holds.
//
//	Fields{...}.When(user.IsAdmin, "secret_notes", user.Notes)
func (f Fields) When(cond bool, key string, val any) Fields {
	if !cond {
		return f.ensure()
	}
	return f.Set(key, val)
}

// WhenFunc is like When but defers the value to a function, so an expensive or
// possibly-panicking computation only runs when cond is true.
func (f Fields) WhenFunc(cond bool, key string, val func() any) Fields {
	if !cond {
		return f.ensure()
	}
	return f.Set(key, val())
}

// Unless is the inverse of When: it sets key to val only when cond is false.
func (f Fields) Unless(cond bool, key string, val any) Fields {
	return f.When(!cond, key, val)
}

// Merge copies all keys from other into f (other wins on collisions). Useful
// for splicing in a shared block of fields.
func (f Fields) Merge(other Fields) Fields {
	f = f.ensure()
	for k, v := range other {
		f[k] = v
	}
	return f
}

// Only returns a new Fields containing just the listed keys (a whitelist).
// Missing keys are skipped. The receiver is not modified.
func (f Fields) Only(keys ...string) Fields {
	out := make(Fields, len(keys))
	for _, k := range keys {
		if v, ok := f[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Except returns a new Fields with the listed keys removed (a blacklist). The
// receiver is not modified.
func (f Fields) Except(keys ...string) Fields {
	drop := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := make(Fields, len(f))
	for k, v := range f {
		if _, skip := drop[k]; !skip {
			out[k] = v
		}
	}
	return out
}

// Rename moves the value at from to to, dropping the old key. It is a no-op if
// from is absent or from == to.
func (f Fields) Rename(from, to string) Fields {
	f = f.ensure()
	if from == to {
		return f
	}
	if v, ok := f[from]; ok {
		f[to] = v
		delete(f, from)
	}
	return f
}

// OmitEmpty deletes keys whose value is a Go zero value (nil, "", 0, false,
// empty slice/map). Use it to drop optional fields instead of rendering them as
// null/zero — the manual, opt-in equivalent of the json `,omitempty` tag.
func (f Fields) OmitEmpty(keys ...string) Fields {
	f = f.ensure()
	check := keys
	if len(check) == 0 {
		check = make([]string, 0, len(f))
		for k := range f {
			check = append(check, k)
		}
	}
	for _, k := range check {
		if isEmpty(f[k]) {
			delete(f, k)
		}
	}
	return f
}
