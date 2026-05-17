// Package services is the framework-agnostic API surface for the blog.
// All business logic lives here; HTTP handlers (and gRPC/GraphQL/cron jobs)
// just call these methods.
package services

import (
	"context"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/orm"
	"github.com/devituz/lagodev/relations"

	"github.com/devituz/lagodev/examples/blog/models"
)

type PostService struct {
	Conn *database.Connection
}

func NewPostService(conn *database.Connection) *PostService { return &PostService{Conn: conn} }

// ListWithAuthor returns posts ordered by recency. Each post is enriched
// with its author via eager-loading — a single extra query instead of N.
func (s *PostService) ListWithAuthor(ctx context.Context, limit int) ([]models.Post, map[uint64]*models.User, error) {
	var posts []models.Post
	if err := orm.Query[models.Post](s.Conn).
		OrderBy("created_at", "desc").
		Limit(limit).
		Get(ctx, &posts); err != nil {
		return nil, nil, err
	}
	parents := make([]any, len(posts))
	for i := range posts {
		parents[i] = &posts[i]
	}
	authors := make(map[uint64]*models.User, len(posts))
	rel := relations.BelongsToOf(s.Conn, &posts, &models.User{}, "user_id")
	err := rel.LoadMany(ctx, parents, func(parent, child any) {
		p := parent.(*models.Post)
		u := child.(models.User)
		authors[p.UserID] = &u
	})
	return posts, authors, err
}

// Get loads one post by primary key.
func (s *PostService) Get(ctx context.Context, id uint64) (*models.Post, error) {
	return orm.Query[models.Post](s.Conn).Find(ctx, id)
}

// Create persists a new post.
func (s *PostService) Create(ctx context.Context, p *models.Post) error {
	return orm.Save(ctx, s.Conn, p)
}

// IncrementViews atomically bumps the views counter. Demonstrates a raw
// builder UPDATE without round-tripping through the ORM Save path.
func (s *PostService) IncrementViews(ctx context.Context, id uint64) error {
	return s.Conn.Transaction(ctx, func(tx *database.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE posts SET views = views + 1 WHERE id = "+s.Conn.Grammar.Placeholder(1),
			id,
		)
		return err
	})
}

// Pin / Unpin toggle the pinned flag.
func (s *PostService) Pin(ctx context.Context, p *models.Post)   error { p.Pinned = true; return orm.Save(ctx, s.Conn, p) }
func (s *PostService) Unpin(ctx context.Context, p *models.Post) error { p.Pinned = false; return orm.Save(ctx, s.Conn, p) }

// Delete soft-deletes the post (deleted_at is set).
func (s *PostService) Delete(ctx context.Context, p *models.Post) error {
	return orm.Delete(ctx, s.Conn, p)
}

// Comments for a post.
func (s *PostService) Comments(ctx context.Context, postID uint64) ([]models.Comment, error) {
	var out []models.Comment
	err := orm.Query[models.Comment](s.Conn).
		Where("post_id", "=", postID).
		OrderBy("created_at", "asc").
		Get(ctx, &out)
	return out, err
}
