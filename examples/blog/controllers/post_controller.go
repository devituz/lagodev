// Package controllers translates HTTP requests into PostService calls.
// The service is framework-agnostic; only this file knows about net/http.
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/orm"

	"github.com/devituz/lagodev/examples/blog/models"
	"github.com/devituz/lagodev/examples/blog/services"
)

type PostController struct {
	Service *services.PostService
}

func NewPostController(conn *database.Connection) *PostController {
	return &PostController{Service: services.NewPostService(conn)}
}

func (c *PostController) Index(w http.ResponseWriter, r *http.Request) {
	posts, authors, err := c.Service.ListWithAuthor(r.Context(), 20)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	type postWithAuthor struct {
		models.Post
		Author *models.User `json:"author,omitempty"`
	}
	out := make([]postWithAuthor, len(posts))
	for i, p := range posts {
		out[i] = postWithAuthor{Post: p, Author: authors[p.UserID]}
	}
	writeJSON(w, 200, out)
}

func (c *PostController) Show(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	p, err := c.Service.Get(r.Context(), id)
	if errors.Is(err, orm.ErrNotFound) {
		writeError(w, 404, err)
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := c.Service.IncrementViews(r.Context(), p.ID); err != nil {
		writeError(w, 500, err)
		return
	}
	p.Views++
	writeJSON(w, 200, p)
}

func (c *PostController) Store(w http.ResponseWriter, r *http.Request) {
	var p models.Post
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := c.Service.Create(r.Context(), &p); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 201, p)
}

func (c *PostController) Destroy(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	p, err := c.Service.Get(r.Context(), id)
	if errors.Is(err, orm.ErrNotFound) {
		writeError(w, 404, err)
		return
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	if err := c.Service.Delete(r.Context(), p); err != nil {
		writeError(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

func parseID(r *http.Request) (uint64, error) {
	raw := r.PathValue("id")
	if raw == "" {
		raw = r.URL.Query().Get("id")
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("invalid id")
	}
	return id, nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
