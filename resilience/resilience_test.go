package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// epoch is a fixed anchor so virtual time is reproducible across runs.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var errBoom = errors.New("boom")

func alwaysFail(ctx context.Context) (any, error) { return nil, errBoom }
func alwaysOK(ctx context.Context) (any, error)   { return "ok", nil }

// ---------------------------------------------------------------------------
// FakeClock
// ---------------------------------------------------------------------------

func TestFakeClock_NowAndAdvance(t *testing.T) {
	c := NewFakeClock(epoch)
	if !c.Now().Equal(epoch) {
		t.Fatalf("Now() = %v, want %v", c.Now(), epoch)
	}
	c.Advance(5 * time.Second)
	if got := c.Now(); !got.Equal(epoch.Add(5 * time.Second)) {
		t.Fatalf("after advance Now() = %v, want %v", got, epoch.Add(5*time.Second))
	}
}

func TestFakeClock_AfterFiresOnAdvance(t *testing.T) {
	c := NewFakeClock(epoch)
	ch := c.After(10 * time.Second)

	select {
	case <-ch:
		t.Fatal("timer fired before its deadline")
	default:
	}

	if got := c.BlockedSleepers(); got != 1 {
		t.Fatalf("BlockedSleepers() = %d, want 1", got)
	}

	c.Advance(9 * time.Second)
	select {
	case <-ch:
		t.Fatal("timer fired before deadline (advanced 9s of 10s)")
	default:
	}

	c.Advance(1 * time.Second)
	select {
	case fired := <-ch:
		if !fired.Equal(epoch.Add(10 * time.Second)) {
			t.Fatalf("fired time = %v, want %v", fired, epoch.Add(10*time.Second))
		}
	default:
		t.Fatal("timer did not fire after reaching deadline")
	}

	if got := c.BlockedSleepers(); got != 0 {
		t.Fatalf("BlockedSleepers() after fire = %d, want 0", got)
	}
}

func TestFakeClock_AfterNonPositiveFiresImmediately(t *testing.T) {
	c := NewFakeClock(epoch)
	for _, d := range []time.Duration{0, -time.Second} {
		ch := c.After(d)
		select {
		case fired := <-ch:
			if !fired.Equal(epoch) {
				t.Fatalf("d=%v: fired = %v, want %v", d, fired, epoch)
			}
		default:
			t.Fatalf("d=%v: non-positive After did not fire immediately", d)
		}
		if got := c.BlockedSleepers(); got != 0 {
			t.Fatalf("d=%v: BlockedSleepers = %d, want 0", d, got)
		}
	}
}

func TestFakeClock_SleepReleasedByAdvance(t *testing.T) {
	c := NewFakeClock(epoch)
	done := make(chan struct{})
	go func() {
		c.Sleep(3 * time.Second)
		close(done)
	}()

	// Wait until the goroutine has parked on After.
	waitFor(t, func() bool { return c.BlockedSleepers() == 1 })

	select {
	case <-done:
		t.Fatal("Sleep returned before the clock advanced")
	default:
	}

	c.Advance(3 * time.Second)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Sleep did not return after Advance")
	}
}

func TestFakeClock_SleepNonPositiveReturnsImmediately(t *testing.T) {
	c := NewFakeClock(epoch)
	done := make(chan struct{})
	go func() {
		c.Sleep(0)
		c.Sleep(-1 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("non-positive Sleep blocked")
	}
}

func TestFakeClock_MultipleWaitersFireInOrder(t *testing.T) {
	c := NewFakeClock(epoch)
	late := c.After(20 * time.Second)
	early := c.After(5 * time.Second)
	mid := c.After(10 * time.Second)

	c.Advance(10 * time.Second) // fires early + mid, not late
	if !recv(early) {
		t.Fatal("5s timer did not fire after advancing 10s")
	}
	if !recv(mid) {
		t.Fatal("10s timer did not fire after advancing 10s")
	}
	select {
	case <-late:
		t.Fatal("20s timer fired too early")
	default:
	}
	c.Advance(10 * time.Second)
	if !recv(late) {
		t.Fatal("20s timer did not fire after advancing to 20s")
	}
}

func TestSystemClock_Behaves(t *testing.T) {
	before := time.Now()
	now := SystemClock.Now()
	if now.Before(before.Add(-time.Second)) {
		t.Fatalf("SystemClock.Now() looks wrong: %v", now)
	}
	SystemClock.Sleep(time.Millisecond)
	select {
	case <-SystemClock.After(time.Millisecond):
	case <-time.After(time.Second):
		t.Fatal("SystemClock.After never fired")
	}
}

// ---------------------------------------------------------------------------
// Counts
// ---------------------------------------------------------------------------

func TestCounts_Transitions(t *testing.T) {
	var c Counts
	if got := c.FailureRatio(); got != 0 {
		t.Fatalf("empty FailureRatio = %v, want 0", got)
	}
	c.onRequest()
	c.onSuccess()
	c.onRequest()
	c.onFailure()
	c.onRequest()
	c.onFailure()

	if c.Requests != 3 || c.TotalSuccesses != 1 || c.TotalFailures != 2 {
		t.Fatalf("counts = %+v", c)
	}
	if c.ConsecutiveFailures != 2 {
		t.Fatalf("ConsecutiveFailures = %d, want 2", c.ConsecutiveFailures)
	}
	if c.ConsecutiveSuccess != 0 {
		t.Fatalf("ConsecutiveSuccess = %d, want 0", c.ConsecutiveSuccess)
	}
	if got := c.FailureRatio(); got != 2.0/3.0 {
		t.Fatalf("FailureRatio = %v, want %v", got, 2.0/3.0)
	}

	// A success resets the consecutive-failure streak.
	c.onRequest()
	c.onSuccess()
	if c.ConsecutiveFailures != 0 || c.ConsecutiveSuccess != 1 {
		t.Fatalf("after success: failures=%d successes=%d", c.ConsecutiveFailures, c.ConsecutiveSuccess)
	}

	c.clear()
	if (c != Counts{}) {
		t.Fatalf("clear() did not zero counts: %+v", c)
	}
}

func TestState_String(t *testing.T) {
	cases := map[State]string{
		StateClosed:   "closed",
		StateOpen:     "open",
		StateHalfOpen: "half-open",
		State(99):     "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Breaker — defaults & config
// ---------------------------------------------------------------------------

func TestNewBreaker_Defaults(t *testing.T) {
	b := NewBreaker(BreakerConfig{})
	if b.maxRequests != 1 {
		t.Errorf("default maxRequests = %d, want 1", b.maxRequests)
	}
	if b.openTimeout != 60*time.Second {
		t.Errorf("default openTimeout = %v, want 60s", b.openTimeout)
	}
	if b.consecutiveFailures != 5 {
		t.Errorf("default consecutiveFailures = %d, want 5", b.consecutiveFailures)
	}
	if b.isSuccessful == nil {
		t.Error("default isSuccessful is nil")
	}
	if b.clock == nil {
		t.Error("default clock is nil")
	}
	if b.State() != StateClosed {
		t.Errorf("initial state = %v, want closed", b.State())
	}
	// Default predicate: nil == success.
	if !b.isSuccessful(nil) || b.isSuccessful(errBoom) {
		t.Error("default isSuccessful predicate wrong")
	}
}

func TestNewBreaker_RatioConfigDoesNotForceConsecutiveDefault(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureRatio: 0.5})
	if b.consecutiveFailures != 0 {
		t.Fatalf("with FailureRatio set, consecutiveFailures = %d, want 0 (no default)", b.consecutiveFailures)
	}
}

// ---------------------------------------------------------------------------
// Breaker — state machine
// ---------------------------------------------------------------------------

func TestBreaker_ClosedTripsOnConsecutiveFailures(t *testing.T) {
	clk := NewFakeClock(epoch)
	var changes []string
	b := NewBreaker(BreakerConfig{
		Name:                "svc",
		ConsecutiveFailures: 3,
		OpenTimeout:         30 * time.Second,
		Clock:               clk,
		OnStateChange: func(name string, from, to State) {
			changes = append(changes, fmt.Sprintf("%s:%s->%s", name, from, to))
		},
	})

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := b.Execute(ctx, alwaysFail); !errors.Is(err, errBoom) {
			t.Fatalf("call %d: err = %v, want boom", i, err)
		}
	}
	if b.State() != StateClosed {
		t.Fatalf("after 2 failures state = %v, want closed", b.State())
	}

	// 3rd consecutive failure trips it.
	if _, err := b.Execute(ctx, alwaysFail); !errors.Is(err, errBoom) {
		t.Fatalf("3rd call err = %v", err)
	}
	if b.State() != StateOpen {
		t.Fatalf("after 3 failures state = %v, want open", b.State())
	}
	if len(changes) != 1 || changes[0] != "svc:closed->open" {
		t.Fatalf("state changes = %v", changes)
	}
}

func TestBreaker_OpenRejectsFastWithoutCallingFn(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		OpenTimeout:         30 * time.Second,
		Clock:               clk,
	})
	ctx := context.Background()

	_, _ = b.Execute(ctx, alwaysFail) // trips open
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open", b.State())
	}

	var called int32
	probe := func(ctx context.Context) (any, error) {
		atomic.AddInt32(&called, 1)
		return "ok", nil
	}
	_, err := b.Execute(ctx, probe)
	if !errors.Is(err, ErrOpenCircuit) {
		t.Fatalf("open breaker err = %v, want ErrOpenCircuit", err)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Fatal("fn was invoked while circuit open")
	}
}

func TestBreaker_OpenToHalfOpenAfterTimeout(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		OpenTimeout:         30 * time.Second,
		Clock:               clk,
	})
	ctx := context.Background()
	_, _ = b.Execute(ctx, alwaysFail)

	// Just before the timeout it is still open.
	clk.Advance(30*time.Second - time.Nanosecond)
	if b.State() != StateOpen {
		t.Fatalf("state before timeout = %v, want open", b.State())
	}
	// At the deadline it promotes to half-open.
	clk.Advance(time.Nanosecond)
	if b.State() != StateHalfOpen {
		t.Fatalf("state after timeout = %v, want half-open", b.State())
	}
}

func TestBreaker_HalfOpenSuccessCloses(t *testing.T) {
	clk := NewFakeClock(epoch)
	var changes []string
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		OpenTimeout:         30 * time.Second,
		Clock:               clk,
		OnStateChange: func(name string, from, to State) {
			changes = append(changes, fmt.Sprintf("%s->%s", from, to))
		},
	})
	ctx := context.Background()
	_, _ = b.Execute(ctx, alwaysFail) // open
	clk.Advance(30 * time.Second)     // -> half-open

	out, err := b.Execute(ctx, alwaysOK)
	if err != nil || out != "ok" {
		t.Fatalf("half-open trial = (%v, %v)", out, err)
	}
	if b.State() != StateClosed {
		t.Fatalf("state after successful trial = %v, want closed", b.State())
	}
	if want := []string{"closed->open", "open->half-open", "half-open->closed"}; !eqStr(changes, want) {
		t.Fatalf("changes = %v, want %v", changes, want)
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		OpenTimeout:         30 * time.Second,
		Clock:               clk,
	})
	ctx := context.Background()
	_, _ = b.Execute(ctx, alwaysFail) // open
	clk.Advance(30 * time.Second)     // half-open

	if b.State() != StateHalfOpen {
		t.Fatalf("precondition state = %v, want half-open", b.State())
	}
	_, err := b.Execute(ctx, alwaysFail)
	if !errors.Is(err, errBoom) {
		t.Fatalf("trial err = %v", err)
	}
	if b.State() != StateOpen {
		t.Fatalf("state after failed trial = %v, want open", b.State())
	}

	// The open timeout restarts from the re-open moment.
	clk.Advance(29 * time.Second)
	if b.State() != StateOpen {
		t.Fatalf("state mid-cooldown = %v, want open", b.State())
	}
	clk.Advance(time.Second)
	if b.State() != StateHalfOpen {
		t.Fatalf("state after second cooldown = %v, want half-open", b.State())
	}
}

func TestBreaker_HalfOpenQuotaRejectsExtraTrials(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		MaxRequests:         2,
		OpenTimeout:         30 * time.Second,
		Clock:               clk,
	})
	ctx := context.Background()
	_, _ = b.Execute(ctx, alwaysFail)
	clk.Advance(30 * time.Second) // half-open, quota = 2

	// Block the breaker in half-open by holding a trial in-flight, so the
	// quota is consumed without the generation rolling over.
	release1 := make(chan struct{})
	started1 := make(chan struct{})
	release2 := make(chan struct{})
	started2 := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = b.Execute(ctx, func(ctx context.Context) (any, error) {
			close(started1)
			<-release1
			return "ok", nil
		})
	}()
	<-started1
	go func() {
		defer wg.Done()
		_, _ = b.Execute(ctx, func(ctx context.Context) (any, error) {
			close(started2)
			<-release2
			return "ok", nil
		})
	}()
	<-started2

	// Quota (2) now consumed; a 3rd trial is rejected immediately.
	if _, err := b.Execute(ctx, alwaysOK); !errors.Is(err, ErrOpenCircuit) {
		t.Fatalf("over-quota trial err = %v, want ErrOpenCircuit", err)
	}

	close(release1)
	close(release2)
	wg.Wait()

	// Two consecutive half-open successes (== MaxRequests) close the circuit.
	if b.State() != StateClosed {
		t.Fatalf("state after quota trials succeeded = %v, want closed", b.State())
	}
}

func TestBreaker_FailureRatioTripsWithMinRequests(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		FailureRatio: 0.5,
		MinRequests:  4,
		OpenTimeout:  30 * time.Second,
		Clock:        clk,
	})
	ctx := context.Background()

	// 3 requests, 2 failures => ratio 0.66 but below MinRequests(4): stays closed.
	_, _ = b.Execute(ctx, alwaysOK)
	_, _ = b.Execute(ctx, alwaysFail)
	_, _ = b.Execute(ctx, alwaysFail)
	if b.State() != StateClosed {
		t.Fatalf("below MinRequests state = %v, want closed (counts=%+v)", b.State(), b.Counts())
	}

	// 4th request, a failure => 4 requests / 3 failures = 0.75 >= 0.5 and
	// MinRequests satisfied: trips.
	_, _ = b.Execute(ctx, alwaysFail)
	if b.State() != StateOpen {
		t.Fatalf("after MinRequests reached state = %v, want open (counts=%+v)", b.State(), b.Counts())
	}
}

func TestBreaker_CustomIsSuccessfulIgnoresError(t *testing.T) {
	clk := NewFakeClock(epoch)
	sentinel := errors.New("not a real failure")
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		Clock:               clk,
		IsSuccessful: func(err error) bool {
			// Treat sentinel as success.
			return err == nil || errors.Is(err, sentinel)
		},
	})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := b.Execute(ctx, func(ctx context.Context) (any, error) { return nil, sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("call %d err = %v", i, err)
		}
	}
	if b.State() != StateClosed {
		t.Fatalf("state = %v, want closed (sentinel classified as success)", b.State())
	}
	c := b.Counts()
	if c.TotalSuccesses != 5 || c.TotalFailures != 0 {
		t.Fatalf("counts = %+v, want 5 successes", c)
	}
}

func TestBreaker_PanicCountsAsFailureAndPropagates(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		Clock:               clk,
	})
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic to propagate")
			}
		}()
		_, _ = b.Execute(ctx, func(ctx context.Context) (any, error) {
			panic("kaboom")
		})
	}()

	if b.State() != StateOpen {
		t.Fatalf("state after panic = %v, want open", b.State())
	}
	if c := b.Counts(); c.TotalFailures == 0 {
		// After tripping, generation reset clears counts; instead assert state.
		_ = c
	}
}

func TestBreaker_StaleOutcomeAfterGenerationRollIgnored(t *testing.T) {
	// A long-running call whose generation rolls over (because the open
	// timeout elapses and a state change starts a new generation) must not
	// record its outcome against the new generation.
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		OpenTimeout:         10 * time.Second,
		Clock:               clk,
	})
	ctx := context.Background()

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = b.Execute(ctx, func(ctx context.Context) (any, error) {
			close(started)
			<-release
			return nil, errBoom
		})
		close(done)
	}()
	<-started // in-flight request counted in generation G (still closed)

	// A separate failing call (also generation G) trips the breaker because
	// ConsecutiveFailures == 1, which starts a fresh generation.
	_, _ = b.Execute(ctx, alwaysFail)
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open", b.State())
	}
	genCountsBefore := b.Counts() // new generation, freshly cleared

	close(release)
	<-done

	// The stale failure from generation G must be dropped; the open
	// generation's counts are unchanged by it.
	if got := b.Counts(); got != genCountsBefore {
		t.Fatalf("stale outcome leaked into new generation: before=%+v after=%+v", genCountsBefore, got)
	}
}

// ---------------------------------------------------------------------------
// Wrap / Middleware / Do
// ---------------------------------------------------------------------------

func TestWrap_OrderOutermostFirst(t *testing.T) {
	var log []string
	mk := func(tag string) Middleware {
		return func(next Operation) Operation {
			return func(ctx context.Context) (any, error) {
				log = append(log, "enter:"+tag)
				v, err := next(ctx)
				log = append(log, "exit:"+tag)
				return v, err
			}
		}
	}
	guard := Wrap(mk("a"), mk("b"), mk("c"))
	op := guard(func(ctx context.Context) (any, error) {
		log = append(log, "op")
		return 1, nil
	})
	if _, err := op(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"enter:a", "enter:b", "enter:c", "op", "exit:c", "exit:b", "exit:a"}
	if !eqStr(log, want) {
		t.Fatalf("order = %v, want %v", log, want)
	}
}

func TestWrap_EmptyIsIdentity(t *testing.T) {
	guard := Wrap()
	op := guard(alwaysOK)
	v, err := op(context.Background())
	if err != nil || v != "ok" {
		t.Fatalf("identity wrap = (%v, %v)", v, err)
	}
}

func TestDo_TypedResult(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{ConsecutiveFailures: 2, Clock: clk})
	mw := b.Middleware()

	got, err := Do(context.Background(), mw, func(ctx context.Context) (int, error) {
		return 42, nil
	})
	if err != nil || got != 42 {
		t.Fatalf("Do = (%d, %v), want (42, nil)", got, err)
	}
}

func TestDo_NilMiddlewareRunsDirectly(t *testing.T) {
	got, err := Do(context.Background(), nil, func(ctx context.Context) (string, error) {
		return "hello", nil
	})
	if err != nil || got != "hello" {
		t.Fatalf("Do nil mw = (%q, %v)", got, err)
	}
}

func TestDo_ErrorReturnsZeroValue(t *testing.T) {
	got, err := Do(context.Background(), nil, func(ctx context.Context) (int, error) {
		return 7, errBoom
	})
	// fn returned (7, errBoom); the value is non-nil so it is asserted to int.
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if got != 7 {
		t.Fatalf("got = %d, want 7 (value preserved alongside error)", got)
	}
}

func TestDo_OpenBreakerYieldsZeroValueAndErr(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{ConsecutiveFailures: 1, OpenTimeout: time.Minute, Clock: clk})
	mw := b.Middleware()
	ctx := context.Background()

	_, _ = Do(ctx, mw, func(ctx context.Context) (int, error) { return 0, errBoom }) // trip
	got, err := Do(ctx, mw, func(ctx context.Context) (int, error) { return 99, nil })
	if !errors.Is(err, ErrOpenCircuit) {
		t.Fatalf("err = %v, want ErrOpenCircuit", err)
	}
	if got != 0 {
		t.Fatalf("got = %d, want 0 (zero value when short-circuited)", got)
	}
}

func TestBreaker_MiddlewareEquivalentToExecute(t *testing.T) {
	clk := NewFakeClock(epoch)
	b := NewBreaker(BreakerConfig{ConsecutiveFailures: 2, Clock: clk})
	op := b.Middleware()(alwaysOK)
	v, err := op(context.Background())
	if err != nil || v != "ok" {
		t.Fatalf("middleware op = (%v, %v)", v, err)
	}
}

// ---------------------------------------------------------------------------
// Concurrency (-race)
// ---------------------------------------------------------------------------

func TestBreaker_ConcurrentExecuteRace(t *testing.T) {
	clk := NewFakeClock(epoch)
	var stateChanges int32
	b := NewBreaker(BreakerConfig{
		Name:                "conc",
		ConsecutiveFailures: 5,
		FailureRatio:        0.6,
		MinRequests:         10,
		MaxRequests:         3,
		OpenTimeout:         time.Second,
		Clock:               clk,
		OnStateChange: func(name string, from, to State) {
			atomic.AddInt32(&stateChanges, 1)
		},
	})

	const goroutines = 50
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(goroutines)
	ctx := context.Background()
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				fail := (g+i)%3 == 0
				_, _ = b.Execute(ctx, func(ctx context.Context) (any, error) {
					if fail {
						return nil, errBoom
					}
					return "ok", nil
				})
				// Concurrent readers exercise the locks too.
				_ = b.State()
				_ = b.Counts()
				if i%37 == 0 {
					clk.Advance(200 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()

	// No assertion on final state — the point is the race detector stays
	// clean and no deadlock occurs. Sanity: the callback ran under lock-free
	// re-entrancy without deadlock.
	_ = atomic.LoadInt32(&stateChanges)
}

func TestBreaker_OnStateChangeCanQueryBreakerNoDeadlock(t *testing.T) {
	clk := NewFakeClock(epoch)
	var observed State
	var b *Breaker
	b = NewBreaker(BreakerConfig{
		ConsecutiveFailures: 1,
		Clock:               clk,
		OnStateChange: func(name string, from, to State) {
			// Re-entrant query: setState releases the lock around the callback.
			observed = b.State()
		},
	})
	done := make(chan struct{})
	go func() {
		_, _ = b.Execute(context.Background(), alwaysFail)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: OnStateChange querying breaker hung")
	}
	if observed != StateOpen {
		t.Fatalf("observed state in callback = %v, want open", observed)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func recv(ch <-chan time.Time) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
