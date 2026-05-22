// Command artisan is the default lagodev Artisan-style CLI binary.
//
// Build it with:
//
//	go build -o artisan ./cmd/artisan
//
// Auto-bootstrap: when invoked from a directory that contains
// ./cmd/artisan/main.go (the project-local artisan), the global binary
// transparently re-executes that local binary via `go run`. This is how
// project-specific migrations and seeders — registered through init() in
// the project's own packages — become visible without users having to
// remember a separate command. Set LAGO_BOOTSTRAPPED=1 in the environment
// to disable the indirection (also set automatically inside the child
// process to prevent infinite recursion).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/devituz/lagodev/cli"

	// Blank-import drivers so DB_CONNECTION=sqlite|postgres|mysql Just Works.
	_ "github.com/devituz/lagodev/drivers/mysql"
	_ "github.com/devituz/lagodev/drivers/postgres"
	_ "github.com/devituz/lagodev/drivers/sqlite"
)

const bootstrapEnv = "LAGO_BOOTSTRAPPED"

func main() {
	if os.Getenv(bootstrapEnv) != "1" && bootstrapToProjectBinary() {
		return
	}
	cli.Default().Execute()
}

// bootstrapToProjectBinary re-execs the project-local artisan via `go run`
// when ./cmd/artisan/main.go exists in the current directory. That local
// binary blank-imports the project's migrations/seeders packages, so their
// init() functions populate the global registries before the CLI runs.
// Returns true when it handled execution (caller should not continue).
func bootstrapToProjectBinary() bool {
	if _, err := os.Stat(filepath.Join("cmd", "artisan", "main.go")); err != nil {
		return false
	}
	c := exec.Command("go", append([]string{"run", "./cmd/artisan"}, os.Args[1:]...)...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), bootstrapEnv+"=1")
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "lago: bootstrap failed:", err)
		os.Exit(1)
	}
	return true
}
