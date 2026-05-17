// Command artisan is the default lagodev Artisan-style CLI binary.
//
// Build it with:
//
//	go build -o artisan ./cmd/artisan
//
// In your own project, you can replace it with a tiny main that imports the
// driver you actually use and registers your custom commands:
//
//	package main
//
//	import (
//		_ "github.com/devituz/lagodev/drivers/sqlite"
//
//		"github.com/devituz/lagodev/cli"
//		_ "myapp/migrations" // side-effect: registers migrations
//		_ "myapp/seeders"    // side-effect: registers seeders
//	)
//
//	func main() { cli.Default().Execute() }
package main

import (
	"github.com/devituz/lagodev/cli"

	// Blank-import drivers so DB_CONNECTION=sqlite|postgres|mysql Just Works.
	_ "github.com/devituz/lagodev/drivers/mysql"
	_ "github.com/devituz/lagodev/drivers/postgres"
	_ "github.com/devituz/lagodev/drivers/sqlite"
)

func main() {
	cli.Default().Execute()
}
