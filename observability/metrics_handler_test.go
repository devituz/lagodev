package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerExposition(t *testing.T) {
	reg := NewRegistry()

	reg.Counter("http_requests_total", "method", "GET").Add(3)
	reg.Gauge("queue_depth").Set(7)
	h := reg.Histogram("latency_seconds", []float64{0.1, 0.5, 1}, "route", "/x")
	h.Observe(0.05)
	h.Observe(0.2)
	h.Observe(2)

	rr := httptest.NewRecorder()
	MetricsHandler(reg).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q", ct)
	}

	body := rr.Body.String()

	wantLines := []string{
		"# TYPE http_requests_total counter",
		`http_requests_total{method="GET"} 3`,
		"# TYPE queue_depth gauge",
		"queue_depth 7",
		"# TYPE latency_seconds histogram",
		`latency_seconds_bucket{route="/x",le="0.1"} 1`,
		`latency_seconds_bucket{route="/x",le="0.5"} 2`,
		`latency_seconds_bucket{route="/x",le="1"} 2`,
		`latency_seconds_bucket{route="/x",le="+Inf"} 3`,
		`latency_seconds_sum{route="/x"} 2.25`,
		`latency_seconds_count{route="/x"} 3`,
	}
	for _, want := range wantLines {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("metrics output missing line:\n  %q\nfull body:\n%s", want, body)
		}
	}
}

func TestMetricsHandlerEmptyRegistry(t *testing.T) {
	rr := httptest.NewRecorder()
	MetricsHandler(NewRegistry()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("empty registry should render nothing, got %q", rr.Body.String())
	}
}

func TestMetricsHandlerEscapesLabelValues(t *testing.T) {
	reg := NewRegistry()
	reg.Counter("c", "k", `a"b\c`).Inc()

	rr := httptest.NewRecorder()
	MetricsHandler(reg).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	want := `c{k="a\"b\\c"} 1`
	if !strings.Contains(rr.Body.String(), want) {
		t.Fatalf("escaping wrong, got:\n%s", rr.Body.String())
	}
}
