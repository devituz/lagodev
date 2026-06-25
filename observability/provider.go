package observability

import "net/http"

// Provider bundles a Tracer and a Registry so the whole observability stack
// can be wired with a single object. It is a thin convenience over the
// primitives: Provider holds no state of its own beyond the two roots, and
// every method delegates to the package-level helpers.
//
//	p := observability.NewProvider(observability.NewTracer(), observability.NewRegistry())
//	mux := http.NewServeMux()
//	mux.Handle("/metrics", p.MetricsHandler())
//	srv := p.Middleware()(mux)
//	client := &http.Client{Transport: p.RoundTripper(nil)}
type Provider struct {
	tracer Tracer
	reg    *Registry
}

// NewProvider builds a Provider. If tracer is nil a default Tracer is created;
// if reg is nil a fresh Registry is created.
func NewProvider(tracer Tracer, reg *Registry) *Provider {
	if tracer == nil {
		tracer = NewTracer()
	}
	if reg == nil {
		reg = NewRegistry()
	}
	return &Provider{tracer: tracer, reg: reg}
}

// Tracer returns the bundled Tracer.
func (p *Provider) Tracer() Tracer { return p.tracer }

// Registry returns the bundled Registry (the default Meter).
func (p *Provider) Registry() *Registry { return p.reg }

// Middleware returns the server middleware wired to this Provider's Tracer and
// Registry. See Middleware for the full behavior and available options.
func (p *Provider) Middleware(opts ...HTTPOption) func(http.Handler) http.Handler {
	return Middleware(p.tracer, p.reg, opts...)
}

// RoundTripper returns a client RoundTripper wired to this Provider's Tracer
// and Registry, wrapping base (nil uses http.DefaultTransport).
func (p *Provider) RoundTripper(base http.RoundTripper, opts ...HTTPOption) http.RoundTripper {
	return NewRoundTripper(base, p.tracer, p.reg, opts...)
}

// MetricsHandler returns an http.Handler exposing this Provider's Registry in
// the Prometheus text exposition format.
func (p *Provider) MetricsHandler() http.Handler {
	return MetricsHandler(p.reg)
}
