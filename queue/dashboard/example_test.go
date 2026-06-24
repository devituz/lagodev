package dashboard_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/devituz/lagodev/queue"
	"github.com/devituz/lagodev/queue/dashboard"
)

// Example wires the in-memory Inspector with a couple of dead-lettered jobs,
// mounts the dashboard Handler, and queries both the HTML overview and the
// JSON stats endpoint over httptest.
func Example() {
	// Counters the worker feeds, plus the dead-letter store.
	stats := dashboard.NewStats()
	failed := dashboard.NewMemoryFailedJobs(nil) // forget-only; no requeue needed here

	// Simulate two jobs on the "emails" queue that exhausted their retries.
	for _, j := range []queue.Job{
		{ID: "job-1", Name: "SendWelcomeEmail", Attempts: 3},
		{ID: "job-2", Name: "SendWelcomeEmail", Attempts: 3},
	} {
		stats.OnPushed("emails")
		stats.OnFailed("emails")
		failed.Record(j, "emails", errors.New("smtp: connection refused"))
	}

	srv := httptest.NewServer(dashboard.Handler(dashboard.NewInspector(stats, failed)))
	defer srv.Close()

	// 1) HTML overview must render and mention the queue.
	overview, err := http.Get(srv.URL + "/queues/")
	if err != nil {
		panic(err)
	}
	body, _ := io.ReadAll(overview.Body)
	overview.Body.Close()
	fmt.Println("overview status:", overview.StatusCode)
	fmt.Println("overview html:", containsAll(string(body), "Queue Overview", "emails"))

	// 2) JSON stats snapshot — read the failed count deterministically.
	resp, err := http.Get(srv.URL + "/queues/api/stats")
	if err != nil {
		panic(err)
	}
	fmt.Println("stats content-type:", resp.Header.Get("Content-Type"))

	var sv dashboard.StatsView
	if err := json.NewDecoder(resp.Body).Decode(&sv); err != nil {
		panic(err)
	}
	resp.Body.Close()

	fmt.Println("queues:", len(sv.Queues))
	fmt.Println("queue name:", sv.Queues[0].Queue)
	fmt.Println("queue failed:", sv.Queues[0].Failed)
	fmt.Println("totals failed:", sv.Totals.Failed)

	// 3) The Inspector lists both dead-lettered jobs.
	rows, _ := dashboard.NewInspector(stats, failed).Failed(1)
	fmt.Println("failed rows:", len(rows))

	// Output:
	// overview status: 200
	// overview html: true
	// stats content-type: application/json; charset=utf-8
	// queues: 1
	// queue name: emails
	// queue failed: 2
	// totals failed: 2
	// failed rows: 2
}

// containsAll reports whether s contains every substring in subs.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
