// Package orm provides an ActiveRecord-style Model, a fluent query builder,
// hooks, casts, soft-deletes, and relationships. Models are plain Go structs
// that embed orm.Model (or one of the timestamp variants) and live without a
// global router — the only required dependency is a *database.Connection.
//
// Example:
//
//	type User struct {
//	    orm.Model
//	    Name  string
//	    Email string `orm:"unique"`
//	}
//
//	user := &User{Name: "Ada", Email: "ada@example.com"}
//	if err := orm.Save(ctx, conn, user); err != nil { ... }
//
//	var users []User
//	err := orm.Query[User](conn).Where("email", "like", "%@example.com").Get(ctx, &users)
package orm

import (
	"time"

	"database/sql"
)

// Model is the base struct most models embed. It provides the standard
// id/created_at/updated_at/deleted_at columns. Models that do not need soft
// deletes can embed Timestamps instead.
type Model struct {
	ID        uint64       `column:"id" orm:"primary;autoincrement"`
	CreatedAt time.Time    `column:"created_at"`
	UpdatedAt time.Time    `column:"updated_at"`
	DeletedAt sql.NullTime `column:"deleted_at" orm:"nullable"`
}

// Timestamps is a lighter base for models that do not need soft deletes.
type Timestamps struct {
	ID        uint64    `column:"id" orm:"primary;autoincrement"`
	CreatedAt time.Time `column:"created_at"`
	UpdatedAt time.Time `column:"updated_at"`
}

// Tabler may be implemented on a model to override the inferred table name.
type Tabler interface {
	TableName() string
}

// Connectioner may be implemented on a model to pin it to a specific named
// connection from the database.Manager. Useful in multi-tenant or
// multi-database setups.
type Connectioner interface {
	ConnectionName() string
}

// Fillable may be implemented to whitelist columns for mass assignment.
type Fillable interface {
	FillableColumns() []string
}

// Hidden may be implemented to hide columns from ToMap serialization.
type Hidden interface {
	HiddenColumns() []string
}

// BeforeCreate / AfterCreate / etc. are optional hook interfaces.
type (
	BeforeCreateHook interface{ BeforeCreate(*HookContext) error }
	AfterCreateHook  interface{ AfterCreate(*HookContext) error }
	BeforeUpdateHook interface{ BeforeUpdate(*HookContext) error }
	AfterUpdateHook  interface{ AfterUpdate(*HookContext) error }
	BeforeSaveHook   interface{ BeforeSave(*HookContext) error }
	AfterSaveHook    interface{ AfterSave(*HookContext) error }
	BeforeDeleteHook interface{ BeforeDelete(*HookContext) error }
	AfterDeleteHook  interface{ AfterDelete(*HookContext) error }
	AfterFindHook    interface{ AfterFind(*HookContext) error }
)
