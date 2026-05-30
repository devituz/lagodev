package seeder_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/seeder"
)

type recorder struct {
	name string
	deps []string
	log  *[]string
}

func (r *recorder) Name() string           { return r.name }
func (r *recorder) Dependencies() []string { return r.deps }
func (r *recorder) Run(_ context.Context, _ *database.Connection) error {
	*r.log = append(*r.log, r.name)
	return nil
}

func TestSorted_RespectsDependencies(t *testing.T) {
	reg := seeder.NewRegistry()
	log := []string{}
	reg.Register(&recorder{name: "C", deps: []string{"B"}, log: &log})
	reg.Register(&recorder{name: "A", log: &log})
	reg.Register(&recorder{name: "B", deps: []string{"A"}, log: &log})

	sorted, err := reg.Sorted()
	require.NoError(t, err)
	names := []string{}
	for _, s := range sorted {
		names = append(names, s.Name())
	}
	// A must come before B, B before C.
	assert.Equal(t, "A", names[0])
	assert.Equal(t, "B", names[1])
	assert.Equal(t, "C", names[2])
}

func TestSorted_DetectsCycles(t *testing.T) {
	reg := seeder.NewRegistry()
	log := []string{}
	reg.Register(&recorder{name: "A", deps: []string{"B"}, log: &log})
	reg.Register(&recorder{name: "B", deps: []string{"A"}, log: &log})

	_, err := reg.Sorted()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestRunner_OnlyRestricts(t *testing.T) {
	reg := seeder.NewRegistry()
	log := []string{}
	reg.Register(&recorder{name: "A", log: &log})
	reg.Register(&recorder{name: "B", log: &log})
	reg.Register(&recorder{name: "C", log: &log})

	runner := seeder.NewRunner(nil, reg, seeder.Options{Only: []string{"B"}})
	require.NoError(t, runner.Run(context.Background()))
	assert.Equal(t, []string{"B"}, log)
}
