// Package migrations registers every migration on import. The blog/main.go
// blank-imports this package so registration happens at startup.
package migrations

import (
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/schema"
)

func init() {
	migrations.Register(migrations.Define("2026_01_01_000001_create_users_table",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("users", func(t *schema.Blueprint) {
				t.ID()
				t.String("name")
				t.String("email").Unique()
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("users"))
		},
	))
}
