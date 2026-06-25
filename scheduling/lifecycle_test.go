package scheduling

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// settle gives the scheduler's goroutines a moment to unwind, then
// returns the live goroutine count after a GC. Used to assert that a
// Run/Stop cycle leaves nothing lingering.
func settle() int {
	for i := 0; i < 10; i++ {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

// TestRunner_NoGoroutineLeak runs many Run/Stop cycles and asserts the
// goroutine count returns to baseline. The Runner spawns one watcher
// goroutine plus one per fired task; all must be reaped on Stop.
func TestRunner_NoGoroutineLeak(t *testing.T) {
	base := settle()

	for i := 0; i < 30; i++ {
		r := New().Tick(2 * time.Millisecond)
		r.Job("x", Every(2*time.Millisecond), func(_ context.Context) error {
			time.Sleep(time.Millisecond)
			return nil
		})
		done := make(chan struct{})
		go func() { _ = r.Run(context.Background()); close(done) }()
		time.Sleep(15 * time.Millisecond)
		r.Stop()
		<-done
	}

	after := settle()
	// Allow a small slack for the test runtime's own goroutines.
	if after > base+2 {
		t.Fatalf("goroutine leak: base=%d after=%d", base, after)
	}
}

// TestRunner_StopAfterCtxCancelNoLeak covers the watcher goroutine path
// where the caller's ctx is cancelled (not Stop). The watcher selecting
// on ctx.Done()/taskCtx.Done() must exit.
func TestRunner_CtxCancelNoLeak(t *testing.T) {
	base := settle()
	for i := 0; i < 30; i++ {
		r := New().Tick(2 * time.Millisecond)
		r.Job("x", Every(2*time.Millisecond), func(_ context.Context) error { return nil })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = r.Run(ctx); close(done) }()
		time.Sleep(10 * time.Millisecond)
		cancel()
		<-done
	}
	after := settle()
	if after > base+2 {
		t.Fatalf("goroutine leak on ctx cancel: base=%d after=%d", base, after)
	}
}

// TestDue_EveryBoundary checks Every fires exactly at/after the interval
// boundary and never before. Driven by explicit prev/now — no wall-clock
// sleeps.
func TestDue_EveryBoundary(t *testing.T) {
	s := Every(time.Minute)
	prev := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		delta time.Duration
		want  bool
	}{
		{59 * time.Second, false},
		{60 * time.Second, true},
		{61 * time.Second, true},
		{0, false},
	}
	for _, c := range cases {
		if got := s.Due(prev, prev.Add(c.delta)); got != c.want {
			t.Fatalf("Every(1m).Due(+%v) = %v, want %v", c.delta, got, c.want)
		}
	}
}

// TestDue_DailyAtBoundary checks DailyAt fires only within the configured
// minute and only once per day.
func TestDue_DailyAtBoundary(t *testing.T) {
	s := DailyAt("02:30")
	day1 := time.Date(2026, 6, 25, 2, 30, 0, 0, time.Local)
	day2 := time.Date(2026, 6, 26, 2, 30, 0, 0, time.Local)

	// New day, configured minute: fire.
	if !s.Due(day1, day2) {
		t.Fatal("DailyAt must fire at the configured minute on a new day")
	}
	// One minute early: no fire.
	if s.Due(day1, day2.Add(-time.Minute)) {
		t.Fatal("DailyAt must not fire before the configured minute")
	}
	// One minute late: no fire.
	if s.Due(day1, day2.Add(time.Minute)) {
		t.Fatal("DailyAt must not fire after the configured minute")
	}
	// Same minute already fired today: no refire.
	if s.Due(day2, day2.Add(30*time.Second)) {
		t.Fatal("DailyAt must not refire within the same minute")
	}
}

// TestDue_DailyBoundary verifies Daily() == DailyAt("00:00") fires at
// midnight on a fresh day only.
func TestDue_DailyBoundary(t *testing.T) {
	s := Daily()
	prev := time.Date(2026, 6, 25, 0, 0, 0, 0, time.Local)
	midnight := time.Date(2026, 6, 26, 0, 0, 0, 0, time.Local)
	if !s.Due(prev, midnight) {
		t.Fatal("Daily must fire at midnight on a new day")
	}
	if s.Due(prev, time.Date(2026, 6, 26, 0, 1, 0, 0, time.Local)) {
		t.Fatal("Daily must not fire one minute past midnight")
	}
}

// TestRunner_OverlapNoPileup asserts a task slower than its interval never
// runs concurrently with itself and does not accumulate goroutines.
func TestRunner_OverlapNoPileup(t *testing.T) {
	base := settle()
	r := New().Tick(2 * time.Millisecond)
	var concurrent, maxConcurrent, total int32
	r.Job("slow", Every(2*time.Millisecond), func(_ context.Context) error {
		n := atomic.AddInt32(&concurrent, 1)
		for {
			m := atomic.LoadInt32(&maxConcurrent)
			if n <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, n) {
				break
			}
		}
		atomic.AddInt32(&total, 1)
		time.Sleep(25 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = r.Run(ctx)

	if atomic.LoadInt32(&maxConcurrent) > 1 {
		t.Fatalf("overlapping runs: maxConcurrent=%d", maxConcurrent)
	}
	// Over 200ms with a 25ms body and skip-on-overlap, at most ~8 runs.
	if got := atomic.LoadInt32(&total); got > 12 {
		t.Fatalf("runs piled up: total=%d", got)
	}
	after := settle()
	if after > base+2 {
		t.Fatalf("goroutine leak after overlap test: base=%d after=%d", base, after)
	}
}

// TestRunner_ConcurrentTickStopTasks exercises Run, Stop, and Tasks
// concurrently for the race detector.
func TestRunner_ConcurrentTickStopTasks(t *testing.T) {
	r := New().Tick(time.Millisecond)
	r.Job("a", Every(time.Millisecond), func(_ context.Context) error { return nil })
	r.Job("b", Every(2*time.Millisecond), func(_ context.Context) error { return nil })

	done := make(chan struct{})
	go func() { _ = r.Run(context.Background()); close(done) }()

	stopReaders := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopReaders:
				return
			default:
				_ = r.Tasks()
			}
		}
	}()

	time.Sleep(30 * time.Millisecond)
	close(stopReaders)
	r.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}
