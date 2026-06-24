package telescope_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/devituz/lagodev/telescope"
)

// Example shows a real user creating a Recorder, wrapping a handler with
// Middleware(), driving it with a single httptest request, and recording two
// identical (modulo bind value) queries under that request. It then prints
// deterministic facts: how many entries were stored, the recorded types, and
// whether the N+1 burst was flagged on the repeated query.
func Example() {
	rec := telescope.NewRecorder(telescope.Options{Capacity: 100})

	// The handler records two queries that normalise to the same statement,
	// correlated with the active request id so the N+1 heuristic can fire.
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rec.RecordQuery(ctx, telescope.QueryEntry{
			SQL:      "select * from posts where id = 1",
			Duration: time.Millisecond,
		})
		rec.RecordQuery(ctx, telescope.QueryEntry{
			SQL:      "select * from posts where id = 2",
			Duration: time.Millisecond,
		})
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with Middleware: it records a Request entry when the handler returns.
	srv := httptest.NewServer(rec.Middleware()(app))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/posts")
	if err != nil {
		panic(err)
	}
	resp.Body.Close()

	// Inspect the recorded entries (newest first).
	all := rec.Entries()
	fmt.Println("total entries:", len(all))
	fmt.Println("request entries:", len(rec.Filter(telescope.TypeRequest, 0)))
	fmt.Println("query entries:", len(rec.Filter(telescope.TypeQuery, 0)))

	// The newest query (recorded second) is the N+1 repeat.
	queries := rec.Filter(telescope.TypeQuery, 0)
	repeat := queries[0]
	fmt.Println("repeat type:", repeat.Type)
	flagged, _ := repeat.Payload["n_plus_one"].(bool)
	fmt.Println("n+1 flagged:", flagged)
	fmt.Println("repeat count:", repeat.Payload["repeat_count"])

	// Output:
	// total entries: 3
	// request entries: 1
	// query entries: 2
	// repeat type: query
	// n+1 flagged: true
	// repeat count: 2
}
