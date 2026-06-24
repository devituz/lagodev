package observability_test

import (
	"context"
	"fmt"

	"github.com/devituz/lagodev/observability"
)

// Example traces a parent/child span pair and records a counter and a
// histogram, then prints only the stable facts (names, the parent/child
// link, and metric aggregates) — never the random trace/span IDs.
func Example() {
	tr := observability.NewTracer()

	// Root span for an inbound request.
	ctx, parent := tr.Start(context.Background(), "http.request")
	parent.SetAttr("http.method", "GET")
	parent.SetAttr("http.route", "/users/{id}")

	// Child span for the DB query; deriving it from ctx links it to parent.
	_, child := tr.Start(ctx, "db.query")
	child.SetAttr("db.system", "postgres")
	child.SetStatus(observability.StatusOK, "")
	child.End()

	parent.SetStatus(observability.StatusOK, "")
	parent.End()

	pc := parent.Context()
	cc := child.Context()

	fmt.Println("parent name:", "http.request")
	fmt.Println("child name:", "db.query")
	fmt.Println("same trace:", cc.TraceID == pc.TraceID)
	fmt.Println("child is child of parent:", cc.ParentID == pc.SpanID)
	fmt.Println("parent is root:", pc.ParentID == "")

	// Metrics: a request counter and a latency histogram.
	reg := observability.NewRegistry()
	reqs := reg.Counter("http_requests_total", "route", "/users")
	reqs.Inc()
	reqs.Inc()
	reqs.Add(3)

	lat := reg.Histogram("http_request_seconds", nil, "route", "/users")
	lat.Observe(0.012)
	lat.Observe(0.4)
	lat.Observe(1.2)

	snap := lat.Snapshot()
	fmt.Printf("counter value: %.0f\n", reqs.Value())
	fmt.Println("histogram count:", snap.Count)
	fmt.Printf("histogram sum: %.3f\n", snap.Sum)

	// Output:
	// parent name: http.request
	// child name: db.query
	// same trace: true
	// child is child of parent: true
	// parent is root: true
	// counter value: 5
	// histogram count: 3
	// histogram sum: 1.612
}
