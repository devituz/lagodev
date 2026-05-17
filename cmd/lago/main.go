// Command lago is the alternative name for the lagodev CLI. It is identical
// to `artisan` — install whichever name you prefer:
//
//	go install github.com/devituz/lagodev/cmd/lago@latest    # → lago migrate
//	go install github.com/devituz/lagodev/cmd/artisan@latest # → artisan migrate
//
// Both binaries share the same command tree, drivers, and flags.
package main

import (
	"github.com/devituz/lagodev/cli"

	_ "github.com/devituz/lagodev/drivers/mysql"
	_ "github.com/devituz/lagodev/drivers/postgres"
	_ "github.com/devituz/lagodev/drivers/sqlite"
)

func main() {
	app := cli.New(cli.Options{ProjectName: "lago"})
	app.Execute()
}
