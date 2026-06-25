package telescope

import (
	"crypto/subtle"
	"net/http"
)

// RequireBasicAuth wraps h so that every request must carry HTTP Basic
// credentials matching username and password before the wrapped handler runs.
// It is the documented, dependency-free way to gate the dashboard when it must
// be reachable on a network that is not already private.
//
// The handler returned by Handler is intentionally auth-agnostic so it can be
// mounted behind whatever middleware an application already uses (session auth,
// an IP allowlist, a reverse proxy, a VPN). When none of those apply, wrap it:
//
//	dash := rec.Handler(telescope.HandlerOptions{})
//	mux.Handle("/telescope/", http.StripPrefix("/telescope",
//		telescope.RequireBasicAuth("ops", os.Getenv("TELESCOPE_PASSWORD"), dash)))
//
// Credentials are compared in constant time to avoid leaking their length or
// content through timing. An empty username and password disables the guard and
// panics at construction, so a misconfigured deployment fails loudly instead of
// silently exposing the dashboard.
func RequireBasicAuth(username, password string, h http.Handler) http.Handler {
	if username == "" && password == "" {
		panic("telescope: RequireBasicAuth requires a non-empty username or password")
	}
	wantUser := []byte(username)
	wantPass := []byte(password)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, ok := req.BasicAuth()
		// subtle.ConstantTimeCompare returns 1 only when both length and
		// content match; the bitwise AND keeps the whole check constant time.
		userOK := subtle.ConstantTimeCompare([]byte(user), wantUser) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), wantPass) == 1
		if !ok || !(userOK && passOK) {
			w.Header().Set("WWW-Authenticate", `Basic realm="telescope", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, req)
	})
}
