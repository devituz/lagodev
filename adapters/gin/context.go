package lagogin

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/devituz/lagodev/orm"
)

// Ctx wraps *gin.Context with helpers that match the lagodev web.Context API.
// Handlers receive *Ctx and return (any, error). The H wrapper converts the
// return value into a Gin response automatically.
type Ctx struct {
	*gin.Context
}

// newCtx — internal constructor used by H/Wrap.
func newCtx(c *gin.Context) *Ctx { return &Ctx{Context: c} }

// Ctx returns the request's context.Context.
func (c *Ctx) Ctx() context.Context { return c.Request.Context() }

// Param returns a URL path parameter (e.g. ":id").
func (c *Ctx) Param(name string) string { return c.Context.Param(name) }

// ParamUint parses a path parameter as uint64. Returns 0 on parse error.
func (c *Ctx) ParamUint(name string) uint64 {
	v, _ := strconv.ParseUint(c.Context.Param(name), 10, 64)
	return v
}

// ParamInt parses a path parameter as int. Returns 0 on parse error.
func (c *Ctx) ParamInt(name string) int {
	v, _ := strconv.Atoi(c.Context.Param(name))
	return v
}

// Query returns a URL query string parameter.
func (c *Ctx) Query(name string) string { return c.Context.Query(name) }

// QueryDefault returns the query parameter, or fallback when empty.
func (c *Ctx) QueryDefault(name, fallback string) string {
	if v := c.Context.Query(name); v != "" {
		return v
	}
	return fallback
}

// QueryInt parses a query parameter as int with a fallback.
func (c *Ctx) QueryInt(name string, fallback int) int {
	v, err := strconv.Atoi(c.Context.Query(name))
	if err != nil {
		return fallback
	}
	return v
}

// Bind decodes the JSON body into dst. On failure responds with 400 and
// returns the parse error so the handler can `return nil, err`.
func (c *Ctx) Bind(dst any) error {
	if err := c.Context.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		c.Abort()
		return err
	}
	return nil
}

// Created marks the response 201 and returns v so the handler can:
//
//	return c.Created(post), nil
func (c *Ctx) Created(v any) any {
	c.Status(http.StatusCreated)
	return v
}

// NoContent writes a 204 with no body.
func (c *Ctx) NoContent() { c.Status(http.StatusNoContent) }

// UserID — populated by AuthJWT middleware. Returns 0 when unauthenticated.
func (c *Ctx) UserID() uint64 {
	if v, ok := c.Context.Get("auth_user_id"); ok {
		if id, ok := v.(uint64); ok {
			return id
		}
	}
	return 0
}

// Role returns the auth role populated by AuthJWT, or "" when unauthenticated.
func (c *Ctx) Role() string {
	if v, ok := c.Context.Get("auth_role"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// respond converts (value, err) to a Gin response — Laravel-style.
//
//	err != nil:
//	  orm.ErrNotFound       → 404 {"error": "..."}
//	  validation.Error      → 422 {"errors": {...}}
//	  any other error       → 500 {"error": "..."}
//	value == nil:
//	  status already set    → keep it (handler already responded)
//	  otherwise             → 204 No Content
//	value != nil:
//	  status already set    → write JSON with that status
//	  otherwise             → 200 + JSON
func (c *Ctx) respond(value any, err error) {
	if err != nil {
		c.writeError(err)
		return
	}
	// If handler already wrote (c.Status / c.JSON / c.Redirect), respect it.
	if c.Writer.Written() {
		return
	}
	status := c.Writer.Status()
	if value == nil {
		if status == http.StatusOK {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		// Status was explicitly set via c.Status() — write headers only.
		c.Writer.WriteHeaderNow()
		return
	}
	if status != http.StatusOK {
		c.JSON(status, value)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (c *Ctx) writeError(err error) {
	if errors.Is(err, orm.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if ve, ok := err.(*ValidationError); ok {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"errors": ve.Fields, "message": ve.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}
