package models

import "github.com/devituz/lagodev/orm"

// User authors posts.
type User struct {
	orm.Model

	Name  string `column:"name" json:"name"`
	Email string `column:"email" json:"email"`
}

func (User) TableName() string { return "users" }
