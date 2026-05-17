// Example: a microservice-style worker that reads jobs from a queue table.
// Demonstrates transactional locking via FOR UPDATE plus the ORM's
// scopes/soft-delete behavior. Postgres-leaning; runs on SQLite too with the
// caveat that FOR UPDATE is a no-op there.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/devituz/lagodev/database"
	_ "github.com/devituz/lagodev/drivers/sqlite"
	"github.com/devituz/lagodev/migrations"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/schema"
)

type Job struct {
	orm.Model
	Payload     string
	Status      string
	AttemptedAt *time.Time `column:"attempted_at"`
}

func init() {
	migrations.Register(migrations.Define("0001_jobs",
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.Create("jobs", func(t *schema.Blueprint) {
				t.ID()
				t.String("payload")
				t.String("status").Default("pending")
				t.Timestamp("attempted_at").Nullable()
				t.Timestamps()
				t.SoftDeletes()
			}))
		},
		func(ctx *migrations.Context) error {
			return ctx.Schema(schema.DropIfExists("jobs"))
		},
	))
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := database.Global.Open("default", database.Config{
		Driver: "sqlite",
		DSN:    "file::memory:?cache=shared",
	})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := migrations.New(conn, nil, migrations.Options{}).Up(ctx); err != nil {
		log.Fatal(err)
	}

	// Enqueue a handful of jobs.
	for i := 1; i <= 3; i++ {
		must(orm.Save(ctx, conn, &Job{
			Payload: fmt.Sprintf("payload-%d", i),
			Status:  "pending",
		}))
	}

	for {
		processed, err := claimAndProcess(ctx, conn)
		if errors.Is(err, orm.ErrNotFound) {
			fmt.Println("queue drained")
			return
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("processed job %d\n", processed.ID)
	}
}

// claimAndProcess locks a single pending job, marks it done, and returns it.
// Wrapping the whole operation in a transaction makes the claim atomic.
func claimAndProcess(ctx context.Context, conn *database.Connection) (*Job, error) {
	var picked *Job
	err := conn.Transaction(ctx, func(tx *database.Tx) error {
		job, err := orm.Query[Job](conn).
			WithTx(tx).
			Where("status", "=", "pending").
			OrderBy("id", "asc").
			Limit(1).
			First(ctx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		job.Status = "completed"
		job.AttemptedAt = &now
		if err := orm.Save(ctx, conn, job); err != nil {
			return err
		}
		picked = job
		return nil
	})
	return picked, err
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
