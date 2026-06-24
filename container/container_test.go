package container

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// --- local test types (no framework coupling) ---

type Config struct {
	DSN string
}

// Connection stands in for a *database.Connection without importing it.
type Connection struct {
	cfg Config
	id  int64
}

type UserRepo struct {
	conn *Connection
}

type Logger interface {
	Log(string)
}

type stdLogger struct{ prefix string }

func (l *stdLogger) Log(string) {}

func TestBind_Transient_RunsFactoryEveryTime(t *testing.T) {
	c := New()
	var calls int64
	Bind(c, func(_ *Container) (*Connection, error) {
		n := atomic.AddInt64(&calls, 1)
		return &Connection{id: n}, nil
	})

	a, err := Make[*Connection](c)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	b, err := Make[*Connection](c)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	if a == b {
		t.Fatalf("transient returned the same pointer")
	}
	if calls != 2 {
		t.Fatalf("factory ran %d times, want 2", calls)
	}
}

func TestSingleton_CachesAndSharesInstance(t *testing.T) {
	c := New()
	var calls int64
	Singleton(c, func(_ *Container) (*Connection, error) {
		atomic.AddInt64(&calls, 1)
		return &Connection{id: 1}, nil
	})

	a := MustMake[*Connection](c)
	b := MustMake[*Connection](c)
	if a != b {
		t.Fatalf("singleton returned different instances")
	}
	if calls != 1 {
		t.Fatalf("factory ran %d times, want 1", calls)
	}
}

func TestInstance_ReturnedAsIs(t *testing.T) {
	c := New()
	want := Config{DSN: "x"}
	Instance(c, want)

	got, err := Make[Config](c)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMake_Unbound_ReturnsErrNotBound(t *testing.T) {
	c := New()
	_, err := Make[*Connection](c)
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("got %v, want ErrNotBound", err)
	}
}

func TestMake_FactoryError_Propagates(t *testing.T) {
	c := New()
	sentinel := errors.New("boom")
	Bind(c, func(_ *Container) (*Connection, error) {
		return nil, sentinel
	})
	_, err := Make[*Connection](c)
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want sentinel", err)
	}
}

func TestFactoryDependencyInjection(t *testing.T) {
	c := New()
	Instance(c, Config{DSN: "postgres://x"})
	Singleton(c, func(c *Container) (*Connection, error) {
		cfg, err := Make[Config](c)
		if err != nil {
			return nil, err
		}
		return &Connection{cfg: cfg}, nil
	})
	Bind(c, func(c *Container) (*UserRepo, error) {
		conn, err := Make[*Connection](c)
		if err != nil {
			return nil, err
		}
		return &UserRepo{conn: conn}, nil
	})

	repo := MustMake[*UserRepo](c)
	if repo.conn.cfg.DSN != "postgres://x" {
		t.Fatalf("DSN not injected, got %q", repo.conn.cfg.DSN)
	}
}

func TestInterfaceBinding(t *testing.T) {
	c := New()
	Bind(c, func(_ *Container) (Logger, error) {
		return &stdLogger{prefix: "app"}, nil
	})
	l, err := Make[Logger](c)
	if err != nil {
		t.Fatalf("Make: %v", err)
	}
	if _, ok := l.(*stdLogger); !ok {
		t.Fatalf("got %T, want *stdLogger", l)
	}
}

func TestNamedBindings(t *testing.T) {
	c := New()
	BindNamed(c, "primary", func(_ *Container) (*Connection, error) {
		return &Connection{id: 1}, nil
	})
	BindNamed(c, "replica", func(_ *Container) (*Connection, error) {
		return &Connection{id: 2}, nil
	})

	p, err := MakeNamed[*Connection](c, "primary")
	if err != nil {
		t.Fatalf("MakeNamed primary: %v", err)
	}
	r, err := MakeNamed[*Connection](c, "replica")
	if err != nil {
		t.Fatalf("MakeNamed replica: %v", err)
	}
	if p.id != 1 || r.id != 2 {
		t.Fatalf("named bindings crossed: primary=%d replica=%d", p.id, r.id)
	}
	// Unnamed lookup must miss.
	if _, err := Make[*Connection](c); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unnamed lookup should be unbound, got %v", err)
	}
}

func TestScope_OverrideAndSingletonScoping(t *testing.T) {
	c := New()
	Singleton(c, func(_ *Container) (Config, error) {
		return Config{DSN: "parent"}, nil
	})

	// Parent singleton resolved from a child returns the parent's instance.
	parentVal := MustMake[Config](c)
	child := c.Scope()
	if got := MustMake[Config](child); got != parentVal {
		t.Fatalf("child saw %+v, want parent's %+v", got, parentVal)
	}

	// Override in the child: a per-scope singleton cached in the scope.
	var childCalls int64
	Singleton(child, func(_ *Container) (Config, error) {
		atomic.AddInt64(&childCalls, 1)
		return Config{DSN: "child"}, nil
	})
	a := MustMake[Config](child)
	b := MustMake[Config](child)
	if a.DSN != "child" || a != b {
		t.Fatalf("child override not cached: a=%+v b=%+v", a, b)
	}
	if childCalls != 1 {
		t.Fatalf("child factory ran %d times, want 1", childCalls)
	}
	// Parent is untouched by the child override.
	if got := MustMake[Config](c); got.DSN != "parent" {
		t.Fatalf("parent mutated by child: %+v", got)
	}

	// Two independent scopes get independent per-scope singletons.
	s1 := c.Scope()
	s2 := c.Scope()
	Singleton(s1, func(_ *Container) (*Connection, error) { return &Connection{id: 1}, nil })
	Singleton(s2, func(_ *Container) (*Connection, error) { return &Connection{id: 2}, nil })
	if MustMake[*Connection](s1) == MustMake[*Connection](s2) {
		t.Fatalf("scopes shared a per-scope singleton")
	}
}

func TestScope_ParentSingletonIsSharedAcrossScopes(t *testing.T) {
	c := New()
	Singleton(c, func(_ *Container) (*Connection, error) {
		return &Connection{id: 99}, nil
	})
	a := MustMake[*Connection](c.Scope())
	b := MustMake[*Connection](c.Scope())
	if a != b {
		t.Fatalf("parent singleton not shared across scopes")
	}
}

func TestCyclicDependency_Detected(t *testing.T) {
	c := New()
	// A depends on *Connection; *Connection depends back on A → cycle.
	type A struct{ conn *Connection }
	Bind(c, func(c *Container) (*A, error) {
		conn, err := Make[*Connection](c)
		if err != nil {
			return nil, err
		}
		return &A{conn: conn}, nil
	})
	Bind(c, func(c *Container) (*Connection, error) {
		if _, err := Make[*A](c); err != nil {
			return nil, err
		}
		return &Connection{}, nil
	})

	_, err := Make[*A](c)
	if err == nil {
		t.Fatalf("expected cyclic error, got nil")
	}
	var ce *cyclicError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v, want cyclicError", err)
	}
	if len(ce.chain) < 2 {
		t.Fatalf("cycle chain too short: %v", ce.chain)
	}
}

func TestSelfCycle_Detected(t *testing.T) {
	c := New()
	Bind(c, func(c *Container) (*Connection, error) {
		return Make[*Connection](c) // resolves itself
	})
	_, err := Make[*Connection](c)
	var ce *cyclicError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v, want cyclicError", err)
	}
}

func TestMustMake_PanicsOnError(t *testing.T) {
	c := New()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("MustMake did not panic on unbound type")
		}
	}()
	_ = MustMake[*Connection](c)
}

// --- autowiring ---

type Service struct {
	Conn   *Connection // autowired by type
	Cfg    Config      // autowired by type
	hidden int         // unexported, never touched
}

type TaggedService struct {
	Primary *Connection `inject:"primary"`
	Skipped *Connection `inject:"-"`
}

type RequiresMissing struct {
	Need *Connection `inject:""` // explicitly required, but unbound
}

type OptionalMissing struct {
	Maybe *Connection // unbound, optional → left nil
}

func TestBuild_AutowiresExportedFields(t *testing.T) {
	c := New()
	Instance(c, Config{DSN: "wired"})
	Singleton(c, func(_ *Container) (*Connection, error) {
		return &Connection{id: 7}, nil
	})

	svc, err := Build[*Service](c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if svc.Conn == nil || svc.Conn.id != 7 {
		t.Fatalf("Conn not autowired: %+v", svc.Conn)
	}
	if svc.Cfg.DSN != "wired" {
		t.Fatalf("Cfg not autowired: %+v", svc.Cfg)
	}
	if svc.hidden != 0 {
		t.Fatalf("unexported field was touched")
	}
}

func TestBuild_ValueStructTarget(t *testing.T) {
	c := New()
	Instance(c, Config{DSN: "v"})
	Singleton(c, func(_ *Container) (*Connection, error) { return &Connection{id: 1}, nil })

	svc, err := Build[Service](c) // value target, not pointer
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if svc.Cfg.DSN != "v" {
		t.Fatalf("value-target autowire failed: %+v", svc)
	}
}

func TestBuild_NamedAndSkipTags(t *testing.T) {
	c := New()
	BindNamed(c, "primary", func(_ *Container) (*Connection, error) {
		return &Connection{id: 1}, nil
	})
	svc, err := Build[*TaggedService](c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if svc.Primary == nil || svc.Primary.id != 1 {
		t.Fatalf("named inject failed: %+v", svc.Primary)
	}
	if svc.Skipped != nil {
		t.Fatalf("inject:\"-\" field should be left nil")
	}
}

func TestBuild_RequiredMissing_Errors(t *testing.T) {
	c := New()
	_, err := Build[*RequiresMissing](c)
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("got %v, want ErrNotBound", err)
	}
}

func TestBuild_OptionalMissing_LeftZero(t *testing.T) {
	c := New()
	svc, err := Build[*OptionalMissing](c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if svc.Maybe != nil {
		t.Fatalf("optional unbound field should be nil, got %+v", svc.Maybe)
	}
}

func TestBuild_NonStruct_Errors(t *testing.T) {
	c := New()
	if _, err := Build[int](c); err == nil {
		t.Fatalf("Build[int] should error")
	}
	if _, err := Build[*int](c); err == nil {
		t.Fatalf("Build[*int] should error")
	}
}

// --- concurrency / -race ---

func TestSingleton_ConcurrentMake_ResolvesOnce(t *testing.T) {
	c := New()
	var calls int64
	Singleton(c, func(_ *Container) (*Connection, error) {
		atomic.AddInt64(&calls, 1)
		return &Connection{id: 1}, nil
	})

	const goroutines = 200
	var wg sync.WaitGroup
	results := make([]*Connection, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = MustMake[*Connection](c)
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("factory ran %d times under concurrency, want 1", got)
	}
	first := results[0]
	for i, r := range results {
		if r != first {
			t.Fatalf("goroutine %d got a different instance", i)
		}
	}
}

func TestConcurrentBindAndMake(t *testing.T) {
	c := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("c%d", n)
			BindNamed(c, name, func(_ *Container) (*Connection, error) {
				return &Connection{id: int64(n)}, nil
			})
			got, err := MakeNamed[*Connection](c, name)
			if err != nil {
				t.Errorf("MakeNamed %s: %v", name, err)
				return
			}
			if got.id != int64(n) {
				t.Errorf("name %s got id %d", name, got.id)
			}
		}(i)
	}
	wg.Wait()
}
