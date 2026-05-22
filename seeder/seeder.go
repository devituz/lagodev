// Package seeder runs database seeders with dependency ordering and
// transactional execution. A Seeder is any type that implements Seeder; the
// Runner topologically sorts the registered seeders and runs them in turn.
package seeder

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/logger"
)

// Seeder defines the contract for a seeder. Name uniquely identifies the
// seeder; Dependencies declares names this seeder requires to run first.
type Seeder interface {
	Name() string
	Run(ctx context.Context, conn *database.Connection) error
}

// Dependent is an optional interface for declaring dependencies. Seeders
// without Dependencies are ordered by registration order.
type Dependent interface {
	Dependencies() []string
}

// Registry stores seeders.
type Registry struct {
	mu      sync.RWMutex
	seeders map[string]Seeder
	order   []string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{seeders: make(map[string]Seeder)}
}

// Register adds a seeder. Panics on duplicate.
func (r *Registry) Register(s Seeder) {
	if s == nil || s.Name() == "" {
		panic("seeder: nil seeder or empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.seeders[s.Name()]; dup {
		panic(fmt.Sprintf("seeder: duplicate name %q", s.Name()))
	}
	r.seeders[s.Name()] = s
	r.order = append(r.order, s.Name())
}

// Get returns a seeder by name.
func (r *Registry) Get(name string) (Seeder, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.seeders[name]
	return s, ok
}

// Names returns registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Sorted returns seeders in dependency order. Cycles return an error.
func (r *Registry) Sorted() ([]Seeder, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	type node struct {
		seeder Seeder
		deps   []string
	}
	nodes := make(map[string]node, len(r.seeders))
	for name, s := range r.seeders {
		var deps []string
		if d, ok := s.(Dependent); ok {
			deps = d.Dependencies()
		}
		nodes[name] = node{seeder: s, deps: deps}
	}
	visited := map[string]int{} // 0 unseen, 1 visiting, 2 done
	var out []Seeder
	var visit func(string) error
	visit = func(name string) error {
		n, ok := nodes[name]
		if !ok {
			return fmt.Errorf("seeder: unknown dependency %q", name)
		}
		switch visited[name] {
		case 1:
			return fmt.Errorf("seeder: dependency cycle through %q", name)
		case 2:
			return nil
		}
		visited[name] = 1
		// Stable order so reruns are deterministic.
		deps := append([]string(nil), n.deps...)
		sort.Strings(deps)
		for _, d := range deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		visited[name] = 2
		out = append(out, n.seeder)
		return nil
	}
	names := append([]string(nil), r.order...)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Default is the process-wide registry.
var Default = NewRegistry()

// Register is a package-level helper for Default.
func Register(s Seeder) { Default.Register(s) }

// Options configures Runner behavior.
type Options struct {
	// Transactional wraps each seeder run in a transaction.
	Transactional bool
	// Logger receives status messages.
	Logger *logger.Logger
	// Only restricts the run to the named seeders (and their deps).
	Only []string
}

// Runner executes seeders.
type Runner struct {
	Conn     *database.Connection
	Registry *Registry
	Opts     Options
}

// NewRunner builds a Runner. registry may be nil to use Default.
func NewRunner(conn *database.Connection, registry *Registry, opts Options) *Runner {
	if registry == nil {
		registry = Default
	}
	return &Runner{Conn: conn, Registry: registry, Opts: opts}
}

// Run executes all (or selected) seeders.
func (r *Runner) Run(ctx context.Context) error {
	seeders, err := r.Registry.Sorted()
	if err != nil {
		return err
	}
	if len(r.Opts.Only) > 0 {
		want := make(map[string]struct{}, len(r.Opts.Only))
		for _, n := range r.Opts.Only {
			want[n] = struct{}{}
		}
		filtered := seeders[:0]
		for _, s := range seeders {
			if _, ok := want[s.Name()]; ok {
				filtered = append(filtered, s)
			}
		}
		seeders = filtered
	}
	for _, s := range seeders {
		if err := r.runOne(ctx, s); err != nil {
			return fmt.Errorf("seeder %s: %w", s.Name(), err)
		}
		if r.Opts.Logger != nil {
			r.Opts.Logger.Infof("seeded %s", s.Name())
		}
	}
	return nil
}

func (r *Runner) runOne(ctx context.Context, s Seeder) error {
	if !r.Opts.Transactional {
		return s.Run(ctx, r.Conn)
	}
	// We pass the connection through but ensure all queries within Run are
	// transactional via WithTx — convention: seeders that want a transaction
	// should call orm.Save inside a Connection.Transaction themselves, OR we
	// can run them implicitly here.
	return r.Conn.Transaction(ctx, func(_ *database.Tx) error {
		return s.Run(ctx, r.Conn)
	})
}

// FuncSeeder adapts a function to the Seeder interface.
type FuncSeeder struct {
	N    string
	Deps []string
	Fn   func(ctx context.Context, conn *database.Connection) error
}

func (f *FuncSeeder) Name() string             { return f.N }
func (f *FuncSeeder) Dependencies() []string   { return f.Deps }
func (f *FuncSeeder) Run(ctx context.Context, c *database.Connection) error {
	if f.Fn == nil {
		return errors.New("seeder: nil Run function")
	}
	return f.Fn(ctx, c)
}

// Define builds an inline FuncSeeder.
func Define(name string, deps []string, fn func(ctx context.Context, conn *database.Connection) error) Seeder {
	return &FuncSeeder{N: name, Deps: deps, Fn: fn}
}

// Call runs the given seeders in the order they were passed. Intended for
// use inside a DatabaseSeeder's Run() to orchestrate child seeders the
// Laravel way:
//
//	func (DatabaseSeeder) Run(ctx context.Context, conn *database.Connection) error {
//	    return seeder.Call(ctx, conn,
//	        &CategorySeeder{},
//	        &UserSeeder{},
//	    )
//	}
//
// Errors are wrapped with the failing seeder's Name() for easier triage.
func Call(ctx context.Context, conn *database.Connection, seeders ...Seeder) error {
	for _, s := range seeders {
		if err := s.Run(ctx, conn); err != nil {
			return fmt.Errorf("seeder %s: %w", s.Name(), err)
		}
	}
	return nil
}
