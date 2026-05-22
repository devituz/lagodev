// Package models holds the domain types. Each file declares one model
// and (in this example) one TableName method so the inferred table name
// is explicit.
package models

import "github.com/devituz/lagodev/orm"

// Post is a published blog entry. It embeds orm.Model to inherit the
// canonical ID / CreatedAt / UpdatedAt columns.
type Post struct {
	orm.Model

	UserID  uint64 `column:"user_id" json:"user_id"`
	Title   string `column:"title" json:"title"`
	Slug    string `column:"slug" json:"slug"`
	Body    string `column:"body" json:"body"`
	Views   int    `column:"views" json:"views"`
	Pinned  bool   `column:"pinned" json:"pinned"`
}

func (Post) TableName() string { return "posts" }
