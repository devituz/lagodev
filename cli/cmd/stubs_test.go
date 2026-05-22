package cmd

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// Point the cmd package at the on-disk stubs directory so tests don't need
	// to import cli (which would create an import cycle).
	SetStubFS(os.DirFS("../stubs"))
}

func TestRenderStub_Model(t *testing.T) {
	out, err := renderStub("model", map[string]any{
		"Name":    "User",
		"Package": "models",
		"Table":   "users",
	})
	require.NoError(t, err)
	assert.Contains(t, out, "package models")
	assert.Contains(t, out, "type User struct")
	assert.Contains(t, out, "orm.Model")
	assert.Contains(t, out, `return "users"`)
}

func TestRenderStub_Migration(t *testing.T) {
	out, err := renderStub("migration", map[string]any{
		"Package": "migrations",
		"Name":    "20260101000000_create_users_table",
		"Table":   "users",
	})
	require.NoError(t, err)
	assert.Contains(t, out, "migrations.Register")
	assert.Contains(t, out, "20260101000000_create_users_table")
	assert.Contains(t, out, `schema.Create("users"`)
}

func TestInferTable(t *testing.T) {
	cases := map[string]string{
		"create_users_table":   "users",
		"drop_orders_table":    "orders",
		"add_email_to_users":   "users",
		"remove_role_from_admins": "admins",
		"modify_things":           "thingses",
		"shohruh":                 "shohruhs",
	}
	for in, want := range cases {
		assert.Equal(t, want, inferTable(in), "in=%s", in)
	}
}

func TestPkgFromOutDir(t *testing.T) {
	assert.Equal(t, "main", pkgFromOutDir("."))
	assert.Equal(t, "models", pkgFromOutDir("models"))
	assert.Equal(t, "my_seeders", pkgFromOutDir("MySeeders"))
}
