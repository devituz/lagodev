package session

import (
	"context"
	"net/http"
)

// ctxKey is the type used to stash *Session on the request context.
type ctxKey struct{}

// Middleware returns a framework-agnostic net/http middleware. It
// starts the session at the top of each request, makes the *Session
// available via session.FromRequest(r), and saves the session before
// the handler returns.
//
// The middleware is compatible with any router that speaks the stdlib
// http.Handler interface (Gin, Fiber adapters, Chi, Echo, plain
// net/http, lagodev's web package). Use it once per app:
//
//	mw := session.Middleware(mgr)
//	mux.Handle("/", mw(http.HandlerFunc(handler)))
//
// If Save fails (store unreachable), the failure is silently absorbed
// — the response has already begun streaming by the time the handler
// returns. Wrap a custom logger via FailureHandler if you need to be
// notified.
func (m *Manager) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			s, err := m.Start(ctx, r)
			if err != nil {
				http.Error(w, "session error", http.StatusInternalServerError)
				return
			}
			// Make session available to the handler via r.Context().
			r = r.WithContext(context.WithValue(ctx, ctxKey{}, s))

			// Buffer responses minimally — we need to inject the
			// Set-Cookie header BEFORE the handler writes its body.
			// Save eagerly when the handler attempts the first write.
			sw := &sessionWriter{ResponseWriter: w, ctx: ctx, sess: s}
			next.ServeHTTP(sw, r)
			// Flush in case handler returned without writing anything.
			sw.flushSave()
		})
	}
}

// FromRequest pulls the *Session attached by Middleware. Returns nil
// if the middleware was not applied to the request.
func FromRequest(r *http.Request) *Session {
	v := r.Context().Value(ctxKey{})
	if v == nil {
		return nil
	}
	return v.(*Session)
}

// FromContext is the bare-ctx variant. Returns nil when no session is
// attached.
func FromContext(ctx context.Context) *Session {
	v := ctx.Value(ctxKey{})
	if v == nil {
		return nil
	}
	return v.(*Session)
}

// sessionWriter saves the session on the first response write so the
// Set-Cookie header lands before the body. WriteHeader and Write both
// trigger Save exactly once.
type sessionWriter struct {
	http.ResponseWriter
	ctx   context.Context
	sess  *Session
	saved bool
}

func (s *sessionWriter) flushSave() {
	if s.saved {
		return
	}
	s.saved = true
	_ = s.sess.Save(s.ctx, s.ResponseWriter)
}

func (s *sessionWriter) WriteHeader(code int) {
	s.flushSave()
	s.ResponseWriter.WriteHeader(code)
}

func (s *sessionWriter) Write(b []byte) (int, error) {
	s.flushSave()
	return s.ResponseWriter.Write(b)
}
