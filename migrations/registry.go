package migrations

import (
	"fmt"
	"sort"
	"sync"
)

// Registry collects migrations and returns them in deterministic order.
// Applications normally have one global registry (Default) that migration
// files populate via Register() in their init().
type Registry struct {
	mu   sync.RWMutex
	mig  map[string]Migration
	seen []string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{mig: make(map[string]Migration)}
}

// Register adds m. Panics on duplicate name to fail fast at startup.
func (r *Registry) Register(m Migration) {
	if m == nil || m.Name() == "" {
		panic("migrations: nil migration or empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.mig[m.Name()]; dup {
		panic(fmt.Sprintf("migrations: duplicate name %q", m.Name()))
	}
	r.mig[m.Name()] = m
	r.seen = append(r.seen, m.Name())
}

// All returns migrations in lexical name order.
func (r *Registry) All() []Migration {
	r.mu.RLock()
	names := append([]string(nil), r.seen...)
	mig := r.mig
	r.mu.RUnlock()
	sort.Strings(names)
	out := make([]Migration, 0, len(names))
	for _, n := range names {
		out = append(out, mig[n])
	}
	return out
}

// Get returns a migration by name.
func (r *Registry) Get(name string) (Migration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.mig[name]
	return m, ok
}

// Names returns all registered names (sorted).
func (r *Registry) Names() []string {
	r.mu.RLock()
	names := append([]string(nil), r.seen...)
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

// Reset clears the registry. Intended for tests.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mig = make(map[string]Migration)
	r.seen = nil
}

// Default is the process-wide registry.
var Default = NewRegistry()

// Register is a package-level convenience that registers on Default.
func Register(m Migration) { Default.Register(m) }
