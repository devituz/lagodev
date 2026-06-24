// Package app is lagodev's application bootstrap and module layer. It ties
// the standalone packages — the DI container, the web layer, config and
// migrations — into a single orchestrated lifecycle so a real program reads
//
//	app.New(opts...).
//	    Register(UsersModule, BillingModule).
//	    Run(ctx)
//
// instead of wiring every dependency by hand.
//
// The design follows Laravel's service providers and NestJS's modules,
// adapted to Go:
//
//   - A Provider contributes services to the container in two phases —
//     Register (bind only) then Boot (resolve and use). Boot runs after
//     every provider has registered, so a provider may safely depend on
//     bindings owned by any other provider.
//   - A Module groups related providers, routes, migrations and commands
//     under one name so a feature is wired with a single Register call.
//
// The App owns a *container.Container, the config-derived state, a *web.App
// for HTTP, a *migrations.Registry and the ordered provider set. It does not
// modify any of those packages; it composes them.
package app

import "github.com/devituz/lagodev/container"

// Provider contributes services to the application across two phases.
//
// Register binds services into the container. It must NOT resolve other
// providers' bindings — they may not exist yet. Keep Register pure: declare
// factories, no side effects that depend on the rest of the app.
//
// Boot runs after every provider has registered. Here a provider may resolve
// its dependencies, register routes on the web app, attach event hooks,
// schedule tasks, and so on. Implement Boot via the optional Booter
// interface; a provider that only binds services can skip it.
//
// Providers boot in registration order, which is deterministic.
type Provider interface {
	// Register binds this provider's services into the container.
	Register(c *container.Container) error
}

// Booter is the optional second phase of a Provider. A Provider that also
// implements Booter has its Boot method called once, after all providers
// have completed Register, in registration order.
type Booter interface {
	// Boot runs after every provider has registered. It may resolve
	// bindings owned by other providers.
	Boot(c *container.Container) error
}

// Named is an optional Provider/Module capability: a stable name used in
// boot-ordering errors and diagnostics. Providers that do not implement it
// are reported by their Go type.
type Named interface {
	// ProviderName returns a human-readable identifier.
	ProviderName() string
}

// RegisterFunc adapts a plain function into a register-only Provider. Use it
// for small providers that only bind a few services:
//
//	app.RegisterFunc(func(c *container.Container) error {
//	    container.Singleton(c, newClock)
//	    return nil
//	})
type RegisterFunc func(c *container.Container) error

// Register implements Provider.
func (f RegisterFunc) Register(c *container.Container) error { return f(c) }

// providerName returns the best available label for a provider: its declared
// ProviderName, otherwise its Go type.
func providerName(p Provider) string {
	if n, ok := p.(Named); ok {
		if name := n.ProviderName(); name != "" {
			return name
		}
	}
	return typeName(p)
}
