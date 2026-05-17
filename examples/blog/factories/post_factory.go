package factories

import (
	"strings"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/factory"

	"github.com/devituz/lagodev/examples/blog/models"
)

// PostFactory builds posts for a specific author. Note how state-style
// closures let callers pin fields without overloading the definition.
func PostFactory(conn *database.Connection, authorID uint64) *factory.Factory[models.Post] {
	return factory.New(conn, func(f *factory.Faker) models.Post {
		title := f.Sentence(5)
		slug := strings.ToLower(strings.ReplaceAll(strings.TrimRight(title, "."), " ", "-"))
		return models.Post{
			UserID: authorID,
			Title:  title,
			Slug:   slug,
			Body:   f.Paragraph(3, 4, 5, " "),
			Views:  f.IntRange(0, 5000),
			Pinned: false,
		}
	})
}
