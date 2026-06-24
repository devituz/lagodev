package scheduling

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestEvery_FiresAtInterval(t *testing.T) {
	s := Every(50 * time.Millisecond)
	now := time.Now()
	if s.Due(now, now.Add(20*time.Millisecond)) {
		t.Fatal("must not fire before interval")
	}
	if !s.Due(now, now.Add(60*time.Millisecond)) {
		t.Fatal("must fire after interval")
	}
}

func TestEvery_PanicsOnNonPositive(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("must panic on zero interval")
		}
	}()
	_ = Every(0)
}

func TestDailyAt_HHMM(t *testing.T) {
	s := DailyAt("02:30")
	// yesterday's lastRun, fire today at 02:30
	prev := time.Date(2026, 6, 1, 2, 30, 0, 0, time.Local)
	now := time.Date(2026, 6, 2, 2, 30, 0, 0, time.Local)
	if !s.Due(prev, now) {
		t.Fatal("must fire at the configured minute on a new day")
	}
	// same day at 02:30 must NOT re-fire
	if s.Due(now, now) {
		t.Fatal("must not refire within the same minute")
	}
	// outside the configured minute
	if s.Due(prev, time.Date(2026, 6, 2, 3, 0, 0, 0, time.Local)) {
		t.Fatal("must not fire outside HH:MM")
	}
}

func TestDailyAt_PanicsOnBadInput(t *testing.T) {
	for _, bad := range []string{"", "9:00:00", "25:00", "02:99", "abc:00"} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("DailyAt(%q) must panic", bad)
				}
			}()
			_ = DailyAt(bad)
		}()
	}
}

func TestRunner_FiresRegisteredTask(t *testing.T) {
	r := New().Tick(10 * time.Millisecond)
	var hits int32
	r.Job("ping", Every(15*time.Millisecond), func(_ context.Context) error {
		atomic.AddInt32(&hits, 1)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("expected at least 2 hits, got %d", hits)
	}
}

func TestRunner_OverlapPrevented(t *testing.T) {
	r := New().Tick(5 * time.Millisecond)
	var concurrent int32
	var maxConcurrent int32
	r.Job("slow", Every(5*time.Millisecond), func(_ context.Context) error {
		n := atomic.AddInt32(&concurrent, 1)
		for {
			m := atomic.LoadInt32(&maxConcurrent)
			if n <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if maxConcurrent > 1 {
		t.Fatalf("task ran concurrently with itself: maxConcurrent=%d", maxConcurrent)
	}
}

func TestRunner_DuplicateNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("duplicate name must panic")
		}
	}()
	r := New()
	r.Job("x", Every(time.Second), func(_ context.Context) error { return nil })
	r.Job("x", Every(time.Second), func(_ context.Context) error { return nil })
}

func TestRunner_TasksSnapshot(t *testing.T) {
	r := New()
	r.Job("a", Every(time.Minute), func(_ context.Context) error { return nil })
	r.Job("b", Hourly(), func(_ context.Context) error { return nil })
	info := r.Tasks()
	if len(info) != 2 {
		t.Fatalf("len = %d", len(info))
	}
	names := map[string]bool{}
	for _, i := range info {
		names[i.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Fatalf("missing task names: %v", info)
	}
}

func TestRunner_StopExitsRun(t *testing.T) {
	r := New().Tick(10 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		_ = r.Run(context.Background())
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	r.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after Stop")
	}
}

// TestRunner_StopDrainsInFlightTask ensures Stop waits for a task that
// has already fired to finish (graceful drain) instead of returning
// while it is still running.
func TestRunner_StopDrainsInFlightTask(t *testing.T) {
	r := New().Tick(5 * time.Millisecond)
	started := make(chan struct{})
	var finished int32
	r.Job("slow", Every(5*time.Millisecond), func(ctx context.Context) error {
		select {
		case <-started:
		default:
			close(started)
		}
		time.Sleep(150 * time.Millisecond)
		atomic.StoreInt32(&finished, 1)
		return nil
	})

	runDone := make(chan struct{})
	go func() {
		_ = r.Run(context.Background())
		close(runDone)
	}()

	<-started // task is in-flight
	r.Stop()  // signal stop while task runs
	select {  // Run must block until the task drains
	case <-runDone:
		if atomic.LoadInt32(&finished) != 1 {
			t.Fatal("Run returned before in-flight task finished (no drain)")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after drain")
	}
}

// TestRunner_RestartableAfterStop verifies a Runner can Run again after
// Stop. The one-shot stop channel previously made the second Run return
// immediately.
func TestRunner_RestartableAfterStop(t *testing.T) {
	r := New().Tick(5 * time.Millisecond)
	var hits int32
	r.Job("ping", Every(5*time.Millisecond), func(_ context.Context) error {
		atomic.AddInt32(&hits, 1)
		return nil
	})

	// First cycle.
	go func() { _ = r.Run(context.Background()) }()
	time.Sleep(40 * time.Millisecond)
	r.Stop()
	first := atomic.LoadInt32(&hits)
	if first < 1 {
		t.Fatalf("first run produced no hits: %d", first)
	}

	// Second cycle must actually run again.
	atomic.StoreInt32(&hits, 0)
	done := make(chan struct{})
	go func() { _ = r.Run(context.Background()); close(done) }()
	time.Sleep(40 * time.Millisecond)
	r.Stop()
	<-done
	if atomic.LoadInt32(&hits) < 1 {
		t.Fatal("Runner did not fire after restart")
	}
}

func TestEveryFiveMinutesAlias(t *testing.T) {
	if EveryFiveMinutes().String() != "every 5m0s" {
		t.Fatalf("description = %q", EveryFiveMinutes().String())
	}
}

func TestRunner_TaskErrorDoesNotKillRunner(t *testing.T) {
	r := New().Tick(10 * time.Millisecond)
	var hits int32
	r.Job("flaky", Every(15*time.Millisecond), func(_ context.Context) error {
		atomic.AddInt32(&hits, 1)
		if hits%2 == 0 {
			return context.Canceled // any error
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)
	if hits < 2 {
		t.Fatalf("runner died after failure; hits=%d", hits)
	}
}
