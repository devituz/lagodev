// Command artisan is the default lagodev Artisan-style CLI binary.
//
// Build it with:
//
//	go build -o artisan ./cmd/artisan
//
// Auto-bootstrap: when invoked from a directory that contains
// ./cmd/lago/main.go or ./cmd/artisan/main.go (the project-local CLI), the global binary
// transparently re-executes that local binary via `go run`. This is how
// project-specific migrations and seeders — registered through init() in
// the project's own packages — become visible without users having to
// remember a separate command. Set LAGO_BOOTSTRAPPED=1 in the environment
// to disable the indirection (also set automatically inside the child
// process to prevent infinite recursion).
package main

import (
	"github.com/devituz/lagodev/cli"

	// Blank-import drivers so DB_CONNECTION=sqlite|postgres|mysql Just Works.
	_ "github.com/devituz/lagodev/drivers/mysql"
	_ "github.com/devituz/lagodev/drivers/postgres"
	_ "github.com/devituz/lagodev/drivers/sqlite"
)

func main() {
	// Re-run through the project-local cmd/lago or cmd/artisan entrypoint
	// when present (see cli.RunProjectBinary).
	if cli.RunProjectBinary() {
		return
	}
	cli.Default().Execute()
}
