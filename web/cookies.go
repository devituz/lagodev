package web

import (
	"net/http"
	"time"
)

// CookieOption customises a cookie set via c.SetCookie.
type CookieOption func(*http.Cookie)

// CookieMaxAge sets the Max-Age in seconds (0 means session cookie).
func CookieMaxAge(seconds int) CookieOption {
	return func(c *http.Cookie) { c.MaxAge = seconds }
}

// CookieExpires sets the Expires attribute. Prefer CookieMaxAge.
func CookieExpires(t time.Time) CookieOption {
	return func(c *http.Cookie) { c.Expires = t }
}

// CookiePath overrides the cookie Path (default "/").
func CookiePath(p string) CookieOption {
	return func(c *http.Cookie) { c.Path = p }
}

// CookieDomain restricts the cookie to a domain.
func CookieDomain(d string) CookieOption {
	return func(c *http.Cookie) { c.Domain = d }
}

// CookieInsecure marks the cookie as **not** Secure. Use only for local
// HTTP development. The default is Secure=true.
func CookieInsecure() CookieOption {
	return func(c *http.Cookie) { c.Secure = false }
}

// CookieReadable marks the cookie as readable by JavaScript (turns off
// HttpOnly). Use only when the client must echo the value into a header
// — e.g. CSRF double-submit tokens.
func CookieReadable() CookieOption {
	return func(c *http.Cookie) { c.HttpOnly = false }
}

// CookieSameSite overrides the SameSite attribute (default Lax).
func CookieSameSite(s http.SameSite) CookieOption {
	return func(c *http.Cookie) { c.SameSite = s }
}

// SetCookie writes a cookie with Laravel-grade safe defaults:
//
//	Path     = "/"
//	HttpOnly = true   (use CookieReadable() to expose to JS)
//	Secure   = true   (use CookieInsecure() in local HTTP dev)
//	SameSite = Lax    (use CookieSameSite() to override)
func (c *Context) SetCookie(name, value string, opts ...CookieOption) {
	ck := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	for _, o := range opts {
		o(ck)
	}
	http.SetCookie(c.Writer, ck)
}

// Cookie returns the value of the named cookie, or "" if missing.
func (c *Context) Cookie(name string) string {
	ck, err := c.Request.Cookie(name)
	if err != nil || ck == nil {
		return ""
	}
	return ck.Value
}

// ClearCookie expires the named cookie immediately.
func (c *Context) ClearCookie(name string, opts ...CookieOption) {
	ck := &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	for _, o := range opts {
		o(ck)
	}
	http.SetCookie(c.Writer, ck)
}
