package telescope

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Middleware returns a net/http middleware that times each request, assigns
// it a telescope request id and records a Request entry once the wrapped
// handler returns. The request id is injected into the request context (via
// ContextWithRequestID) so any Record* calls made while handling the request
// are correlated with the Request entry, and it is mirrored onto the
// X-Request-ID response header.
//
// An inbound X-Request-ID header is honoured when present so the id can be
// propagated across services; otherwise a fresh one is generated.
//
//	rec := telescope.NewRecorder(telescope.Options{})
//	srv := rec.Middleware()(mux)
//	http.ListenAndServe(":8080", srv)
func (r *Recorder) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			id := strings.TrimSpace(req.Header.Get("X-Request-ID"))
			if id == "" {
				id = newRequestID()
			}
			ctx := ContextWithRequestID(req.Context(), id)
			req = req.WithContext(ctx)
			w.Header().Set("X-Request-ID", id)

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			start := r.now()
			next.ServeHTTP(sw, req)
			elapsed := r.now().Sub(start)

			r.RecordRequest(ctx, RequestEntry{
				Method:   req.Method,
				Path:     req.URL.Path,
				Status:   sw.status,
				Duration: elapsed,
				IP:       clientIP(req),
			})
		})
	}
}

// statusWriter captures the response status code so Middleware can record it.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so streaming responses are not
// buffered when Middleware wraps the writer.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// newRequestID returns a random 16-hex-character request id.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read does not fail on a healthy host; fall back to a
		// time-derived id rather than failing the request.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// clientIP extracts the remote address, preferring X-Forwarded-For's first
// hop when present so reverse-proxied requests show the real client.
func clientIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := req.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		return addr[:i]
	}
	return addr
}
