// Package web is the Laravel-style HTTP framework for lagodev.
//
// Bu paket Gin/Fiber'ga muqobil sodda, framework-mustaqil API beradi.
// Asosiy tushunchalar:
//
//   - web.App — ilovaning tepa darajadagi konteyner (Router + Server + DB)
//   - web.Context — har bir so'rovga moslangan helper'lar (JSON, Bind, Param)
//   - web.Router — Laravel-uslubidagi marshrutlash (Get/Post/Resource/Group)
//   - web.ResourceController — RESTful 5 metodli interfeys
//
// Minimal misol:
//
//	app := web.New()
//	app.Get("/", func(c *web.Context) { c.String(200, "hi") })
//	app.Resource("posts", &PostController{})
//	app.Run(":8080")
package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/devituz/lagodev/database"
	"github.com/devituz/lagodev/orm"
)

// Context bitta HTTP so'rovni ifoda etadi. U handler'lar va middleware'lar
// orasidan o'tadi va so'rov/javob ustida ishlash uchun helper'lar beradi.
type Context struct {
	// Writer — yozish uchun standart http.ResponseWriter.
	Writer http.ResponseWriter
	// Request — kelgan HTTP so'rov.
	Request *http.Request
	// DB — agar App.WithDatabase orqali biriktirilgan bo'lsa, ulanish.
	// Hech qachon nil bo'lmasligi shart emas; nil bo'lganda foydalanuvchi
	// o'zi tekshiradi.
	DB *database.Connection

	// statusWritten — Status() yoki body yozish allaqachon sodir bo'lganmi.
	statusWritten bool
	// store — middleware'lar contextda saqlamoqchi bo'lgan qiymatlar.
	store map[string]any
}

// newContext yangi Context yaratadi. Foydalanuvchi to'g'ridan-to'g'ri
// chaqirmaydi — router ichida qo'llaniladi.
func newContext(w http.ResponseWriter, r *http.Request, db *database.Connection) *Context {
	return &Context{Writer: w, Request: r, DB: db}
}

// Ctx so'rovning context.Context'ini qaytaradi. DB chaqiruvlariga uzating:
//
//	user, err := orm.Query[User](c.DB).Find(c.Ctx(), id)
func (c *Context) Ctx() context.Context { return c.Request.Context() }

// ---------------------------------------------------------------------------
// Path / query parametrlari
// ---------------------------------------------------------------------------

// Param yo'l parametri qiymatini qaytaradi (masalan "/posts/{id}" → "id").
// Yo'qol bo'lsa, bo'sh qator.
func (c *Context) Param(name string) string {
	return c.Request.PathValue(name)
}

// ParamUint yo'l parametri'ni uint64 ko'rinishida qaytaradi. Xato yoki
// bo'sh bo'lsa 0 qaytadi.
func (c *Context) ParamUint(name string) uint64 {
	v, _ := strconv.ParseUint(c.Param(name), 10, 64)
	return v
}

// ParamInt yo'l parametri'ni int qiymat sifatida qaytaradi.
func (c *Context) ParamInt(name string) int {
	v, _ := strconv.Atoi(c.Param(name))
	return v
}

// Query URL-dan so'rov stringini oladi (?key=value).
func (c *Context) Query(name string) string {
	return c.Request.URL.Query().Get(name)
}

// QueryDefault Query bilan bir xil, lekin bo'sh bo'lsa fallback qaytaradi.
func (c *Context) QueryDefault(name, fallback string) string {
	if v := c.Query(name); v != "" {
		return v
	}
	return fallback
}

// QueryInt URL parametrini int sifatida o'qiydi. Xato bo'lsa fallback.
func (c *Context) QueryInt(name string, fallback int) int {
	v, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return fallback
	}
	return v
}

// ---------------------------------------------------------------------------
// Body binding (JSON)
// ---------------------------------------------------------------------------

// Bind so'rov body'sini JSON sifatida dst'ga decode qiladi. Xato bo'lsa
// avtomatik 400 Bad Request qaytaradi va sentinel xatoni qaytaradi.
func (c *Context) Bind(dst any) error {
	if c.Request.Body == nil {
		c.BadRequest("empty body")
		return errors.New("web: empty body")
	}
	if err := json.NewDecoder(c.Request.Body).Decode(dst); err != nil {
		c.BadRequest(err.Error())
		return err
	}
	return nil
}

// MustBind Bind bilan bir xil lekin xato bo'lsa true qaytaradi.
// Foydalanish: if !c.MustBind(&payload) { return }
func (c *Context) MustBind(dst any) bool {
	return c.Bind(dst) == nil
}

// ---------------------------------------------------------------------------
// Javoblar
// ---------------------------------------------------------------------------

// Status faqat status kodini yozadi, body yubormaydi.
func (c *Context) Status(code int) {
	if !c.statusWritten {
		c.Writer.WriteHeader(code)
		c.statusWritten = true
	}
}

// JSON v'ni JSON sifatida code statusi bilan yuboradi.
func (c *Context) JSON(code int, v any) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Status(code)
	_ = json.NewEncoder(c.Writer).Encode(v)
}

// String matn javobi.
func (c *Context) String(code int, body string) {
	c.Writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Status(code)
	_, _ = c.Writer.Write([]byte(body))
}

// NoContent 204 javobi (body yo'q).
func (c *Context) NoContent() { c.Status(http.StatusNoContent) }

// BadRequest 400 + JSON xato.
func (c *Context) BadRequest(msg string) {
	c.JSON(http.StatusBadRequest, map[string]string{"error": msg})
}

// NotFound 404 + JSON xato.
func (c *Context) NotFound(msg string) {
	if msg == "" {
		msg = "not found"
	}
	c.JSON(http.StatusNotFound, map[string]string{"error": msg})
}

// Unauthorized 401 + JSON xato.
func (c *Context) Unauthorized(msg string) {
	if msg == "" {
		msg = "unauthorized"
	}
	c.JSON(http.StatusUnauthorized, map[string]string{"error": msg})
}

// Forbidden 403 + JSON xato.
func (c *Context) Forbidden(msg string) {
	if msg == "" {
		msg = "forbidden"
	}
	c.JSON(http.StatusForbidden, map[string]string{"error": msg})
}

// InternalError 500 + JSON xato.
func (c *Context) InternalError(err error) {
	c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// Error err nil emas bo'lsa, mos status kodi bilan JSON javob qaytaradi.
// orm.ErrNotFound uchun 404, qolganlar uchun 500. Foydalanish:
//
//	if c.Error(err) { return }
func (c *Context) Error(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, orm.ErrNotFound) {
		c.NotFound(err.Error())
		return true
	}
	c.InternalError(err)
	return true
}

// ---------------------------------------------------------------------------
// Kontekst ichida saqlash (middleware uchun)
// ---------------------------------------------------------------------------

// Set context ichida key/value saqlaydi (masalan, authenticated user).
func (c *Context) Set(key string, value any) {
	if c.store == nil {
		c.store = make(map[string]any)
	}
	c.store[key] = value
}

// Get saqlangan qiymatni oladi.
func (c *Context) Get(key string) (any, bool) {
	if c.store == nil {
		return nil, false
	}
	v, ok := c.store[key]
	return v, ok
}

// respond — handler qaytargan (value, err) ni HTTP javobiga aylantiradi.
// Laravel uslubidagi avtomat javob mantiqi:
//
//	err != nil:                  Error(err) — orm.ErrNotFound→404, qolgani→500
//	value == nil, status set:    handler o'zi javob bergan; teginmaymiz
//	value == nil:                204 No Content
//	value != nil, status set:    faqat JSON tana yoziladi (status saqlanadi)
//	value != nil:                200 + JSON
func (c *Context) respond(value any, err error) {
	if c.Error(err) {
		return
	}
	if value == nil {
		if !c.statusWritten {
			c.NoContent()
		}
		return
	}
	if c.statusWritten {
		c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(c.Writer).Encode(value)
		return
	}
	c.JSON(http.StatusOK, value)
}

// Created — Store handler uchun qulay yordamchi. 201 statusi'ni belgilab
// foydalanuvchi return value, nil yozsa, framework JSON'ga aylantiradi:
//
//	return ctx.Created(post), nil
func (c *Context) Created(v any) any {
	c.Status(http.StatusCreated)
	return v
}

