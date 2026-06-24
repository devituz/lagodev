package app

import (
	"context"
	"fmt"
	"reflect"

	"github.com/devituz/lagodev/container"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/web"
)

// App is the top-level application: it owns the DI container, the HTTP layer,
// the migration registry and the ordered set of providers/modules, and drives
// the two-phase boot lifecycle.
//
// Typical use:
//
//	a := app.New(app.WithAddr(":8080"))
//	a.Register(UsersModule, BillingModule)
//	if err := a.Run(context.Background()); err != nil {
//	    log.Fatal(err)
//	}
//
// App is not safe for concurrent Register/Boot; configure and boot it from a
// single goroutine, then serve. The container it owns is itself concurrency
// safe.
type App struct {
	container  *container.Container
	web        *web.App
	migrations *migrations.Registry

	webOpts []web.Option
	addr    string

	// entries holds providers and modules in registration order. Modules are
	// flattened lazily at boot so registration order is preserved exactly.
	entries []entry

	booted bool
}

// entry is one registered unit: either a bare provider or a module. Exactly
// one field is set.
type entry struct {
	provider Provider
	module   *Module
}

// Option configures an App at construction.
type Option func(*App)

// WithContainer supplies a pre-built container instead of a fresh one. Useful
// when the host has already bound process-wide services.
func WithContainer(c *container.Container) Option {
	return func(a *App) { a.container = c }
}

// WithMigrations supplies the migration registry the app exposes to modules.
// Defaults to migrations.Default.
func WithMigrations(reg *migrations.Registry) Option {
	return func(a *App) { a.migrations = reg }
}

// WithAddr sets the HTTP listen address passed to web.App.Run (default
// ":8080").
func WithAddr(addr string) Option {
	return func(a *App) { a.addr = addr }
}

// WithWebOptions forwards options to the underlying web.App constructed by
// New. Ignored if WithWebApp supplies a ready app.
func WithWebOptions(opts ...web.Option) Option {
	return func(a *App) { a.webOpts = append(a.webOpts, opts...) }
}

// WithWebApp supplies a pre-built *web.App instead of constructing one from
// WithWebOptions.
func WithWebApp(w *web.App) Option {
	return func(a *App) { a.web = w }
}

// New constructs an App. Defaults: a fresh container, a fresh web.App, and
// the process-wide migrations.Default registry. The application's own
// singletons (the container, web app and migration registry) are bound into
// the container so providers can resolve them.
func New(opts ...Option) *App {
	a := &App{addr: ":8080"}
	for _, o := range opts {
		o(a)
	}
	if a.container == nil {
		a.container = container.New()
	}
	if a.migrations == nil {
		a.migrations = migrations.Default
	}
	if a.web == nil {
		a.web = web.New(a.webOpts...)
	}
	// Make the framework primitives resolvable from any provider.
	container.Instance(a.container, a.web)
	container.Instance(a.container, a.migrations)
	return a
}

// Register adds providers and/or modules in order. It accepts any value that
// is a Provider (including *Module) so callers may mix both:
//
//	a.Register(UsersModule, app.RegisterFunc(bindClock), &MetricsProvider{})
//
// Registration only records order; nothing runs until Boot (or Run/Test).
// Register returns the App for chaining.
func (a *App) Register(providers ...Provider) *App {
	for _, p := range providers {
		if m, ok := p.(*Module); ok {
			a.entries = append(a.entries, entry{module: m})
			continue
		}
		a.entries = append(a.entries, entry{provider: p})
	}
	return a
}

// Boot runs the full lifecycle: every provider's Register phase (in
// registration order), then every provider's Boot phase, then each module's
// route and migration callbacks. It is idempotent — calling Boot twice is a
// no-op after the first success.
//
// Errors are wrapped with the offending provider/module name so failures are
// actionable.
func (a *App) Boot() error {
	if a.booted {
		return nil
	}

	// Flatten entries into the ordered provider list plus the per-module
	// boot hooks, preserving registration order. A module contributes its
	// providers first, then itself (its Register/Boot callbacks).
	type bootHook struct {
		name   string
		module *Module
	}
	var providers []Provider
	var hooks []bootHook

	for _, e := range a.entries {
		if e.module != nil {
			m := e.module
			providers = append(providers, m.Providers...)
			if m.RegisterFn != nil || m.BootFn != nil {
				providers = append(providers, moduleProvider{m})
			}
			hooks = append(hooks, bootHook{name: m.Name, module: m})
			continue
		}
		providers = append(providers, e.provider)
	}

	// Phase 1: Register all.
	for _, p := range providers {
		if err := p.Register(a.container); err != nil {
			return fmt.Errorf("app: register provider %q: %w", providerName(p), err)
		}
	}

	// Phase 2: Boot all (only providers that implement Booter).
	for _, p := range providers {
		b, ok := p.(Booter)
		if !ok {
			continue
		}
		if err := b.Boot(a.container); err != nil {
			return fmt.Errorf("app: boot provider %q: %w", providerName(p), err)
		}
	}

	// Phase 3: module route + migration wiring, in registration order.
	for _, h := range hooks {
		if h.module.Routes != nil {
			h.module.Routes(a.web.Router)
		}
		if h.module.Migrations != nil {
			h.module.Migrations(a.migrations)
		}
	}

	a.booted = true
	return nil
}

// Run boots the application and starts the HTTP server, delegating to
// web.App.Run so its graceful-shutdown semantics are preserved. It blocks
// until the server stops (signal or error).
//
// The provided context is honoured for the boot phase; cancelling it before
// boot completes aborts startup. The web server's own signal-based shutdown
// governs runtime termination.
func (a *App) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Boot(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.web.Run(a.addr)
}

// Test boots the application without binding a port, for use in tests. After
// Test returns, drive HTTP requests through App.Web().Test(req).
func (a *App) Test() error {
	return a.Boot()
}

// Container returns the application's DI container.
func (a *App) Container() *container.Container { return a.container }

// Web returns the underlying HTTP application.
func (a *App) Web() *web.App { return a.web }

// Migrations returns the application's migration registry.
func (a *App) Migrations() *migrations.Registry { return a.migrations }

// Booted reports whether Boot has completed successfully.
func (a *App) Booted() bool { return a.booted }

// Scope returns a request-scoped child container derived from the
// application container. Handlers (or middleware) call it once per request to
// obtain per-request singletons that inherit application-wide bindings:
//
//	rc := a.Scope()
//	defer ... // request lifetime
//	svc := container.MustMake[*RequestSvc](rc)
//
// Per-request scoping is exposed as this hook rather than wired into
// web.Context, because the web layer threads its own *web.Context and binding
// the scope into it would require editing web. Middleware that wants a scope
// per request can capture rc in a closure; see the package tests for the
// pattern.
func (a *App) Scope() *container.Container { return a.container.Scope() }

// Commands collects every CLI command contributed by registered modules, in
// registration order. The app layer does not dispatch commands; the host
// program (or the cli package) consumes this list.
func (a *App) Commands() []Command {
	var cmds []Command
	for _, e := range a.entries {
		if e.module != nil && e.module.Commands != nil {
			cmds = append(cmds, e.module.Commands()...)
		}
	}
	return cmds
}

// moduleProvider adapts a Module's own Register/Boot callbacks into a
// Provider so they participate in the same two-phase ordering as everything
// else, but after the module's own providers.
type moduleProvider struct{ m *Module }

func (mp moduleProvider) Register(c *container.Container) error {
	if mp.m.RegisterFn != nil {
		return mp.m.RegisterFn(c)
	}
	return nil
}

func (mp moduleProvider) Boot(c *container.Container) error {
	if mp.m.BootFn != nil {
		return mp.m.BootFn(c)
	}
	return nil
}

func (mp moduleProvider) ProviderName() string { return mp.m.Name }

// typeName renders a value's Go type for diagnostics, dereferencing one
// pointer level for readability (*UserProvider -> app.UserProvider style).
func typeName(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return "<nil>"
	}
	return t.String()
}
