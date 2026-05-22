package migrations

import (
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/schema"
)

func init() {
	migrations.Register(migrations.Define("2026_01_01_000003_create_comments_table",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("comments", func(t *schema.Blueprint) {
				t.ID()
				t.ForeignId("post_id").Constrained("posts").OnDelete("cascade")
				t.ForeignId("user_id").Constrained("users").OnDelete("cascade")
				t.Text("body")
				t.Timestamps()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("comments"))
		},
	))
}
