package observability

import (
	"net/http"
	"strconv"
	"time"
)

func newClientOptions(opts ...HTTPOption) httpOptions {
	o := httpOptions{
		spanName:        "http.client",
		requestsTotal:   "http_client_requests_total",
		requestDuration: "http_client_request_duration_seconds",
		// in-flight and trace response header are unused on the client side.
	}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// roundTripper is the client-side tracing/metrics http.RoundTripper.
type roundTripper struct {
	base   http.RoundTripper
	tracer Tracer
	reg    *Registry
	opts   httpOptions
}

// NewRoundTripper wraps base with a RoundTripper that, for each outbound call,
// opens a client span (default name "http.client"), injects the active span as
// a W3C traceparent header into the outgoing request, and records an outbound
// request counter and latency histogram labeled by method and status code.
//
// If base is nil, http.DefaultTransport is used. Either tracer or reg may be
// nil to disable that half independently. Install it on an http.Client:
//
//	client := &http.Client{
//		Transport: observability.NewRoundTripper(nil, tracer, reg),
//	}
func NewRoundTripper(base http.RoundTripper, tracer Tracer, reg *Registry, opts ...HTTPOption) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	rt := &roundTripper{
		base:   base,
		tracer: tracer,
		reg:    reg,
		opts:   newClientOptions(opts...),
	}
	return rt
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	var span Span
	if rt.tracer != nil {
		ctx, span = rt.tracer.Start(ctx, rt.opts.spanName)
		span.SetAttr("http.method", req.Method).
			SetAttr("http.url", req.URL.String())
	}

	// Per the RoundTripper contract the transport must not modify the passed
	// request; clone it (also rebinds the span-carrying context) before
	// injecting the trace header.
	out := req.Clone(ctx)
	if span != nil {
		InjectHTTP(out.Header, span.Context())
	} else if sc, ok := SpanContextFromContext(ctx); ok {
		// No tracer, but the caller already has an active span context:
		// still propagate it so the trace continues downstream.
		InjectHTTP(out.Header, sc)
	}

	start := time.Now()
	resp, err := rt.base.RoundTrip(out)
	elapsed := time.Since(start).Seconds()

	status := "error"
	if resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}

	if rt.reg != nil {
		rt.reg.Counter(rt.opts.requestsTotal,
			"method", req.Method,
			"status", status,
		).Inc()
		rt.reg.Histogram(rt.opts.requestDuration, rt.opts.buckets,
			"method", req.Method,
			"status", status,
		).Observe(elapsed)
	}

	if span != nil {
		if err != nil {
			span.RecordError(err)
		} else {
			span.SetAttr("http.status_code", resp.StatusCode)
			if resp.StatusCode >= 500 {
				span.SetStatus(StatusError, http.StatusText(resp.StatusCode))
			} else {
				span.SetStatus(StatusOK, "")
			}
		}
		span.End()
	}

	return resp, err
}
