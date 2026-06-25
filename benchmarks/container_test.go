package benchmarks

import (
	"testing"

	"github.com/devituz/lagodev/container"
)

type diConfig struct{ DSN string }
type diDB struct{ cfg diConfig }
type diRepo struct{ db *diDB }

// BenchmarkContainer_MakeSingleton measures resolving a cached singleton —
// the hot path for app-scoped services that are built once and shared.
func BenchmarkContainer_MakeSingleton(b *testing.B) {
	c := container.New()
	container.Instance(c, diConfig{DSN: "postgres://localhost/app"})
	container.Singleton(c, func(c *container.Container) (*diDB, error) {
		cfg, err := container.Make[diConfig](c)
		if err != nil {
			return nil, err
		}
		return &diDB{cfg: cfg}, nil
	})
	// Warm the singleton cache so we measure resolution, not construction.
	_ = container.MustMake[*diDB](c)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := container.Make[*diDB](c); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkContainer_MakeTransient measures resolving a transient (Bind)
// service whose factory runs on every Make — the per-request DI cost when a
// fresh instance is wanted each time.
func BenchmarkContainer_MakeTransient(b *testing.B) {
	c := container.New()
	container.Instance(c, diConfig{DSN: "postgres://localhost/app"})
	container.Singleton(c, func(c *container.Container) (*diDB, error) {
		cfg, err := container.Make[diConfig](c)
		if err != nil {
			return nil, err
		}
		return &diDB{cfg: cfg}, nil
	})
	container.Bind(c, func(c *container.Container) (*diRepo, error) {
		db, err := container.Make[*diDB](c)
		if err != nil {
			return nil, err
		}
		return &diRepo{db: db}, nil
	})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := container.Make[*diRepo](c); err != nil {
			b.Fatal(err)
		}
	}
}
