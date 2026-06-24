package app

import (
	"github.com/devituz/lagodev/container"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/web"
)

// Module groups everything a feature needs — providers, routes, migrations
// and CLI commands — under a single name, so an entire feature is wired with
// one app.Register call.
//
// A Module is a Provider itself: registering a module registers all of its
// providers (in slice order), then registers the module's own
// Register/Boot callbacks last so they observe the providers' bindings.
// During the application boot phase the module's route and migration
// callbacks run, giving the feature access to the web app and migration
// registry.
//
// Construct a Module with NewModule and the With* options, or assemble the
// struct literal directly:
//
//	var UsersModule = app.NewModule("users",
//	    app.WithProviders(&UserServiceProvider{}),
//	    app.WithRoutes(func(r *web.Router) {
//	        r.Get("/users", listUsers)
//	    }),
//	    app.WithModuleMigrations(func(reg *migrations.Registry) {
//	        reg.Register(createUsersTable{})
//	    }),
//	)
type Module struct {
	// Name identifies the module in diagnostics and ordering errors.
	Name string

	// Providers are registered (and booted) in slice order before the
	// module's own Register/Boot callbacks.
	Providers []Provider

	// Register, if non-nil, runs as a register-phase callback after the
	// module's providers have registered. Bind module-level services here.
	RegisterFn func(c *container.Container) error

	// Boot, if non-nil, runs in the boot phase after the module's providers
	// have booted. Resolve services and perform module-level wiring here.
	BootFn func(c *container.Container) error

	// Routes, if non-nil, runs in the boot phase against the application's
	// web router. Declare the feature's HTTP routes here.
	Routes func(r *web.Router)

	// Migrations, if non-nil, runs in the boot phase against the
	// application's migration registry. Register the feature's migrations
	// here.
	Migrations func(reg *migrations.Registry)

	// Commands, if non-nil, returns the feature's CLI commands. The slice is
	// collected by the app and exposed via App.Commands(); the app layer
	// does not run a CLI itself, leaving dispatch to the host program.
	Commands func() []Command
}

// Command is an opaque CLI command contributed by a module. The app layer
// only collects commands; the host program (or the cli package) dispatches
// them. Kept as an interface so app does not depend on a concrete CLI
// package.
type Command interface {
	// Name is the command's invocation name, e.g. "users:prune".
	Name() string
}

// ProviderName implements Named.
func (m *Module) ProviderName() string { return m.Name }

// Register makes *Module satisfy Provider so it can be passed to
// App.Register alongside bare providers. It is a no-op: the app flattens a
// module into its providers and boot hooks at Boot time (see App.Boot), so a
// module is never registered through this method directly.
func (m *Module) Register(*container.Container) error { return nil }

// ModuleOption configures a Module built with NewModule.
type ModuleOption func(*Module)

// NewModule builds a named Module from options.
func NewModule(name string, opts ...ModuleOption) *Module {
	m := &Module{Name: name}
	for _, o := range opts {
		o(m)
	}
	return m
}

// WithProviders appends providers to the module.
func WithProviders(p ...Provider) ModuleOption {
	return func(m *Module) { m.Providers = append(m.Providers, p...) }
}

// WithModuleRegister sets the module's register-phase callback.
func WithModuleRegister(fn func(c *container.Container) error) ModuleOption {
	return func(m *Module) { m.RegisterFn = fn }
}

// WithModuleBoot sets the module's boot-phase callback.
func WithModuleBoot(fn func(c *container.Container) error) ModuleOption {
	return func(m *Module) { m.BootFn = fn }
}

// WithRoutes sets the module's route registration callback, run in the boot
// phase against the application's web router.
func WithRoutes(fn func(r *web.Router)) ModuleOption {
	return func(m *Module) { m.Routes = fn }
}

// WithModuleMigrations sets the module's migration registration callback, run
// in the boot phase against the application's migration registry.
func WithModuleMigrations(fn func(reg *migrations.Registry)) ModuleOption {
	return func(m *Module) { m.Migrations = fn }
}

// WithCommands sets the module's CLI command provider.
func WithCommands(fn func() []Command) ModuleOption {
	return func(m *Module) { m.Commands = fn }
}
