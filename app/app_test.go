package app_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devituz/lagodev/app"
	"github.com/devituz/lagodev/container"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/web"
)

// --- test fixtures -------------------------------------------------------

// greeter is a service bound by one provider and resolved by another in its
// boot phase, exercising cross-provider Boot-time resolution.
type greeter struct{ prefix string }

func (g greeter) greet(name string) string { return g.prefix + name }

// order records the sequence of lifecycle events so tests can assert on it.
type order struct{ events []string }

// greeterProvider binds *greeter in Register.
type greeterProvider struct {
	ord *order
}

func (p *greeterProvider) ProviderName() string { return "greeter" }

func (p *greeterProvider) Register(c *container.Container) error {
	p.ord.events = append(p.ord.events, "greeter.register")
	container.Singleton(c, func(*container.Container) (*greeter, error) {
		return &greeter{prefix: "hello "}, nil
	})
	return nil
}

func (p *greeterProvider) Boot(c *container.Container) error {
	p.ord.events = append(p.ord.events, "greeter.boot")
	return nil
}

// consumerProvider resolves *greeter during Boot — only valid because Boot
// runs after every provider has registered.
type consumerProvider struct {
	ord     *order
	greeted string
}

func (p *consumerProvider) ProviderName() string { return "consumer" }

func (p *consumerProvider) Register(c *container.Container) error {
	p.ord.events = append(p.ord.events, "consumer.register")
	return nil
}

func (p *consumerProvider) Boot(c *container.Container) error {
	p.ord.events = append(p.ord.events, "consumer.boot")
	g, err := container.Make[*greeter](c)
	if err != nil {
		return err
	}
	p.greeted = g.greet("world")
	return nil
}

// --- tests ---------------------------------------------------------------

func TestBootPhaseOrdering(t *testing.T) {
	ord := &order{}
	a := app.New().Register(
		&greeterProvider{ord: ord},
		&consumerProvider{ord: ord},
	)
	if err := a.Test(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// All Register events must precede all Boot events, and within a phase
	// order is registration order.
	want := []string{
		"greeter.register", "consumer.register",
		"greeter.boot", "consumer.boot",
	}
	if len(ord.events) != len(want) {
		t.Fatalf("events = %v, want %v", ord.events, want)
	}
	for i := range want {
		if ord.events[i] != want[i] {
			t.Fatalf("events = %v, want %v", ord.events, want)
		}
	}
}

func TestBootTimeCrossProviderResolve(t *testing.T) {
	ord := &order{}
	consumer := &consumerProvider{ord: ord}
	a := app.New().Register(&greeterProvider{ord: ord}, consumer)
	if err := a.Test(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if consumer.greeted != "hello world" {
		t.Fatalf("greeted = %q, want %q", consumer.greeted, "hello world")
	}
}

func TestBootIsIdempotent(t *testing.T) {
	ord := &order{}
	a := app.New().Register(&greeterProvider{ord: ord})
	if err := a.Boot(); err != nil {
		t.Fatalf("boot 1: %v", err)
	}
	if err := a.Boot(); err != nil {
		t.Fatalf("boot 2: %v", err)
	}
	// Register/Boot must each have run exactly once.
	want := []string{"greeter.register", "greeter.boot"}
	if len(ord.events) != len(want) {
		t.Fatalf("events = %v, want %v (boot ran twice?)", ord.events, want)
	}
	if !a.Booted() {
		t.Fatal("Booted() = false after Boot")
	}
}

func TestModuleRegistersReachableRoute(t *testing.T) {
	mod := app.NewModule("users",
		app.WithProviders(app.RegisterFunc(func(c *container.Container) error {
			container.Singleton(c, func(*container.Container) (*greeter, error) {
				return &greeter{prefix: "hi "}, nil
			})
			return nil
		})),
		app.WithRoutes(func(r *web.Router) {
			r.Get("/users/{id}", func(c *web.Context) (any, error) {
				return map[string]string{"id": c.Param("id")}, nil
			})
		}),
	)

	a := app.New().Register(mod)
	if err := a.Test(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	rec := a.Web().Test(httptest.NewRequest(http.MethodGet, "/users/42", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got == "" || !contains(got, `"42"`) {
		t.Fatalf("body = %q, want it to contain \"42\"", got)
	}
}

func TestModuleRegistersMigrations(t *testing.T) {
	reg := migrations.NewRegistry()
	mod := app.NewModule("billing",
		app.WithModuleMigrations(func(r *migrations.Registry) {
			r.Register(fakeMigration{name: "2024_create_invoices"})
		}),
	)
	a := app.New(app.WithMigrations(reg)).Register(mod)
	if err := a.Test(); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if names := reg.Names(); len(names) != 1 || names[0] != "2024_create_invoices" {
		t.Fatalf("registry names = %v, want [2024_create_invoices]", names)
	}
}

func TestMissingDependencySurfacesError(t *testing.T) {
	// consumerProvider resolves *greeter, but no provider binds it.
	a := app.New().Register(&consumerProvider{ord: &order{}})
	err := a.Test()
	if err == nil {
		t.Fatal("expected boot error for missing dependency, got nil")
	}
	if !errors.Is(err, container.ErrNotBound) {
		t.Fatalf("error = %v, want it to wrap ErrNotBound", err)
	}
	if !contains(err.Error(), "consumer") {
		t.Fatalf("error %q should name the failing provider", err.Error())
	}
}

func TestFailingProviderRegisterSurfacesError(t *testing.T) {
	sentinel := errors.New("boom")
	a := app.New().Register(app.RegisterFunc(func(*container.Container) error {
		return sentinel
	}))
	err := a.Test()
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap sentinel", err)
	}
}

func TestAccessorsReturnWiredSingletons(t *testing.T) {
	reg := migrations.NewRegistry()
	w := web.New()
	a := app.New(app.WithWebApp(w), app.WithMigrations(reg))

	if a.Container() == nil {
		t.Fatal("Container() = nil")
	}
	if a.Web() != w {
		t.Fatal("Web() did not return the supplied web app")
	}
	if a.Migrations() != reg {
		t.Fatal("Migrations() did not return the supplied registry")
	}

	// The framework primitives are resolvable from the container.
	if got, err := container.Make[*web.App](a.Container()); err != nil || got != w {
		t.Fatalf("Make[*web.App] = %v, %v; want the supplied web app", got, err)
	}
	if got, err := container.Make[*migrations.Registry](a.Container()); err != nil || got != reg {
		t.Fatalf("Make[*migrations.Registry] = %v, %v; want the supplied registry", got, err)
	}
}

func TestScopeInheritsAndIsolates(t *testing.T) {
	a := app.New().Register(app.RegisterFunc(func(c *container.Container) error {
		container.Singleton(c, func(*container.Container) (*greeter, error) {
			return &greeter{prefix: "app "}, nil
		})
		return nil
	}))
	if err := a.Test(); err != nil {
		t.Fatalf("boot: %v", err)
	}

	scope := a.Scope()
	// Inherits the app-wide singleton.
	g, err := container.Make[*greeter](scope)
	if err != nil {
		t.Fatalf("scope make: %v", err)
	}
	if g.prefix != "app " {
		t.Fatalf("prefix = %q, want %q", g.prefix, "app ")
	}

	// A per-request override caches inside the scope only.
	type reqID struct{ v int }
	container.Singleton(scope, func(*container.Container) (*reqID, error) {
		return &reqID{v: 7}, nil
	})
	if got := container.MustMake[*reqID](scope).v; got != 7 {
		t.Fatalf("scoped reqID = %d, want 7", got)
	}
	if _, err := container.Make[*reqID](a.Container()); !errors.Is(err, container.ErrNotBound) {
		t.Fatal("scope binding leaked into the app container")
	}
}

func TestRunHonoursCancelledContext(t *testing.T) {
	a := app.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run with cancelled ctx = %v, want context.Canceled", err)
	}
	if a.Booted() {
		t.Fatal("app booted despite cancelled context")
	}
}

// --- helpers -------------------------------------------------------------

type fakeMigration struct{ name string }

func (m fakeMigration) Name() string                   { return m.name }
func (m fakeMigration) Up(*migrations.Context) error   { return nil }
func (m fakeMigration) Down(*migrations.Context) error { return nil }

func contains(s, sub string) bool { return strings.Contains(s, sub) }
