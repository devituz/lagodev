package container_test

import (
	"fmt"

	"github.com/devituz/lagodev/container"
)

// dbConfig and dbConn stand in for real config/database types to keep the
// example free of cross-package coupling.
type dbConfig struct{ DSN string }

type dbConn struct{ dsn string }

type userService struct{ db *dbConn }

func (s *userService) Greeting() string {
	return "connected to " + s.db.dsn
}

// Example shows factory injection: a service that depends on a database
// connection, which in turn depends on configuration. The connection is a
// singleton (shared), the service is transient (built per resolve).
func Example() {
	c := container.New()

	container.Instance(c, dbConfig{DSN: "postgres://localhost/app"})

	container.Singleton(c, func(c *container.Container) (*dbConn, error) {
		cfg, err := container.Make[dbConfig](c)
		if err != nil {
			return nil, err
		}
		return &dbConn{dsn: cfg.DSN}, nil
	})

	container.Bind(c, func(c *container.Container) (*userService, error) {
		db, err := container.Make[*dbConn](c)
		if err != nil {
			return nil, err
		}
		return &userService{db: db}, nil
	})

	svc := container.MustMake[*userService](c)
	fmt.Println(svc.Greeting())
	// Output: connected to postgres://localhost/app
}
