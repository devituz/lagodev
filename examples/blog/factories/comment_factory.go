package factories

import (
	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/factory"

	"github.com/devituz/lagodev/examples/blog/models"
)

func CommentFactory(conn *database.Connection, postID, userID uint64) *factory.Factory[models.Comment] {
	return factory.New(conn, func(f *factory.Faker) models.Comment {
		return models.Comment{
			PostID: postID,
			UserID: userID,
			Body:   f.Paragraph(1, 2, 6, " "),
		}
	})
}
