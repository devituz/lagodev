// Package observability provides a vendor-neutral, dependency-free
// observability layer for the lagodev framework: tracing, metrics and
// trace-correlated logging.
//
// The package is built around small interfaces (Tracer, Span, Meter,
// Counter, Gauge, Histogram) so that the stdlib default implementation can
// be swapped for a real backend (for example OpenTelemetry) through an
// adapter sub-module without touching call sites. A Provider bundles a
// Tracer and a Registry (the default Meter) so the integration layer wires
// from one object.
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
// Middleware, wrap an http.RoundTripper with NewRoundTripper, and expose the
// registry with MetricsHandler. A Provider bundles a Tracer and a Registry so
// the whole stack wires from one object.
//
// # Example
//
//	p := observability.NewProvider(nil, nil) // default Tracer + Registry
//
//	mux := http.NewServeMux()
//	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
//		_, span := p.Tracer().Start(r.Context(), "say-hello")
//		defer span.End()
//		w.Write([]byte("hi"))
//	})
//	mux.Handle("/metrics", p.MetricsHandler())
//
//	srv := p.Middleware()(mux)
//	http.ListenAndServe(":8080", srv)
package observability
