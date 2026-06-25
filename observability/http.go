package observability

import (
	"net/http"
	"strconv"
	"time"
)

// httpOptions configures the server Middleware and the client RoundTripper.
// Defaults are filled in by newHTTPOptions; callers tweak them with the
// With* HTTPOption functions.
type httpOptions struct {
	// spanName names the span. For the server middleware the default is
	// "http.server"; for the round tripper it is "http.client".
	spanName string

	// requestsTotal / requestDuration / inFlight are the metric names.
	requestsTotal   string
	requestDuration string
	inFlight        string

	// buckets are the histogram buckets in seconds; nil uses DefaultBuckets.
	buckets []float64

	// traceResponseHeader, when non-empty, is the response header the server
	// middleware sets to the active trace id. Empty disables it.
	traceResponseHeader string
}

// HTTPOption customizes Middleware / NewRoundTripper behavior.
type HTTPOption func(*httpOptions)

// WithSpanName overrides the span name.
func WithSpanName(name string) HTTPOption {
	return func(o *httpOptions) {
		if name != "" {
			o.spanName = name
		}
	}
}

// WithMetricNames overrides the metric names. An empty string for any
// argument keeps the current (default) name for that metric.
func WithMetricNames(requestsTotal, requestDuration, inFlight string) HTTPOption {
	return func(o *httpOptions) {
		if requestsTotal != "" {
			o.requestsTotal = requestsTotal
		}
		if requestDuration != "" {
			o.requestDuration = requestDuration
		}
		if inFlight != "" {
			o.inFlight = inFlight
		}
	}
}

// WithBuckets sets the latency histogram buckets (upper bounds in seconds).
// Passing nil keeps DefaultBuckets.
func WithBuckets(buckets []float64) HTTPOption {
	return func(o *httpOptions) {
		if buckets != nil {
			o.buckets = buckets
		}
	}
}

// WithTraceResponseHeader sets the response header the server middleware uses
// to echo the active trace id back to the caller. Pass "" to disable it.
func WithTraceResponseHeader(name string) HTTPOption {
	return func(o *httpOptions) {
		o.traceResponseHeader = name
	}
}

func newServerOptions(opts ...HTTPOption) httpOptions {
	o := httpOptions{
		spanName:            "http.server",
		requestsTotal:       "http_server_requests_total",
		requestDuration:     "http_server_request_duration_seconds",
		inFlight:            "http_server_in_flight_requests",
		traceResponseHeader: "Trace-Id",
	}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// Middleware returns net/http middleware that wraps each request in a server
// span and records request metrics against reg.
//
// For every request it:
//   - extracts an inbound W3C traceparent and continues that trace (the new
//     server span becomes a child of the remote span);
//   - starts a span (default name "http.server") carrying http.method and
//     http.target attributes;
//   - increments an in-flight gauge for the duration of the request;
//   - records a request counter and a latency histogram labeled by method and
//     status code on completion;
//   - sets the span status from the response status code (5xx -> error);
//   - injects the active trace id into the response header (default
//     "Trace-Id"), and the request context carries the span context so
//     downstream handlers and loggers can read it.
//
// Either tracer or reg may be nil to disable that half independently.
func Middleware(tracer Tracer, reg *Registry, opts ...HTTPOption) func(http.Handler) http.Handler {
	o := newServerOptions(opts...)

	var inFlight Gauge
	if reg != nil {
		inFlight = reg.Gauge(o.inFlight)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Continue a remote trace if the inbound request carries one.
			if remote, ok := ExtractHTTP(r.Header); ok {
				ctx = ContextWithSpanContext(ctx, remote)
			}

			var span Span
			if tracer != nil {
				ctx, span = tracer.Start(ctx, o.spanName)
				span.SetAttr("http.method", r.Method).
					SetAttr("http.target", r.URL.RequestURI())
				if r.Host != "" {
					span.SetAttr("http.host", r.Host)
				}
				if o.traceResponseHeader != "" {
					if sc := span.Context(); sc.Valid() {
						w.Header().Set(o.traceResponseHeader, sc.TraceID)
					}
				}
			}

			if inFlight != nil {
				inFlight.Inc()
				defer inFlight.Dec()
			}

			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			start := time.Now()
			next.ServeHTTP(sw, r.WithContext(ctx))
			elapsed := time.Since(start).Seconds()

			status := sw.status
			statusStr := strconv.Itoa(status)

			if reg != nil {
				reg.Counter(o.requestsTotal,
					"method", r.Method,
					"status", statusStr,
				).Inc()
				reg.Histogram(o.requestDuration, o.buckets,
					"method", r.Method,
					"status", statusStr,
				).Observe(elapsed)
			}

			if span != nil {
				span.SetAttr("http.status_code", status)
				if status >= 500 {
					span.SetStatus(StatusError, http.StatusText(status))
				} else {
					span.SetStatus(StatusOK, "")
				}
				span.End()
			}
		})
	}
}

// statusWriter wraps http.ResponseWriter to capture the response status code.
// It exposes Unwrap so the stdlib http.ResponseController can reach optional
// interfaces (Flusher, Hijacker, Pusher, ReadFrom, ...) on the underlying
// writer without this wrapper having to re-implement each one.
type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// Mirror net/http: an implicit 200 on first Write.
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying ResponseWriter so the stdlib
// http.ResponseController can locate optional interfaces (Flusher, Hijacker,
// etc.) on it. This is the Go 1.20+ idiom and avoids hand-rolling a wrapper
// per optional interface.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
