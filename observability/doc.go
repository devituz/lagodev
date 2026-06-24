// Package observability provides a vendor-neutral, dependency-free
// observability layer for the lagodev framework: tracing, metrics and
// trace-correlated logging.
//
// The package is built around small interfaces (Tracer, Span, Meter,
// Counter, Gauge, Histogram) so that the stdlib default implementation can
// be swapped for a real backend (for example OpenTelemetry) through an
// adapter sub-module without touching call sites. A Provider bundles a
// Tracer, a Meter and an *slog.Logger and can be installed as a swappable
// global default or injected explicitly.
//
// Everything here uses only the Go standard library. No OpenTelemetry or
// Prometheus client dependency is required:
//
//   - Tracing records spans into an in-process ring buffer (for a future
//     Telescope dashboard) and can emit them through log/slog. W3C
//     traceparent propagation is implemented for net/http headers.
//   - Metrics are kept in an in-memory registry that renders the Prometheus
//     text exposition format directly and also publishes to expvar.
//   - Logging is an slog.Handler wrapper that injects trace_id / span_id
//     from the context into every record.
//
// # Opt-in wrappers
//
// The middleware and helpers are returned for the caller to apply; this
// package never edits web, orm or other packages. Wrap an http.Handler with
// HTTPMiddleware, wrap an http.RoundTripper with RoundTripper, time a query
// with TimeQuery and record cache hits with NewCacheStats.
//
// # Example
//
//	p := observability.NewProvider(observability.Options{ServiceName: "api"})
//	observability.SetGlobal(p)
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
//		_, span := p.Tracer().Start(r.Context(), "say-hello")
//		defer span.End()
//		w.Write([]byte("hi"))
//	})
//	mux.Handle("/metrics", p.MetricsHandler())
//
//	srv := observability.HTTPMiddleware(p)(mux)
//	http.ListenAndServe(":8080", srv)
package observability
