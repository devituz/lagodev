// Package mock provides small testing helpers modelled on Laravel's
// Mock facade — the bits applications reach for over and over in
// tests without pulling in a full mocking framework.
//
// Three primitives:
//
//   - Clock     — controllable time source. Inject as time.Now() and
//                 Advance it deterministically.
//   - Calls     — generic call recorder. Counts and stores arguments
//                 each time a function is invoked.
//   - HTTPServer — pre-canned httptest.Server with route-by-method
//                 responses and recorded request inspection.
package mock

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// ----------------------------------------------------------------------
// Clock
// ----------------------------------------------------------------------

// Clock is a controllable time source for tests. Inject it as a
// `func() time.Time` so production callers still use time.Now without
// changes.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a Clock fixed at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start} }

// Now returns the current frozen instant.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d. Negative durations rewind.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// Set jumps the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// ----------------------------------------------------------------------
// Calls — generic call recorder
// ----------------------------------------------------------------------

// Calls records each invocation of a tracked function. T is the
// argument tuple — typically a struct that captures the relevant
// fields:
//
//	type sendCall struct{ To, Subject string }
//	calls := mock.NewCalls[sendCall]()
//	mailer.SendFunc = func(to, subj string) {
//	    calls.Record(sendCall{to, subj})
//	}
//	// later:
//	calls.AssertCount(t, 2)
//	last := calls.Last()
type Calls[T any] struct {
	mu    sync.Mutex
	items []T
}

// NewCalls returns an empty recorder.
func NewCalls[T any]() *Calls[T] { return &Calls[T]{} }

// Record appends arg to the call list.
func (c *Calls[T]) Record(arg T) {
	c.mu.Lock()
	c.items = append(c.items, arg)
	c.mu.Unlock()
}

// Count returns how many times Record has been called.
func (c *Calls[T]) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// At returns the i-th recorded argument. Panics on out-of-range.
func (c *Calls[T]) At(i int) T {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[i]
}

// Last returns the most recent recorded argument (zero value if none).
func (c *Calls[T]) Last() T {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	if len(c.items) == 0 {
		return zero
	}
	return c.items[len(c.items)-1]
}

// All returns a copy of the recorded list.
func (c *Calls[T]) All() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]T, len(c.items))
	copy(out, c.items)
	return out
}

// Reset wipes the recorded list.
func (c *Calls[T]) Reset() {
	c.mu.Lock()
	c.items = nil
	c.mu.Unlock()
}

// testingT is a sliver of *testing.T so mock can call AssertCount
// without importing testing in production code paths.
type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

// AssertCount fails t if the recorded count differs from want.
func (c *Calls[T]) AssertCount(t testingT, want int) {
	t.Helper()
	if got := c.Count(); got != want {
		t.Fatalf("mock: call count = %d, want %d", got, want)
	}
}

// AssertCalled fails t if no calls have been recorded.
func (c *Calls[T]) AssertCalled(t testingT) {
	t.Helper()
	if c.Count() == 0 {
		t.Fatalf("mock: expected at least one call, got 0")
	}
}

// AssertNotCalled fails t if any calls have been recorded.
func (c *Calls[T]) AssertNotCalled(t testingT) {
	t.Helper()
	if c.Count() != 0 {
		t.Fatalf("mock: expected no calls, got %d", c.Count())
	}
}

// ----------------------------------------------------------------------
// HTTPServer — programmable httptest wrapper
// ----------------------------------------------------------------------

// HTTPServer is a thin wrapper around httptest.Server with route-by-
// (method, path) registration and request capture. Suitable for
// shaping responses from external APIs in tests.
type HTTPServer struct {
	server   *httptest.Server
	mu       sync.Mutex
	routes   map[string]routeResponse
	requests []RecordedRequest
}

// RecordedRequest captures the relevant bits of an incoming request.
type RecordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// routeResponse is the canned reply for a (method, path) key.
type routeResponse struct {
	status int
	body   []byte
	header http.Header
}

// NewHTTPServer starts a fresh httptest.Server. Always call Close().
func NewHTTPServer() *HTTPServer {
	h := &HTTPServer{routes: map[string]routeResponse{}}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	return h
}

// URL returns the base URL of the test server (e.g. http://127.0.0.1:PORT).
func (h *HTTPServer) URL() string { return h.server.URL }

// Close releases the underlying server.
func (h *HTTPServer) Close() { h.server.Close() }

// On registers a canned response for (method, path).
//
//	srv.On("GET", "/users/42").Reply(200, []byte(`{"id":42}`))
func (h *HTTPServer) On(method, path string) *RouteBuilder {
	return &RouteBuilder{server: h, method: method, path: path}
}

// OnJSON registers a JSON response.
func (h *HTTPServer) OnJSON(method, path string, status int, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	h.On(method, path).Header("Content-Type", "application/json").Reply(status, b)
	return nil
}

// Requests returns a copy of every request the server has received.
func (h *HTTPServer) Requests() []RecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]RecordedRequest, len(h.requests))
	copy(out, h.requests)
	return out
}

// LastRequest returns the most recent request (or zero if none).
func (h *HTTPServer) LastRequest() RecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) == 0 {
		return RecordedRequest{}
	}
	return h.requests[len(h.requests)-1]
}

// Reset clears recorded requests but keeps registered routes.
func (h *HTTPServer) Reset() {
	h.mu.Lock()
	h.requests = nil
	h.mu.Unlock()
}

func (h *HTTPServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	rec := RecordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Body:   body,
	}
	h.mu.Lock()
	h.requests = append(h.requests, rec)
	key := r.Method + " " + r.URL.Path
	resp, ok := h.routes[key]
	h.mu.Unlock()
	if !ok {
		http.Error(w, "mock: no route for "+key, http.StatusNotImplemented)
		return
	}
	for k, vs := range resp.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.status)
	_, _ = w.Write(resp.body)
}

// RouteBuilder accumulates a response on a registered route.
type RouteBuilder struct {
	server *HTTPServer
	method string
	path   string
	header http.Header
}

// Header adds a response header.
func (b *RouteBuilder) Header(k, v string) *RouteBuilder {
	if b.header == nil {
		b.header = http.Header{}
	}
	b.header.Add(k, v)
	return b
}

// Reply finalises the route with the given status and body.
func (b *RouteBuilder) Reply(status int, body []byte) {
	b.server.mu.Lock()
	defer b.server.mu.Unlock()
	b.server.routes[b.method+" "+b.path] = routeResponse{
		status: status,
		body:   append([]byte(nil), body...),
		header: b.header,
	}
}

// ReplyJSON marshals body as JSON and registers the route.
func (b *RouteBuilder) ReplyJSON(status int, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	b.Header("Content-Type", "application/json")
	b.Reply(status, buf)
	return nil
}

// ----------------------------------------------------------------------
// Body utilities
// ----------------------------------------------------------------------

// MustReadBody drains r.Body and returns its bytes. Panics on read
// error — tests calling MustReadBody assume a well-formed reader.
func MustReadBody(r io.Reader) []byte {
	if r == nil {
		return nil
	}
	b, err := io.ReadAll(r)
	if err != nil {
		panic(err)
	}
	return b
}

// BodyJSON unmarshals the request body into dst. Returns nil on
// empty body so tests can guard.
func BodyJSON(b []byte, dst any) error {
	if len(b) == 0 {
		return errors.New("mock: empty body")
	}
	return json.NewDecoder(bytes.NewReader(b)).Decode(dst)
}
