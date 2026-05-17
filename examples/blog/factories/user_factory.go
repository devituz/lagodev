// Package factories defines fake-data generators for the blog models.
// Each factory is a normal Go function that returns a *factory.Factory[T].
package factories

import (
	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/factory"

	"github.com/devituz/lagodev/examples/blog/models"
)

func UserFactory(conn *database.Connection) *factory.Factory[models.User] {
	return factory.New(conn, func(f *factory.Faker) models.User {
		return models.User{
			Name:  f.Name(),
			Email: f.Email(),
		}
	})
}
