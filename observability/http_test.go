package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func recorderTracer(t *testing.T) (Tracer, Recorder) {
	t.Helper()
	tr := NewTracer()
	rec, ok := tr.(Recorder)
	if !ok {
		t.Fatal("default tracer must implement Recorder")
	}
	return tr, rec
}

func TestMiddlewareRecordsSpanAndMetrics(t *testing.T) {
	tr, rec := recorderTracer(t)
	reg := NewRegistry()

	var sawCtx bool
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := SpanContextFromContext(r.Context()); ok {
			sawCtx = true
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	})

	h := Middleware(tr, reg)(final)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/users/42", nil))

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rr.Code)
	}
	if !sawCtx {
		t.Fatal("handler did not see span context on request")
	}
	if rr.Header().Get("Trace-Id") == "" {
		t.Fatal("Trace-Id response header not injected")
	}

	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Name != "http.server" {
		t.Errorf("span name = %q", s.Name)
	}
	if s.Status != StatusOK {
		t.Errorf("span status = %v, want ok", s.Status)
	}
	if s.Attrs["http.method"] != http.MethodGet {
		t.Errorf("http.method attr = %v", s.Attrs["http.method"])
	}
	if s.Attrs["http.status_code"] != http.StatusCreated {
		t.Errorf("http.status_code attr = %v", s.Attrs["http.status_code"])
	}
	if rr.Header().Get("Trace-Id") != s.Context.TraceID {
		t.Errorf("Trace-Id header %q != span trace id %q", rr.Header().Get("Trace-Id"), s.Context.TraceID)
	}

	if v := reg.Counter("http_server_requests_total", "method", "GET", "status", "201").Value(); v != 1 {
		t.Errorf("request counter = %v, want 1", v)
	}
	if snap := reg.Histogram("http_server_request_duration_seconds", nil, "method", "GET", "status", "201").Snapshot(); snap.Count != 1 {
		t.Errorf("latency histogram count = %d, want 1", snap.Count)
	}
}

func TestMiddlewareContinuesRemoteTrace(t *testing.T) {
	tr, rec := recorderTracer(t)

	h := Middleware(tr, NewRegistry())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	const remoteTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	const remoteSpan = "00f067aa0ba902b7"
	req.Header.Set("Traceparent", "00-"+remoteTrace+"-"+remoteSpan+"-01")

	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("len(spans) = %d", len(spans))
	}
	if spans[0].Context.TraceID != remoteTrace {
		t.Errorf("trace id = %q, want continued %q", spans[0].Context.TraceID, remoteTrace)
	}
	if spans[0].Context.ParentID != remoteSpan {
		t.Errorf("parent id = %q, want %q", spans[0].Context.ParentID, remoteSpan)
	}
}

func TestMiddlewareServerErrorStatus(t *testing.T) {
	tr, rec := recorderTracer(t)
	reg := NewRegistry()

	h := Middleware(tr, reg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))

	spans := rec.Spans()
	if len(spans) != 1 || spans[0].Status != StatusError {
		t.Fatalf("expected one error span, got %+v", spans)
	}
	if v := reg.Counter("http_server_requests_total", "method", "POST", "status", "500").Value(); v != 1 {
		t.Errorf("500 counter = %v, want 1", v)
	}
}

func TestMiddlewareInFlightGauge(t *testing.T) {
	reg := NewRegistry()
	gauge := reg.Gauge("http_server_in_flight_requests")

	h := Middleware(NewTracer(), reg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := gauge.Value(); v != 1 {
			t.Errorf("in-flight during request = %v, want 1", v)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if v := gauge.Value(); v != 0 {
		t.Errorf("in-flight after request = %v, want 0", v)
	}
}

// flushTracker reports whether Flush reached the underlying writer through the
// statusWriter via http.ResponseController.
type flushTracker struct {
	http.ResponseWriter
	flushed bool
}

func (f *flushTracker) Flush() { f.flushed = true }

func TestMiddlewarePreservesFlusher(t *testing.T) {
	h := Middleware(NewTracer(), NewRegistry())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush via ResponseController: %v", err)
		}
	}))

	ft := &flushTracker{ResponseWriter: httptest.NewRecorder()}
	h.ServeHTTP(ft, httptest.NewRequest(http.MethodGet, "/", nil))

	if !ft.flushed {
		t.Fatal("Flush did not reach underlying writer through statusWriter")
	}
}

func TestMiddlewareNilTracerAndRegistry(t *testing.T) {
	// Should not panic with both halves disabled.
	h := Middleware(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}
