package observability

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rtFunc adapts a function to http.RoundTripper.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRoundTripperInjectsAndRecords(t *testing.T) {
	tr, rec := recorderTracer(t)
	reg := NewRegistry()

	var gotTraceparent string
	base := rtFunc(func(r *http.Request) (*http.Response, error) {
		gotTraceparent = r.Header.Get("Traceparent")
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})

	client := &http.Client{Transport: NewRoundTripper(base, tr, reg)}

	resp, err := client.Get("http://example.test/v1/thing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()

	if gotTraceparent == "" {
		t.Fatal("traceparent not injected into outbound request")
	}
	parsed, ok := ParseTraceparent(gotTraceparent)
	if !ok {
		t.Fatalf("injected traceparent invalid: %q", gotTraceparent)
	}

	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "http.client" {
		t.Errorf("span name = %q", s.Name)
	}
	if s.Status != StatusOK {
		t.Errorf("span status = %v", s.Status)
	}
	// Injected header must carry this span's identifiers.
	if parsed.TraceID != s.Context.TraceID || parsed.SpanID != s.Context.SpanID {
		t.Errorf("injected ids %s/%s != span ids %s/%s",
			parsed.TraceID, parsed.SpanID, s.Context.TraceID, s.Context.SpanID)
	}

	if v := reg.Counter("http_client_requests_total", "method", "GET", "status", "200").Value(); v != 1 {
		t.Errorf("outbound counter = %v, want 1", v)
	}
	if snap := reg.Histogram("http_client_request_duration_seconds", nil, "method", "GET", "status", "200").Snapshot(); snap.Count != 1 {
		t.Errorf("outbound latency count = %d, want 1", snap.Count)
	}
}

func TestRoundTripperContinuesParentSpan(t *testing.T) {
	tr, rec := recorderTracer(t)

	// Open a parent server span, then make a client call from its context.
	ctx, parent := tr.Start(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "http.server")

	base := rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	rt := NewRoundTripper(base, tr, NewRegistry())

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x.test/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	parent.End()

	var client SpanRecord
	var found bool
	for _, s := range rec.Spans() {
		if s.Name == "http.client" {
			client = s
			found = true
		}
	}
	if !found {
		t.Fatal("client span not recorded")
	}
	if client.Context.TraceID != parent.Context().TraceID {
		t.Errorf("client trace %q != parent trace %q", client.Context.TraceID, parent.Context().TraceID)
	}
	if client.Context.ParentID != parent.Context().SpanID {
		t.Errorf("client parent %q != parent span %q", client.Context.ParentID, parent.Context().SpanID)
	}
}

func TestRoundTripperRecordsError(t *testing.T) {
	tr, rec := recorderTracer(t)
	reg := NewRegistry()

	wantErr := errors.New("dial failed")
	base := rtFunc(func(r *http.Request) (*http.Response, error) { return nil, wantErr })
	rt := NewRoundTripper(base, tr, reg)

	req, _ := http.NewRequest(http.MethodGet, "http://x.test/", nil)
	_, err := rt.RoundTrip(req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}

	spans := rec.Spans()
	if len(spans) != 1 || spans[0].Status != StatusError {
		t.Fatalf("expected one error span, got %+v", spans)
	}
	if v := reg.Counter("http_client_requests_total", "method", "GET", "status", "error").Value(); v != 1 {
		t.Errorf("error counter = %v, want 1", v)
	}
}

func TestRoundTripperDoesNotMutateOriginalRequest(t *testing.T) {
	base := rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	rt := NewRoundTripper(base, NewTracer(), NewRegistry())

	req, _ := http.NewRequest(http.MethodGet, "http://x.test/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if req.Header.Get("Traceparent") != "" {
		t.Error("original request header was mutated")
	}
}
