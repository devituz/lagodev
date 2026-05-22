package migrations

import (
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/schema"
)

func init() {
	migrations.Register(migrations.Define("2026_01_01_000002_create_posts_table",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("posts", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("user_id").Constrained("users").OnDelete("cascade")
				t.String("title")
				t.String("slug").Unique()
				t.Text("body")
				t.Integer("views").Default(0)
				t.Boolean("pinned").Default(false)
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("posts"))
		},
	))
}
