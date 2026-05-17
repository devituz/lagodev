package models

import "github.com/devituz/lagodev/orm"

// Comment belongs to a Post and to a User.
type Comment struct {
	orm.Model

	PostID uint64 `column:"post_id" json:"post_id"`
	UserID uint64 `column:"user_id" json:"user_id"`
	Body   string `column:"body"    json:"body"`
}

func (Comment) TableName() string { return "comments" }
